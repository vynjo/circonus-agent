// Copyright © 2017 Circonus, Inc. <support@circonus.com>
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/circonus-labs/circonus-agent/internal/config"
	"github.com/circonus-labs/circonus-agent/internal/server/promrecv"
	"github.com/circonus-labs/circonus-agent/internal/server/receiver"
	cgm "github.com/circonus-labs/circonus-gometrics"
	appstats "github.com/maier/go-appstats"
	"github.com/spf13/viper"
)

// run handles requests to execute plugins and return metrics emitted
// handles /, /run, or /run/plugin_name
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	id := ""

	if strings.HasPrefix(r.URL.Path, "/run/") { // run specific item
		id = strings.Replace(r.URL.Path, "/run/", "", -1)
		if id != "" {
			idOK := false

			// highest priority, internal servers (receiver, statsd, etc.)
			if !idOK {
				s.logger.Debug().Str("id", id).Msg("checking internals")
				idOK = s.plugins.IsInternal(id)
			}
			// check builtins before plugins, builtins offer better efficiency
			if !idOK {
				s.logger.Debug().Str("id", id).Msg("checking bulitins")
				idOK = s.builtins.IsBuiltin(id)
			}
			// lastly, check active plugins, if any
			if !idOK {
				s.logger.Debug().Str("id", id).Msg("checking plugins")
				idOK = s.plugins.IsValid(id)
			}

			if !idOK {
				s.logger.Warn().
					Str("id", id).
					Msg("unknown item requested")
				http.NotFound(w, r)
				return
			}
		}
	}

	lastMeticsmu.Lock()
	defer lastMeticsmu.Unlock()

	metrics := map[string]interface{}{}

	// default to true if id is blank, otherwise set all to false
	runBuiltins := id == ""
	runPlugins := id == ""
	flushProm := id == ""
	flushReceiver := id == ""
	flushStatsd := id == ""

	if id != "" {
		// identify _what_ to run based on the id
		switch {
		case id == "prom":
			flushProm = true
		case id == "write":
			flushReceiver = true
		case id == "statsd":
			flushStatsd = true
		case s.builtins.IsBuiltin(id):
			runBuiltins = true
		default:
			runPlugins = true
		}
	}

	if runBuiltins {
		s.builtins.Run(id)
		builtinMetrics := s.builtins.Flush(id)
		for metricName, metric := range *builtinMetrics {
			metrics[metricName] = metric
		}
	}

	if runPlugins {
		// NOTE: errors are ignored from plugins.Run
		//       1. errors are already logged by Run
		//       2. do not expose execution state to callers
		s.plugins.Run(id)
		pluginMetrics := s.plugins.Flush(id)
		for metricName, metric := range *pluginMetrics {
			metrics[metricName] = metric
		}
	}

	if flushReceiver {
		receiverMetrics := receiver.Flush()
		for metricName, metric := range *receiverMetrics {
			metrics[metricName] = metric
		}
	}

	if flushStatsd {
		if s.statsdSvr != nil {
			statsdMetrics := s.statsdSvr.Flush()
			if statsdMetrics != nil {
				metrics[viper.GetString(config.KeyStatsdHostCategory)] = statsdMetrics
			}
		}
	}

	if flushProm {
		promMetrics := promrecv.Flush()
		for metricName, metric := range *promMetrics {
			metrics[metricName] = metric
		}
	}

	lastMetrics.metrics = metrics
	lastMetrics.ts = time.Now()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(metrics); err != nil {
		s.logger.Error().
			Err(err).
			Msg("Writing metrics to response")
	}
}

