package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	prometheusApi "github.com/prometheus/client_golang/api"
	"github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	cli "gopkg.in/urfave/cli.v1"
	"log"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"time"
)

type SelectRange struct {
	Start, End time.Time
	Step       time.Duration
}

func (r SelectRange) String() string {
	return fmt.Sprintf("%v to %v with %v step.", r.Start, r.End, r.Step)
}

type metric struct {
	name   string
	values []model.SamplePair

	value float64
	time  model.Time
}

const (
	METRIC_LABEL = "__name__"
)

var lastExecuteTime time.Time
var running bool = false

var concurrency int
var sourcePrometheusUrl string
var outputPath string
var collectInterval time.Duration

func queryRangeData(concurrency chan int, api v1.API, query string, timeRange SelectRange, result chan []*metric) (model.Value, error) {
	concurrency <- 0
	rawData, queryErr := api.QueryRange(context.Background(), query, v1.Range{Start: timeRange.Start, End: timeRange.End, Step: timeRange.Step})
	if queryErr != nil {
		errMsg := fmt.Sprintf("Query range data error: %v", queryErr.Error())
		<-concurrency
		result <- []*metric{}
		return nil, errors.New(errMsg)
	}
	<-concurrency
	return rawData, nil
}

func downsampleMetrics(matrix *model.Matrix) []*metric {
	type tempCounter struct {
		sum   model.SampleValue
		count int
	}

	metrics := []*metric{}
	for _, sample := range *matrix {
		// Don't know is it have chance have metric but no value
		if len(sample.Values) < 1 {
			continue
		}

		counterByTime := make(map[model.Time]*tempCounter)

		for _, v := range sample.Values {
			recordTime := v.Timestamp - (v.Timestamp % model.Time(collectInterval/time.Millisecond))
			counter, ok := counterByTime[recordTime]
			if !ok {
				counter = &tempCounter{}
				counterByTime[recordTime] = counter
			}

			(*counterByTime[recordTime]).sum += v.Value
			(*counterByTime[recordTime]).count++

		}

		for recordTime, counter := range counterByTime {
			met := metric{name: sample.Metric.String(), value: float64(counter.sum) / float64(counter.count), time: recordTime}
			metrics = append(metrics, &met)
		}

	}
	return metrics
}

func getMetric(api v1.API, query string, timeRange SelectRange, result chan []*metric, concurrency chan int) {
	// download metric data from prometheus
	rawData, err := queryRangeData(concurrency, api, query, timeRange, result)
	if err != nil {
		// if download from prometheus got error, ignore it.
		// TODO: Change to logger
		fmt.Println(err.Error())
		result <- []*metric{}
		return
	}

	matrix, ok := rawData.(model.Matrix)
	if !ok {
		// if the download data not matrix type, ignore it.
		// TODO log which metric not ok
		errMsg := fmt.Sprintf("Query \"%v\" result not type of matrix.", query)
		fmt.Println(errMsg)
		result <- []*metric{}
		return
	}

	metrics := downsampleMetrics(&matrix)
	result <- metrics
}

func getMetricLabels(api v1.API) model.LabelValues {
	labels, getLabelErr := api.LabelValues(context.Background(), METRIC_LABEL)
	if getLabelErr != nil {
		// TODO: Change to logger
		log.Panicf("Can't get labels. Error: %v\n", getLabelErr.Error())
	}
	return labels
}

func processOutput(metrics *map[string][]*metric) {
	// Create temp file
	tempFilePath := fmt.Sprintf("%s_%s.tmp", outputPath, randomString(6))
	outputFile, err := os.Create(tempFilePath)
	if err != nil {
		fmt.Printf("Create file error: %v", err.Error())
		os.Exit(1)
	}
	fileOpened := true
	defer func() {
		if fileOpened {
			fmt.Printf("Output file not normally closed.\n")
			outputFile.Close()
		}
	}()

	// use buffer io for write file
	writer := bufio.NewWriter(outputFile)
	var counter uint64
	for k, v := range *metrics {
		fmt.Fprintf(writer, "# TYPE %v gauge\n", k)

		for _, met := range v {
			fmt.Fprintf(writer, "%v %f %d\n", met.name, met.value, met.time)
			counter++
		}
	}

	// close file before move
	writer.Flush()
	outputFile.Close()
	fileOpened = false

	// Copy file instead direct write to output file. avoid read garbage data when output file still writing
	os.Rename(tempFilePath, outputPath)

	fmt.Printf("Number of metrics: %v\n", counter)
	fmt.Printf("\t")
	PrintMemUsage()

}

