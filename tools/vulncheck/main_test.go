package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

const configRecord = `{"config":{"protocol_version":"v1.0.0","scanner_name":"govulncheck","scan_level":"symbol","scan_mode":"source"}}`

func TestRejectsNewReachableFinding(t *testing.T) {
	report := configRecord + `{"finding":{"osv":"GO-NEW","trace":[{"module":"example.org/lib","version":"v1.0.0","function":"Vulnerable"}]}}`
	if err := check(strings.NewReader(report), io.Discard); err == nil || !strings.Contains(err.Error(), "GO-NEW") {
		t.Fatalf("new finding passed: %v", err)
	}
}

func TestReviewedFindingsAreVersionScoped(t *testing.T) {
	for _, tc := range []struct{ id, module, version string }{{"GO-2023-1961", "k8s.io/minikube", "v1.39.0"}, {"GO-2026-4887", "github.com/docker/docker", "v28.5.2+incompatible"}, {"GO-2026-4883", "github.com/docker/docker", "v28.5.2+incompatible"}} {
		report := configRecord + `{"finding":{"osv":"` + tc.id + `","trace":[{"module":"` + tc.module + `","version":"` + tc.version + `","function":"Affected"}]}}`
		var out bytes.Buffer
		if err := check(strings.NewReader(report), &out); err != nil || !strings.Contains(out.String(), tc.id) {
			t.Fatalf("reviewed finding: %s %v", out.String(), err)
		}
		changed := strings.ReplaceAll(report, tc.version, "v99.0.0")
		if err := check(strings.NewReader(changed), io.Discard); err == nil {
			t.Fatal("version change bypassed review")
		}
	}
}

func TestRejectsIncompleteOrWrongScan(t *testing.T) {
	for _, report := range []string{"", `{`, configRecord + `{"finding":{"osv":"GO-NEW","trace":[]}}`, strings.ReplaceAll(configRecord, "symbol", "module"), strings.ReplaceAll(configRecord, "source", "binary")} {
		if err := check(strings.NewReader(report), io.Discard); err == nil {
			t.Fatalf("invalid scan passed: %q", report)
		}
	}
}

func TestNoReachableFindings(t *testing.T) {
	report := configRecord + `{"progress":{"message":"done"}}{"finding":{"osv":"GO-UNREACHABLE","trace":[{"module":"example.org/lib","version":"v1.0.0"}]}}`
	if err := check(strings.NewReader(report), io.Discard); err != nil {
		t.Fatal(err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func TestReportsOutputErrors(t *testing.T) {
	if err := check(strings.NewReader(configRecord), failingWriter{}); err == nil {
		t.Fatal("output failure swallowed")
	}
}
