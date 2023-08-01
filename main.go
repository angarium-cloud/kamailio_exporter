// MIT License

// Copyright (c) 2021 Thomas Weber, pascom GmbH & Co. Kg

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/angarium-cloud/kamailio_exporter/collector"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	log "github.com/sirupsen/logrus"
	"gopkg.in/urfave/cli.v1"
)

var Version string

func main() {
	app := cli.NewApp()
	app.Name = "Kamailio exporter"
	app.Usage = "Expose Kamailio statistics as http endpoint for prometheus."
	app.Version = Version
	// define cli flags
	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:   "debug",
			Usage:  "Enable debug logging",
			EnvVar: "DEBUG",
		},
		cli.StringFlag{
			Name:   "socketPath",
			Value:  "/var/run/kamailio/kamailio_ctl",
			Usage:  "Path to Kamailio unix domain socket",
			EnvVar: "SOCKET_PATH",
		},
		cli.StringFlag{
			Name:   "host",
			Usage:  "Kamailio ip or hostname. Domain socket is used if no host is defined.",
			EnvVar: "HOST",
		},
		cli.IntFlag{
			Name:   "port",
			Value:  3012,
			Usage:  "Kamailio port",
			EnvVar: "PORT",
		},
		cli.StringFlag{
			Name:   "bindIp",
			Value:  "0.0.0.0",
			Usage:  "Listen on this ip for scrape requests",
			EnvVar: "BIND_IP",
		},
		cli.IntFlag{
			Name:   "bindPort",
			Value:  9494,
			Usage:  "Listen on this port for scrape requests",
			EnvVar: "BIND_PORT",
		},
		cli.StringFlag{
			Name:   "metricsPath",
			Value:  "/metrics",
			Usage:  "The http scrape path",
			EnvVar: "METRICS_PATH",
		},
		cli.StringFlag{
			Name:   "rtpmetricsPath",
			Value:  "",
			Usage:  "The http scrape path for rtpengine metrics",
			EnvVar: "RTPMETRICS_PATH",
		},
		cli.StringFlag{
			Name:   "customKamailioMetricsURL",
			Value:  "",
			Usage:  "URL to request user defined metrics from kamailio",
			EnvVar: "CUSTOM_KAMAILIO_METRICS_URL",
		},
	}
	app.Action = appAction
	// then start the application
	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

// start the application
func appAction(c *cli.Context) error {
	log.Info("Starting kamailio exporter")

	if c.Bool("debug") {
		log.SetLevel(log.DebugLevel)
		log.Debug("Debug logging is enabled")
	}

	// create a collector
	collector, err := collector.New(c)
	if err != nil {
		return err
	}
	// and register it in prometheus API
	prometheus.MustRegister(collector)

	metricsPath := c.String("metricsPath")
	listenAddress := fmt.Sprintf("%s:%d", c.String("bindIp"), c.Int("bindPort"))
	// wire "/" to return some helpful info
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Kamailio Exporter</title></head>
             <body>
			 <p>This is a prometheus metric exporter for Kamailio.</p>
			 <p>Browse <a href='` + metricsPath + `'>` + metricsPath + `</a>
			 to get the metrics.</p>
             </body>
             </html>`))
	})
	rtpmetricsPath := c.String("rtpmetricsPath")
	if rtpmetricsPath != "" {
		log.Info("Enabling rtp metrics @", rtpmetricsPath)
		http.HandleFunc(rtpmetricsPath, func(w http.ResponseWriter, r *http.Request) {
			resp, err := http.Get("http://127.0.0.1:9901/metrics")
			if err != nil {
				log.Error(err)
				http.Error(w,
					fmt.Sprintf("Failed to connect to rtpengine: %s", err.Error()),
					http.StatusServiceUnavailable)
				return
			}
			defer resp.Body.Close()
			resp2, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Error(err)
				http.Error(w,
					fmt.Sprintf("Failed to read response from rtpengine: %s", err.Error()),
					http.StatusInternalServerError)
				return
			}
			w.Write(resp2)
		})
	}

	if customMetricsURL := c.String("customKamailioMetricsURL"); customMetricsURL != "" {
		http.Handle(metricsPath, handlerWithUserDefinedMetrics(customMetricsURL))
	} else {
		http.Handle(metricsPath, promhttp.Handler())
	}

	// start http server
	log.Info("Listening on ", listenAddress, metricsPath)
	return http.ListenAndServe(listenAddress, nil)
}

// Request user defined metrics and parse them into proper data objects
func gatherUserDefinedMetrics(url string) ([]*dto.MetricFamily, error) {
	resp, err := http.Get(url)
	if err != nil {
		log.Error("Failed to query kamailio user defined metrics", err)
		return nil, err
	} else if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		log.Errorf("Requesting user defined kamailio metrics returned status code: %v", resp.StatusCode)
		return nil, err
	}

	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("Failed to read kamailio user defined metrics", err)
		return nil, err
	}

	parser := expfmt.TextParser{}
	parsed, err := parser.TextToMetricFamilies(bytes.NewReader(respBytes))
	if err != nil {
		return nil, err
	}

	result := []*dto.MetricFamily{}
	for _, mf := range parsed {
		result = append(result, mf)
	}

	return result, nil
}

func handlerWithUserDefinedMetrics(userDefinedMetricsURL string) http.Handler {
	gatherer := func() ([]*dto.MetricFamily, error) {
		ours, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			return ours, err
		}
		theirs, err := gatherUserDefinedMetrics(userDefinedMetricsURL)
		if err != nil {
			log.Error("Scraping user defined metrics failed", err)
			return ours, nil
		}
		return append(ours, theirs...), nil
	}

	// defaults like promhttp.Handler(), except using our own gatherer
	return promhttp.InstrumentMetricHandler(
		prometheus.DefaultRegisterer,
		promhttp.HandlerFor(prometheus.GathererFunc(gatherer), promhttp.HandlerOpts{}))
}