func generateTimeRange() SelectRange {
	startTime := time.Now().Add(-collectInterval).Truncate(collectInterval)
	timeRange := SelectRange{
		// Set time range for last 5 minutes. If not -1 second. It will take 6 samples.
		Start: startTime,
		End:   startTime.Add(collectInterval - (1 * time.Second)),
		Step:  time.Minute,
	}
	return timeRange
}

func startNewProcess(api v1.API) {
	// Set flag for running
	running = true
	defer func() {
		running = false
	}()
	lastExecuteTime = time.Now()

	// TODO: Change to logger
	fmt.Printf("Start process at %v\n", lastExecuteTime)
	PrintMemUsage()

	// Get all metric name from prometheus
	labels := getMetricLabels(api)
	// TODO: Change to logger
	fmt.Printf("Got %v labels.\n", labels.Len())

	// gerate time range for range query
	timeRange := generateTimeRange()

	// TODO: Change to logger
	fmt.Printf("Start collect data for time: %v\n", timeRange)

	downloaded := make(chan []*metric)
	concurrency := make(chan int, concurrency)
	metrics := make(map[string][]*metric)

	// Download those metric and make downsample
	for _, v := range labels {
		go getMetric(api, string(v), timeRange, downloaded, concurrency)
	}

	// Extract the metric name and save it to a map for processOutput() write metric type on output file
	for i := 0; i < len(labels); i++ {
		receivedMetrics := <-downloaded
		if len(receivedMetrics) > 0 {
			metricName := strings.Split(receivedMetrics[0].name, "{")[0]
			metrics[metricName] = receivedMetrics
		}
	}
	close(downloaded)
	close(concurrency)
	// TODO: Change to logger
	fmt.Printf("Executed %v\n", time.Since(lastExecuteTime))
	fmt.Printf("\t")
	PrintMemUsage()

	processOutput(&metrics)
}

func argsParserSetup() *cli.App {
	app := cli.NewApp()
	// Detail can refer: https://github.com/urfave/cli
	app.Name = "prometheus-downsampler"
	app.Usage = "Read metrics from Prometheus and downsample it to file"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:        "source,s",
			EnvVar:      "PDS_SOURCE",
			Usage:       "Source Prometheus endpoint.",
			Value:       "http://127.0.0.1:9090",
			Destination: &sourcePrometheusUrl,
		},
		cli.StringFlag{
			Name:        "output,o",
			EnvVar:      "PDS_OUTPUT",
			Usage:       "Output file path.",
			Value:       "/tmp/prometheus_downsample_output.txt",
			Destination: &outputPath,
		},
		cli.IntFlag{
			Name:        "concurrency,c",
			EnvVar:      "PDS_CONCURRENT",
			Usage:       "Max concurrent connection to source Prometheus.",
			Value:       50,
			Destination: &concurrency,
		},
		cli.DurationFlag{
			Name:        "interval,i",
			EnvVar:      "PDS_INTERVAL",
			Usage:       "Interval in minute for collect data from source Prometheus.",
			Value:       5 * time.Minute,
			Destination: &collectInterval,
		},
	}
	app.HideVersion = true
	app.HideHelp = true

	return app
}

func argsHandler(c *cli.Context) error {
	needHelp := c.Bool("help")
	if needHelp {
		cli.ShowAppHelpAndExit(c, 1)
	}
	return nil
}

func main() {
	app := argsParserSetup()
	app.Action = argsHandler
	err := app.Run(os.Args)
	if err != nil {

		log.Fatalf("Parse args error: ", err.Error())
	}

	client, clientErr := prometheusApi.NewClient(prometheusApi.Config{Address: sourcePrometheusUrl})
	if clientErr != nil {
		// TODO: Change to logger
		log.Panicf("Can't create prometheus client. Error: %v\n", clientErr.Error())

	}
	api := v1.NewAPI(client)

	go startNewProcess(api)
	for {
		// Start process every hour.
		<-time.After(collectInterval)
		if running == true {
			fmt.Printf("Job still running. Will skip this time. Last execution time: %v\n", lastExecuteTime)

		} else {
			go startNewProcess(api)
		}
	}
}

// Below function copy from Internet
func PrintMemUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	// For info on each, see: https://golang.org/pkg/runtime/#MemStats
	fmt.Printf("Alloc = %v MiB", bToMb(m.Alloc))
	fmt.Printf("\tTotalAlloc = %v MiB", bToMb(m.TotalAlloc))
	fmt.Printf("\tSys = %v MiB", bToMb(m.Sys))
	fmt.Printf("\tNumGC = %v\n", m.NumGC)
}

func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}

func randomString(n int) string {
	var letter = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

	b := make([]rune, n)
	for i := range b {
		b[i] = letter[rand.Intn(len(letter))]
	}
	return string(b)
}
