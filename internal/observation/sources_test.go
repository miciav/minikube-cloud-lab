package observation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
)

func TestBucketCounters(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		bad        bool
	}{
		{"empty", `{"objects":0,"bytes":0}`, false}, {"missing", `{"objects":1}`, true}, {"malformed", "no", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := func(args ...string) ([]byte, error) {
				if len(args) == 3 && args[2] == "ListBuckets" {
					return []byte(`[{"id":"id","globalAliases":["one"]}]`), nil
				}
				if len(args) != 4 || args[2] != "GetBucketInfo" || args[3] != `{"id":"id"}` {
					t.Fatal(args)
				}
				return []byte(tc.want), nil
			}
			s, e := buckets(cmd)
			if (e != nil) != tc.bad {
				t.Fatal(e)
			}
			if !tc.bad && (len(s) != 2 || s[0].Resource != "one" || s[1].Value != 0) {
				t.Fatal(s)
			}
		})
	}
}

func TestQueueCounters(t *testing.T) {
	const list = `<ListQueuesResponse><ListQueuesResult><QueueUrl>http://localhost:9324/000/q</QueueUrl></ListQueuesResult></ListQueuesResponse>`
	const attrs = `<GetQueueAttributesResponse><GetQueueAttributesResult><Attribute><Name>ApproximateNumberOfMessages</Name><Value>3</Value></Attribute><Attribute><Name>ApproximateNumberOfMessagesDelayed</Name><Value>0</Value></Attribute><Attribute><Name>ApproximateNumberOfMessagesNotVisible</Name><Value>1</Value></Attribute></GetQueueAttributesResult></GetQueueAttributesResponse>`
	for _, tc := range []struct {
		name, list, attrs string
		bad               bool
	}{
		{"valid", list, attrs, false},
		{"bad path", strings.Replace(list, "/000/q", "/../admin", 1), attrs, true},
		{"missing attribute", list, strings.Replace(attrs, "ApproximateNumberOfMessagesDelayed", "Other", 1), true},
		{"invalid number", list, strings.Replace(attrs, "<Value>0", "<Value>-1", 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := func(args ...string) ([]byte, error) {
				u := args[len(args)-1]
				switch {
				case strings.Contains(u, "/?Action=ListQueues"):
					return []byte(tc.list), nil
				case strings.HasPrefix(u, "http://127.0.0.1:9324/000/q?Action=GetQueueAttributes"):
					return []byte(tc.attrs), nil
				default:
					return nil, errors.New("unexpected request")
				}
			}
			s, e := queues(cmd)
			if (e != nil) != tc.bad {
				t.Fatal(e)
			}
			if !tc.bad && (len(s) != 3 || s[0].Resource != "000/q" || s[0].Value != 3) {
				t.Fatal(s)
			}
		})
	}
}

func TestReadSelectsOnlyTheCourseContainer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("Api-Version", "1.52")
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/containers/json") {
			t.Errorf("unexpected %s", r.URL.Path)
		}
		var filters map[string]map[string]bool
		if e := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters); e != nil {
			t.Error(e)
		}
		if !filters["label"]["com.docker.compose.project=fixture-queue"] {
			t.Error(filters)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()
	t.Setenv("CLOUD_COURSE_PROJECT", "fixture")
	t.Setenv("DOCKER_HOST", strings.Replace(server.URL, "http:", "tcp:", 1))
	t.Setenv("DOCKER_API_VERSION", "1.52")
	for _, source := range []string{"queue", "bad"} {
		if _, e := Read(context.Background(), source); e == nil {
			t.Fatal("expected unavailable")
		}
	}
}

func TestRuntimeExecReadsBucketCounters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = io.WriteString(w, `[{"Id":"fixture"}]`)
		case strings.HasSuffix(r.URL.Path, "/containers/fixture/exec"):
			var spec struct{ Cmd []string }
			if e := json.NewDecoder(r.Body).Decode(&spec); e != nil {
				t.Error(e)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": spec.Cmd[2]})
		case strings.HasSuffix(r.URL.Path, "/start"):
			_, _ = io.Copy(io.Discard, r.Body)
			conn, b, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = io.WriteString(b, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
			data := `[{"id":"123","globalAliases":[]}]`
			if strings.Contains(r.URL.Path, "/GetBucketInfo/") {
				data = `{"objects":2,"bytes":7}`
			}
			_, _ = io.WriteString(stdcopy.NewStdWriter(b, stdcopy.Stdout), data)
			_ = b.Flush()
		case strings.Contains(r.URL.Path, "/exec/") && strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"ID":"task","Running":false,"ExitCode":0}`)
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.52")
		default:
			t.Errorf("unexpected runtime endpoint %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", strings.Replace(srv.URL, "http:", "tcp:", 1))
	t.Setenv("DOCKER_API_VERSION", "1.52")
	s, e := Read(t.Context(), "object-storage")
	if e != nil || len(s) != 2 || s[1].Value != 7 || s[1].Resource != "123" {
		t.Fatal(s, e)
	}
}

func TestMonitoringInventoryAndSourceFailures(t *testing.T) {
	t.Setenv("DOCKER_API_VERSION", "1.52")
	t.Setenv("CLOUD_COURSE_PROJECT", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("filters"), "io.course.monitoring.scope=mcl") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"Id":"stopped-observer"}]`)
			return
		}
		http.Error(w, "runtime failure", http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", strings.Replace(srv.URL, "http:", "tcp:", 1))
	ok, e := Installed(t.Context())
	if !ok || e != nil {
		t.Fatal(ok, e)
	}
	if _, e = Read(t.Context(), "queue"); e == nil {
		t.Fatal("treated runtime failure as an empty service")
	}
	for _, read := range []func(command) ([]Sample, error){queues, buckets} {
		for _, input := range []string{"broken", "", "command failure"} {
			_, e := read(func(...string) ([]byte, error) {
				if input == "command failure" {
					return nil, errors.New(input)
				}
				return []byte(input), nil
			})
			if e == nil {
				t.Fatal("accepted invalid listing", input)
			}
		}
	}
}

func TestBoundedScrapes(t *testing.T) {
	tooMany, _ := json.Marshal(make([]map[string]string, 129))
	fixtures := []struct {
		read func(command) ([]Sample, error)
		list string
	}{
		{buckets, string(tooMany)}, {queues, `<ListQueuesResponse><ListQueuesResult><NextToken>more</NextToken></ListQueuesResult></ListQueuesResponse>`},
	}
	for _, f := range fixtures {
		calls := 0
		if _, err := f.read(func(...string) ([]byte, error) { calls++; return []byte(f.list), nil }); err == nil || calls != 1 {
			t.Fatal("partial inventory passed")
		}
	}
	for _, f := range []struct {
		read func(command) ([]Sample, error)
		list string
	}{
		{queues, `<ListQueuesResponse><ListQueuesResult><QueueUrl>http://localhost/000/q</QueueUrl></ListQueuesResult></ListQueuesResponse>`},
		{buckets, `[{"id":"one"}]`},
	} {
		for _, bad := range []bool{true, false} {
			calls := 0
			_, err := f.read(func(...string) ([]byte, error) {
				calls++
				if calls == 1 {
					return []byte(f.list), nil
				}
				if bad {
					return nil, errors.New("service stopped")
				}
				return []byte("{"), nil
			})
			if err == nil || calls != 2 {
				t.Fatal("failed probe passed")
			}
		}
	}
}
