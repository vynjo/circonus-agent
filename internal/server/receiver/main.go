// Copyright © 2017 Circonus, Inc. <support@circonus.com>
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//

package receiver

import (
	"encoding/json"
	"fmt"
	"io"
	stdlog "log"
	"regexp"
	"strconv"
	"strings"

	"github.com/circonus-labs/circonus-agent/internal/config"
	cgm "github.com/circonus-labs/circonus-gometrics"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

func init() {
	histogramRx = regexp.MustCompile(`^H\[(?P<bucket>[^\]]+)\]=(?P<count>[0-9]+)$`)
	histogramRxNames = histogramRx.SubexpNames()
}

func initCGM() error {
	metricsmu.Lock()
	defer metricsmu.Unlock()

	if metrics != nil {
		return nil
	}

	cmc := &cgm.Config{
		Debug: viper.GetBool(config.KeyDebugCGM),
		Log:   stdlog.New(log.With().Str("pkg", "receiver").Logger(), "", 0),
	}
	// put cgm into manual mode (no interval, no api key, invalid submission url)
	cmc.Interval = "0"                            // disable automatic flush
	cmc.CheckManager.Check.SubmissionURL = "none" // disable check management (create/update)

	hm, err := cgm.NewCirconusMetrics(cmc)
	if err != nil {
		return errors.Wrap(err, "receiver cgm")
	}

	metrics = hm
	return nil
}

// Flush returns current metrics
func Flush() *cgm.Metrics {
	initCGM()
	metricsmu.Lock()
	defer metricsmu.Unlock()

	return metrics.FlushMetrics()
}

// Parse handles incoming PUT/POST requests
func Parse(id string, data io.ReadCloser) error {
	initCGM()
	metricsmu.Lock()
	defer metricsmu.Unlock()

	var tmp cgm.Metrics
	if err := json.NewDecoder(data).Decode(&tmp); err != nil {
		if serr, ok := err.(*json.SyntaxError); ok {
			return errors.Wrapf(serr, "id:%s - offset %d", id, serr.Offset)
		}
		return errors.Wrapf(err, "parsing json for %s", id)
	}

	for name, metric := range tmp {
		metricName := strings.Join([]string{id, name}, config.MetricNameSeparator)
		switch metric.Type {
		case "i":
			if v := parseInt32(metricName, metric); v != nil {
				metrics.AddGauge(metricName, *v)
			}
		case "I":
			if v := parseUint32(metricName, metric); v != nil {
				metrics.AddGauge(metricName, *v)
			}
		case "l":
			if v := parseInt64(metricName, metric); v != nil {
				metrics.AddGauge(metricName, *v)
			}
		case "L":
			if v := parseUint64(metricName, metric); v != nil {
				metrics.AddGauge(metricName, *v)
			}
		case "n":
			v, isHist := parseFloat(metricName, metric)
			if v != nil {
				metrics.AddGauge(metricName, *v)
			} else if isHist {
				samples := parseHistogram(metricName, metric)
				if samples != nil && len(*samples) > 0 {
					for _, sample := range *samples {
						if sample.bucket {
							metrics.RecordCountForValue(metricName, sample.value, sample.count)
						} else {
							metrics.RecordValue(metricName, sample.value)
						}
					}
				}
			}
		case "s":
			metrics.SetText(metricName, fmt.Sprintf("%v", metric.Value))
		default:
			log.Warn().Str("metric", metricName).Str("type", metric.Type).Str("pkg", "receiver").Msg("unsupported metric type")
		}
	}

	return nil
}

func parseInt32(metricName string, metric cgm.Metric) *int32 {
	switch t := metric.Value.(type) {
	case float64:
		v := int32(metric.Value.(float64))
		return &v
	case string:
		v, err := strconv.ParseInt(metric.Value.(string), 10, 32)
		if err == nil {
			v2 := int32(v)
			return &v2
		}
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Err(err).
			Msg("parsing")
	default:
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Str("type", fmt.Sprintf("%T", t)).
			Msg("invalid value type for metric type")
	}
	return nil
}

func parseUint32(metricName string, metric cgm.Metric) *uint32 {
	switch t := metric.Value.(type) {
	case float64:
		v := uint32(metric.Value.(float64))
		return &v
	case string:
		v, err := strconv.ParseUint(metric.Value.(string), 10, 32)
		if err == nil {
			v2 := uint32(v)
			return &v2
		}
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Err(err).
			Msg("parsing")
	default:
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Str("type", fmt.Sprintf("%T", t)).
			Msg("invalid value type for metric type")
	}
	return nil
}

func parseInt64(metricName string, metric cgm.Metric) *int64 {
	switch t := metric.Value.(type) {
	case float64:
		v := int64(metric.Value.(float64))
		return &v
	case string:
		v, err := strconv.ParseInt(metric.Value.(string), 10, 64)
		if err == nil {
			v2 := int64(v)
			return &v2
		}
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Err(err).
			Msg("parsing")
	default:
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Str("type", fmt.Sprintf("%T", t)).
			Msg("invalid value type for metric type")
	}
	return nil
}

func parseUint64(metricName string, metric cgm.Metric) *uint64 {
	switch t := metric.Value.(type) {
	case float64:
		v := uint64(metric.Value.(float64))
		return &v
	case string:
		v, err := strconv.ParseUint(metric.Value.(string), 10, 64)
		if err == nil {
			v2 := uint64(v)
			return &v2
		}
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Err(err).
			Msg("parsing")
	default:
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Str("type", fmt.Sprintf("%T", t)).
			Msg("invalid value type for metric type")
	}
	return nil
}

func parseFloat(metricName string, metric cgm.Metric) (*float64, bool) {
	switch t := metric.Value.(type) {
	case float64:
		v := metric.Value.(float64)
		return &v, false
	case []interface{}: // treat as histogram
		return nil, true
	case string:
		v, err := strconv.ParseFloat(metric.Value.(string), 64)
		if err == nil {
			v2 := float64(v)
			return &v2, false
		}
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Err(err).
			Msg("parsing")
	default:
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Str("type", fmt.Sprintf("%T", t)).
			Msg("invalid value type for metric type")
	}
	return nil, false
}

type histSample struct {
	bucket bool
	count  int64
	value  float64
}

func parseHistogram(metricName string, metric cgm.Metric) *[]histSample {
	switch t := metric.Value.(type) {
	case []interface{}:
		ret := make([]histSample, 0, len(metric.Value.([]interface{})))
		for idx, v := range metric.Value.([]interface{}) {
			switch t2 := v.(type) {
			case float64:
				ret = append(ret, histSample{bucket: false, value: v.(float64)})
			case string:
				sv := v.(string)
				if !strings.Contains(sv, "H[") {
					v2, err := strconv.ParseFloat(sv, 64)
					if err != nil {
						log.Error().
							Str("pkg", "receiver").
							Str("metric", metricName).
							Interface("value", v).
							Int("position", idx).
							Err(err).
							Msg("parsing histogram sample")
						continue
					}
					ret = append(ret, histSample{bucket: false, value: float64(v2)})
					continue
				}

				//
				// it's an encoded histogram sample H[value]=count
				//
				bucket := ""
				count := ""
				matches := histogramRx.FindAllStringSubmatch(sv, -1)
				for _, match := range matches {
					for idx, val := range match {
						switch histogramRxNames[idx] {
						case "bucket":
							bucket = val
						case "count":
							count = val
						}
					}
				}
				if bucket == "" || count == "" {
					log.Error().
						Str("pkg", "receiver").
						Str("metric", metricName).
						Str("sample", sv).
						Int("position", idx).
						Msg("invalid encoded histogram sample")
					continue
				}
				b, err := strconv.ParseFloat(bucket, 64)
				if err != nil {
					log.Error().
						Str("pkg", "receiver").
						Str("metric", metricName).
						Str("sample", sv).
						Int("position", idx).
						Err(err).
						Msg("encoded histogram sample, value parse")
					continue
				}
				c, err := strconv.ParseInt(count, 10, 64)
				if err != nil {
					log.Error().
						Str("pkg", "receiver").
						Str("metric", metricName).
						Str("sample", sv).
						Int("position", idx).
						Err(err).
						Msg("encoded histogram sample, count parse")
					continue
				}
				ret = append(ret, histSample{bucket: true, value: b, count: c})
			default:
				log.Error().
					Str("pkg", "receiver").
					Str("metric", metricName).
					Interface("value", v).
					Int("position", idx).
					Str("type", fmt.Sprintf("%T", t2)).
					Msg("invalid value type for histogram sample")
			}
		}
		if len(ret) == 0 {
			return nil
		}
		return &ret
	default:
		log.Error().
			Str("pkg", "receiver").
			Str("metric", metricName).
			Interface("value", metric).
			Str("type", fmt.Sprintf("%T", t)).
			Msg("invalid value type for histogram")
	}
	return nil
}
