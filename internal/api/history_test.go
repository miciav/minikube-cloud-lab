package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func TestHistoryValidatesQueryBeforeContactingPrometheus(t *testing.T) {
	for _, q := range []string{"metric=unknown", "metric=cpu&window=3d", "metric=cpu&query=up", "metric=cpu&resource=x&resource=y"} {
		w := httptest.NewRecorder()
		history(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/monitoring/history?"+q, nil))
		if w.Code != 400 {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
}

func TestHistoryPreservesActualSamples(t *testing.T) {
	out := historyResponse{End: 120, Source: "unknown"}
	e := readHistory(strings.NewReader(`{"status":"success","data":{"resultType":"matrix","result":[
 {"metric":{"__name__":"up"},"values":[[65,"0"],[115,"1"]]},
 {"metric":{"resource":"one"},"values":[[20,"0"],[110,"2"]]},
 {"metric":{"resource":"two"},"values":[[115,"3"]]}]}}`), &out, "one")
	if e != nil || out.Source != "available" || len(out.Series) != 1 || len(out.Series[0].Points) != 2 || out.Series[0].Points[0] != [2]float64{20, 0} {
		t.Fatal(out, e)
	}
	for _, v := range []string{"oops", `{"status":"error"}`, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"resource":"x"},"values":[[1,"NaN"]]}]}}`} {
		if readHistory(strings.NewReader(v), &out, "") == nil {
			t.Fatal(v)
		}
	}
}

type metricsTransportFunc func(*http.Request) (*http.Response, error)

func (f metricsTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConsoleRoutes(t *testing.T) {
	svc, e := cloud.NewService(fake.New(), t.TempDir(), slog.Default())
	if e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	registerMonitoring(mux, svc)
	for _, tc := range []struct {
		path, origin, body string
		upstream, want     int
	}{
		{"/internal/metrics/kubernetes", "127.0.0.1:42", "", 0, 200},
		{"/internal/metrics/kubernetes", "10.0.0.1:42", "", 0, 404},
		{"/internal/metrics/unknown", "127.0.0.1:42", "", 0, 404},
		{"/internal/metrics/queue", "127.0.0.1:42", "", 0, 503},
		{"/v1/monitoring/ready", "", "", 0, 503},
		{"/v1/monitoring/ready", "", "", 503, 503},
		{"/v1/monitoring/ready", "", "", 200, 200},
		{"/v1/monitoring/history?metric=objects", "", "", 0, 503},
		{"/v1/monitoring/history?metric=cpu&window=24h", "", "", 503, 503},
		{"/v1/monitoring/history?metric=bytes&window=1h", "", "bad", 200, 503},
		{"/v1/monitoring/history?metric=visible&resource=q", "", `{"status":"success","data":{"resultType":"matrix","result":[]}}`, 200, 200},
		{"/v1/monitoring/history?metric=%XX", "", "", 0, 400},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Setenv("DOCKER_HOST", "unix://"+t.TempDir()+"/missing.sock")
			old := metricsHTTP.Transport
			t.Cleanup(func() { metricsHTTP.Transport = old })
			metricsHTTP.Transport = metricsTransportFunc(func(_ *http.Request) (*http.Response, error) {
				if tc.upstream == 0 {
					return nil, errors.New("unavailable")
				}
				return &http.Response{StatusCode: tc.upstream, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}}, nil
			})
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
			r.RemoteAddr = tc.origin
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSourceStalenessAndHistoryLimits(t *testing.T) {
	out := historyResponse{End: 100, Source: "unknown"}
	out.observeSource(10, 1)
	if out.Source != "unknown" {
		t.Fatal(out)
	}
	out.observeSource(99, 0)
	if out.Source != "unavailable" || out.SourceObserved != 99 {
		t.Fatal(out)
	}
	if _, _, err := decodePoint(nil); err == nil {
		t.Fatal("missing timestamp")
	}
	if e := readHistory(io.LimitReader(zeroReader{}, (16<<20)+1), &out, ""); e == nil {
		t.Fatal("accepted oversized response")
	}
	if e := readHistory(iotest.ErrReader(errors.New("stream interrupted")), &out, ""); e == nil {
		t.Fatal("accepted failed response")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestRunningClusterWithoutMetricsIsUnavailable(t *testing.T) {
	svc, err := cloud.NewService(fake.New(), t.TempDir(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	op, err := svc.CreateCluster("demo", "lab-small", 1, 1, "student")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Wait(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerMonitoring(mux, svc)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/internal/metrics/kubernetes", nil)
	r.RemoteAddr = "127.0.0.1:42"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unobserved cluster: %d %s", w.Code, w.Body.String())
	}
}
