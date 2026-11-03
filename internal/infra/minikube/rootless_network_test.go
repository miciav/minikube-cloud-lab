package minikube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRootlessLinkPreservesRuntimeMAC(t *testing.T) {
	script, err := rootlessLinkScript("76:d2:8b:99:02:4f")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"/etc/systemd/network/10-cloud-lab.link", "MACAddressPolicy=none", "MACAddress=76:d2:8b:99:02:4f", "ip link set dev eth0 address 76:d2:8b:99:02:4f"} {
		if !strings.Contains(script, required) {
			t.Fatalf("missing %q: %s", required, script)
		}
	}
	for _, mac := range []string{"", "not-a-mac", "76:d2:8b:99:02:4f;touch /tmp/injected", "00:11:22:33:44:55:66:77"} {
		if _, err := rootlessLinkScript(mac); err == nil {
			t.Fatalf("accepted unsafe MAC %q", mac)
		}
	}
}

func TestRestoreRootlessMACThroughEngineAPI(t *testing.T) {
	for _, code := range []int{0, 9} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			created, started := false, false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("API-Version", "1.51")
				case strings.HasSuffix(r.URL.Path, "/containers/node/json"):
					_, _ = w.Write([]byte(`{"NetworkSettings":{"Networks":{"lab":{"MacAddress":"76:d2:8b:99:02:4f"}}}}`))
				case strings.HasSuffix(r.URL.Path, "/containers/node/exec"):
					var body struct {
						User string
						Cmd  []string
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.User != "0" || len(body.Cmd) != 3 || !strings.Contains(body.Cmd[2], "MACAddressPolicy=none") {
						t.Errorf("unsafe exec: %+v", body)
					}
					created = true
					_, _ = w.Write([]byte(`{"Id":"network-exec"}`))
				case strings.HasSuffix(r.URL.Path, "/exec/network-exec/start"):
					started = true
				case strings.HasSuffix(r.URL.Path, "/exec/network-exec/json"):
					_, _ = fmt.Fprintf(w, `{"Running":false,"ExitCode":%d}`, code)
				default:
					t.Errorf("unexpected API call: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			t.Setenv("DOCKER_HOST", strings.Replace(server.URL, "http://", "tcp://", 1))
			t.Setenv("DOCKER_API_VERSION", "")
			t.Setenv("DOCKER_TLS_VERIFY", "")
			t.Setenv("DOCKER_CERT_PATH", "")
			err := restoreRootlessMAC(context.Background(), "node", "lab")
			if (err != nil) != (code != 0) {
				t.Fatalf("exit %d: %v", code, err)
			}
			if !created || !started {
				t.Fatal("network command was not executed")
			}
		})
	}
}
