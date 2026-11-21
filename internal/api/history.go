package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/observation"
)

type metricSpec struct{ series, source, unit string }

var historyMetrics = map[string]metricSpec{
	"cpu": {"cluster_cpu", "kubernetes", "cores"}, "memory": {"cluster_memory", "kubernetes", "MiB"},
	"visible": {"queue_visible", "queue", "messages"}, "inflight": {"queue_inflight", "queue", "messages"}, "delayed": {"queue_delayed", "queue", "messages"},
	"objects": {"bucket_objects", "object-storage", "objects"}, "bytes": {"bucket_bytes", "object-storage", "bytes"},
}

const prometheusURL = "http://127.0.0.1:9092"

var metricsHTTP = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func monitoringReady(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, prometheusURL+"/-/ready", nil)
	res, e := metricsHTTP.Do(req)
	if e == nil {
		defer func() { _ = res.Body.Close() }()
	}
	if e != nil || res.StatusCode != http.StatusOK {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		message := "Monitoring is unavailable. Check lab status monitoring."
		if installed, err := observation.Installed(ctx); err == nil && !installed {
			message = "Monitoring is disabled. Run lab install monitoring."
		}
		respond(w, 503, map[string]string{"error": message}, nil)
		return
	}
	respond(w, 200, map[string]bool{"ready": true}, nil)
}

func registerMonitoring(mux *http.ServeMux, svc *cloud.Service) {
	mux.HandleFunc("GET /v1/monitoring/history", history)
	mux.HandleFunc("GET /v1/monitoring/ready", monitoringReady)
	mux.HandleFunc("GET /internal/metrics/{source}", func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !net.ParseIP(host).IsLoopback() {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		source := r.PathValue("source")
		var samples []observation.Sample
		switch source {
		case "kubernetes":
			running := 0
			for _, c := range svc.Clusters() {
				if c.Status != "RUNNING" {
					continue
				}
				running++
				snap, e := svc.Monitoring(ctx, c.Name)
				if e == nil && snap.MetricsStatus == "available" && snap.Usage != nil {
					samples = append(samples, observation.Sample{Metric: "cluster_cpu", Resource: c.Name, Value: float64(snap.Usage.CPUMillicores) / 1000}, observation.Sample{Metric: "cluster_memory", Resource: c.Name, Value: float64(snap.Usage.MemoryBytes) / (1 << 20)})
				}
			}
			if running > 0 && len(samples) == 0 {
				http.Error(w, "cluster usage unavailable", http.StatusServiceUnavailable)
				return
			}
		case "queue", "object-storage":
			var e error
			samples, e = observation.Read(ctx, source)
			if e != nil {
				http.Error(w, "source unavailable", http.StatusServiceUnavailable)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		b := bufio.NewWriter(w)
		for _, s := range samples {
			_, _ = fmt.Fprintf(b, "course_%s{resource=%q} %g\n", s.Metric, s.Resource, s.Value)
		}
		_ = b.Flush()
	})
}

type historySeries struct {
	Resource string       `json:"resource"`
	Points   [][2]float64 `json:"points"`
}
type historyResponse struct {
	Unit           string          `json:"unit"`
	Start          int64           `json:"start"`
	End            int64           `json:"end"`
	Source         string          `json:"sourceStatus"`
	SourceObserved float64         `json:"sourceObserved"`
	Series         []historySeries `json:"series"`
}

// A range vector returns original scrape timestamps, including holes. A stepped
// query would repeat the last value during the Prometheus lookback interval.
func history(w http.ResponseWriter, r *http.Request) {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		respond(w, 400, nil, cloud.ErrInvalid)
		return
	}
	for k, v := range q {
		if (k != "metric" && k != "window" && k != "resource") || len(v) != 1 {
			respond(w, 400, nil, cloud.ErrInvalid)
			return
		}
	}
	spec, ok := historyMetrics[q.Get("metric")]
	window := q.Get("window")
	if window == "" {
		window = "15m"
	}
	if !ok || (window != "15m" && window != "1h" && window != "24h") || len(q.Get("resource")) > 256 {
		respond(w, 400, nil, cloud.ErrInvalid)
		return
	}
	end := time.Now().UTC()
	duration, _ := time.ParseDuration(window)
	result := historyResponse{Unit: spec.unit, Start: end.Add(-duration).Unix(), End: end.Unix(), Source: "unknown", Series: []historySeries{}}
	selector := fmt.Sprintf(`{__name__=~"course_%s|up",job="course",source="%s"}[%s]`, spec.series, spec.source, window)
	values := url.Values{"query": {selector}, "time": {strconv.FormatInt(end.Unix(), 10)}, "limit": {"130"}}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, prometheusURL+"/api/v1/query?"+values.Encode(), nil)
	res, e := metricsHTTP.Do(req)
	if e != nil {
		respond(w, 503, map[string]string{"error": "Monitoring is unavailable. Check lab status monitoring."}, nil)
		return
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK || readHistory(res.Body, &result, q.Get("resource")) != nil {
		respond(w, 503, nil, cloud.ErrUnavailable)
		return
	}
	respond(w, 200, result, nil)
}

func readHistory(body io.Reader, out *historyResponse, resource string) error {
	var reply struct {
		Status string
		Data   struct {
			ResultType string
			Result     []struct {
				Metric map[string]string
				Values [][]json.RawMessage
			}
		}
	}
	const maxBytes = 16 << 20
	raw, e := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if e != nil {
		return e
	}
	if len(raw) > maxBytes {
		return errors.New("response limit exceeded")
	}
	if e = json.Unmarshal(raw, &reply); e != nil {
		return e
	}
	if reply.Status != "success" || reply.Data.ResultType != "matrix" || len(reply.Data.Result) >= 130 {
		return errors.New("invalid or oversized metrics response")
	}
	for _, s := range reply.Data.Result {
		name := s.Metric["resource"]
		health := s.Metric["__name__"] == "up"
		if !health && (name == "" || (resource != "" && resource != name)) {
			continue
		}
		series := historySeries{Resource: name}
		for _, p := range s.Values {
			ts, n, err := decodePoint(p)
			if err != nil {
				return err
			}
			if health {
				out.observeSource(ts, n)
			} else {
				series.Points = append(series.Points, [2]float64{ts, n})
			}
		}
		if !health && len(series.Points) > 0 {
			out.Series = append(out.Series, series)
		}
	}
	return nil
}

func decodePoint(p []json.RawMessage) (float64, float64, error) {
	var ts float64
	var value string
	if len(p) != 2 || json.Unmarshal(p[0], &ts) != nil || json.Unmarshal(p[1], &value) != nil {
		return 0, 0, errors.New("invalid sample")
	}
	n, e := strconv.ParseFloat(value, 64)
	if e != nil || !(n >= 0 && n <= 1e30) {
		return 0, 0, errors.New("invalid measurement")
	}
	return ts, n, nil
}

func (out *historyResponse) observeSource(ts, n float64) {
	out.SourceObserved = ts
	if ts < float64(out.End-45) {
		return
	}
	out.Source = "unavailable"
	if n == 1 {
		out.Source = "available"
	}
}
