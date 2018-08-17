# Prometheus Downsampler
This program use for collect Prometheus data for last n minutes (default is 5 minutes). 
Then take average on each metrics. Output to a text file.

### Solution
Use with another Prometheus for store the downsampled data. For we, we set the long term Prometheus retention to 2 years (Still testing).

This program only output a text file on K8S empty dir. Then use a nginx in same pod to expose the output to long-term Prometheus.
And need to set `honor_labels: true` inside long term Prometheus scrape job. Otherwise some conflicted labels will be renamed.

![Downsampler with 2 Prometheus](https://github.com/alantang888/prometheus-downsampler/blob/master/other_resource/Prometheus_Downsampler_Solution.png)


### Config
There 4 parameters can config. You can either use `args` or `environment variable`.
- Source Prometheus endpoint
    - Default: http://127.0.0.1:9090
    - Args: -s
    - Environame variable: PDS_SOURCE
- Output file path
    - Default: /tmp/prometheus_downsample_output.txt
    - Args: -o
    - Environame variable: PDS_OUTPUT
- Interval in minute for collect data from source Prometheus
    - Default: 5m
    - Args: -i
    - Environame variable: PDS_INTERVAL
- Max concurrent connection to source Prometheus
    - Default: 50
    - Args: -c
    - Environame variable: PDS_CONCURRENT
    
Example: Your prometheus endpoint is `http://192.168.1.20:9090` and want to downsample data for every `10 minutes`:
```bash
go run prometheus-downsampler.go -s http://192.168.1.20:9090 -i 10m
```
or
```bash
./prometheus-downsampler -s http://192.168.1.20:9090 -i 10m
```

### How it work
1. Call [Querying label values] API to get all metric names
1. Call [Range Queries] API to get every metrics with 1 minute step
1. Take average on each metrics
1. Write all metrics with [exposition format] to a temp file
1. Rename the temp file to output file name

### Issue
This program can handle collect a longer time range data. Then group them to every n minute and take average.
**But due to below reasons. Now only process for single time group.**
- [exposition format] mention `Each line must have a unique combination of a metric name and labels. Otherwise,
 the ingestion behavior is undefined.`. But didn't mention is it safe if have different timestamp
- Also tested for a while with export 1 hour data with 12 data points. The long term Prometheus lost some of data point.

### Why not use InfluxDB
Because we scrape over 650K metrics every 10 second (With 2 Prometheus servers for HA). 
We tried use remote_write to InfluxDB (Single server, not enterprise edition). 
But it cause very high CPU usage and OOM dead very soon. Also make the operation Prometheus dead.
So we try on different way.

[Querying label values]: https://prometheus.io/docs/prometheus/latest/querying/api/#querying-label-values
[Range Queries]: https://prometheus.io/docs/prometheus/latest/querying/api/#range-queries
[exposition format]: https://prometheus.io/docs/instrumenting/exposition_formats/
