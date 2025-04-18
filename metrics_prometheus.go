// Copyright 2019 Aporeto Inc.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//     http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bahamut

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	opentracing "github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	pregexp = regexp.MustCompile(`^/_[a-zA-Z0-9-_+]+`)
	vregexp = regexp.MustCompile(`^/v/\d+`)
)

func sanitizePath(url string) string {

	prefix := "/"
	matches := pregexp.FindAllString(url, 1)
	if len(matches) == 1 {
		prefix = matches[0] + "/"
		url = strings.TrimPrefix(url, matches[0])
	}
	url = vregexp.ReplaceAllString(url, "")
	url = strings.TrimPrefix(url, "/")

	parts := strings.Split(url, "/")

	if len(parts) <= 1 {
		return prefix + url
	}

	parts[1] = ":id"

	return prefix + strings.Join(parts, "/")
}

type prometheusMetricsManager struct {
	reqDurationMetric    *prometheus.HistogramVec
	reqTotalMetric       *prometheus.CounterVec
	errorMetric          *prometheus.CounterVec
	tcpConnTotalMetric   prometheus.Counter
	tcpConnCurrentMetric prometheus.Gauge
	wsConnTotalMetric    prometheus.Counter
	wsConnCurrentMetric  prometheus.Gauge

	handler http.Handler
}

// NewPrometheusMetricsManager returns a new MetricManager using the prometheus format.
func NewPrometheusMetricsManager() MetricsManager {

	return newPrometheusMetricsManager(prometheus.DefaultRegisterer)
}

func newPrometheusMetricsManager(registerer prometheus.Registerer) MetricsManager {
	mc := &prometheusMetricsManager{
		handler: promhttp.Handler(),
		reqTotalMetric: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_requests_total",
				Help: "The total number of requests.",
			},
			[]string{"method", "url", "code"},
		),
		reqDurationMetric: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "http_requests_duration_seconds",
				Help:    "The average duration of the requests",
				Buckets: []float64{0.001, 0.0025, 0.005, 0.010, 0.025, 0.050, 0.100, 0.250, 0.500, 1.0, 2.5, 5.0, 10.0},
			},
			[]string{"method", "url"},
		),
		tcpConnTotalMetric: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "tcp_connections_total",
				Help: "The total number of TCP connection.",
			},
		),
		tcpConnCurrentMetric: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "tcp_connections_current",
				Help: "The current number of TCP connection.",
			},
		),
		wsConnTotalMetric: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "http_ws_connections_total",
				Help: "The total number of ws connection.",
			},
		),
		wsConnCurrentMetric: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "http_ws_connections_current",
				Help: "The current number of ws connection.",
			},
		),
		errorMetric: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_errors_5xx_total",
				Help: "The total number of 5xx errors.",
			},
			[]string{"trace", "method", "url", "code"},
		),
	}

	registerer.MustRegister(mc.tcpConnCurrentMetric)
	registerer.MustRegister(mc.tcpConnTotalMetric)
	registerer.MustRegister(mc.reqTotalMetric)
	registerer.MustRegister(mc.reqDurationMetric)
	registerer.MustRegister(mc.wsConnTotalMetric)
	registerer.MustRegister(mc.wsConnCurrentMetric)
	registerer.MustRegister(mc.errorMetric)

	return mc
}

func (c *prometheusMetricsManager) MeasureRequest(method string, path string) FinishMeasurementFunc {

	surl := sanitizePath(path)

	timer := prometheus.NewTimer(
		prometheus.ObserverFunc(
			func(v float64) {
				c.reqDurationMetric.With(
					prometheus.Labels{
						"method": method,
						"url":    surl,
					},
				).Observe(v)
			},
		),
	)

	return func(code int, span opentracing.Span) time.Duration {

		c.reqTotalMetric.With(prometheus.Labels{
			"method": method,
			"url":    surl,
			"code":   strconv.Itoa(code),
		}).Inc()

		if code >= http.StatusInternalServerError {

			c.errorMetric.With(prometheus.Labels{
				"trace":  extractSpanID(span),
				"method": method,
				"url":    surl,
				"code":   strconv.Itoa(code),
			}).Inc()
		}

		return timer.ObserveDuration()
	}
}

func (c *prometheusMetricsManager) RegisterWSConnection() {
	c.wsConnTotalMetric.Inc()
	c.wsConnCurrentMetric.Inc()
}

func (c *prometheusMetricsManager) UnregisterWSConnection() {
	c.wsConnCurrentMetric.Dec()
}

func (c *prometheusMetricsManager) RegisterTCPConnection() {
	c.tcpConnTotalMetric.Inc()
	c.tcpConnCurrentMetric.Inc()
}

func (c *prometheusMetricsManager) UnregisterTCPConnection() {
	c.tcpConnCurrentMetric.Dec()
}

func (c *prometheusMetricsManager) Write(w http.ResponseWriter, r *http.Request) {
	c.handler.ServeHTTP(w, r)
}