// inventory returns the current, active plugin inventory
func (s *Server) inventory(w http.ResponseWriter, r *http.Request) {
	inventory := s.plugins.Inventory()
	if inventory == nil {
		inventory = []byte(`{"error": "empty inventory"}`)
		s.logger.Error().Msg("inventory is nil/empty...")
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(inventory)
}

// socketHandler gates /write for the socket server only
func (s *Server) socketHandler(w http.ResponseWriter, r *http.Request) {
	if !writePathRx.MatchString(r.URL.Path) {
		appstats.IncrementInt("requests_bad")
		s.logger.Warn().
			Str("method", r.Method).
			Str("url", r.URL.String()).
			Msg("Not found")
		http.NotFound(w, r)
		return
	}

	if r.Method != "PUT" && r.Method != "POST" {
		appstats.IncrementInt("requests_bad")
		s.logger.Warn().
			Str("method", r.Method).
			Str("url", r.URL.String()).
			Msg("Not found")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.write(w, r)
}

// write handles PUT/POST requests with a JSON playload containing "freeform"
// metrics. No validation is applied to the "format" of the metrics beyond k/v.
// Where 'key' is the metric name and 'value' is the metric value as either a
// simple value (e.g. {"name": 1, "foo": "bar", ...}) or a structured value
// representation (e.g. {"foo": {_type: "i", _value: 1}, ...}).
func (s *Server) write(w http.ResponseWriter, r *http.Request) {
	id := strings.Replace(r.URL.Path, "/write/", "", -1)

	s.logger.Debug().Str("path", r.URL.Path).Str("id", id).Msg("write request")
	// a write request *MUST* include a metric group id to act as a namespace.
	// in other words, a "plugin name", all metrics for that write will appear
	// _under_ the metric group id (aka plugin name)
	if id == "" {
		s.logger.Warn().Msg("write recevier - invalid id (empty)")
		http.NotFound(w, r)
		return
	}

	if err := receiver.Parse(id, r.Body); err != nil {
		s.logger.Warn().Err(err).Msg("write recevier")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// promReceiver handles PUT/POST requests with prometheus TEXT formatted metrics
// https://prometheus.io/docs/instrumenting/exposition_formats/
func (s *Server) promReceiver(w http.ResponseWriter, r *http.Request) {
	s.logger.Debug().Str("path", r.URL.Path).Msg("prom metrics recevied")

	if err := promrecv.Parse(r.Body); err != nil {
		s.logger.Warn().Err(err).Msg("prom recevier")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// promOutput returns the last metrics in prom format
func (s *Server) promOutput(w http.ResponseWriter, r *http.Request) {
	if lastMetrics.metrics == nil || len(lastMetrics.metrics) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ms := lastMetrics.ts.UnixNano() / int64(time.Millisecond)

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	for id, data := range lastMetrics.metrics {
		s.metricsToPromFormat(w, id, ms, data)
	}
}

func (s *Server) metricsToPromFormat(w io.Writer, prefix string, ts int64, val interface{}) {
	l := s.logger.With().Str("op", "prom export").Logger()
	switch t := val.(type) {
	case cgm.Metric:
		metric := val.(cgm.Metric)
		sv := fmt.Sprintf("%v", metric.Value)
		switch metric.Type {
		case "i":
			fallthrough
		case "I":
			fallthrough
		case "l":
			fallthrough
		case "L":
			v, err := strconv.ParseInt(sv, 10, 64)
			if err != nil {
				l.Error().Err(err).Msg("conv int64")
				return
			}
			if _, err := w.Write([]byte(fmt.Sprintf("%s %d %d\n", prefix, v, ts))); err != nil {
				l.Error().Err(err).Msg("writing prom output")
			}
		case "n":
			if strings.Contains(sv, "[H[") {
				l.Warn().
					Str("type", "histogram != [prom]histogram(percentile)").
					Str("metric", fmt.Sprintf("%s = %s", prefix, sv)).
					Msg("unsupported metric type")
			} else {
				v, err := strconv.ParseFloat(sv, 64)
				if err != nil {
					l.Error().Err(err).Msg("conv float64")
					return
				}
				if _, err := w.Write([]byte(fmt.Sprintf("%s %f %d\n", prefix, v, ts))); err != nil {
					l.Error().Err(err).Msg("writing prom output")
				}
			}
		case "s":
			l.Warn().
				Str("type", "text [prom]???").
				Str("metric", fmt.Sprintf("%s = %s", prefix, sv)).
				Msg("unsuported metric type")
		default:
			l.Warn().
				Str("type", metric.Type).
				Str("name", prefix).
				Interface("metric", metric).
				Msg("invalid metric type")
		}
	case cgm.Metrics:
		metrics := val.(cgm.Metrics)
		for pfx, metric := range metrics {
			name := prefix
			if pfx != "" {
				name = strings.Join([]string{name, pfx}, config.MetricNameSeparator)
			}
			s.metricsToPromFormat(w, name, ts, metric)
		}
	case *cgm.Metrics:
		metrics := val.(*cgm.Metrics)
		s.metricsToPromFormat(w, prefix, ts, *metrics)
	default:
		l.Warn().
			Str("metric", fmt.Sprintf("#TYPE(%T) %v = %#v", t, prefix, val)).
			Msg("unhandled export type")
	}
}
