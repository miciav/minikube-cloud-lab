// Command vulncheck gates govulncheck JSON with narrowly reviewed database corrections.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// These unreviewed, module-wide records incorrectly flag client-side code.
// Versions are exact so any dependency change requires reviewing applicability again.
var reviewed = map[string]string{
	// Upstream identifies vulnerable minikube 0.3.0–0.29.0, fixed in 0.30.0.
	// https://github.com/kubernetes/minikube/issues/3208
	"GO-2023-1961 k8s.io/minikube@v1.39.0": "dashboard issue fixed in v0.30.0; no dashboard is enabled",
	// Both advisories affect Docker Engine plugin handling, not the SDK client used here.
	// https://github.com/moby/moby/security/advisories/GHSA-x744-4wpc-v9h2
	"GO-2026-4887 github.com/docker/docker@v28.5.2+incompatible": "daemon AuthZ plugin handling; this program only uses the Docker client",
	// https://github.com/moby/moby/security/advisories/GHSA-pxq6-2prw-chj9
	"GO-2026-4883 github.com/docker/docker@v28.5.2+incompatible": "daemon plugin installation validation; this program does not install plugins or run a daemon",
}

type record struct {
	Config *struct {
		Protocol string `json:"protocol_version"`
		Scanner  string `json:"scanner_name"`
		Level    string `json:"scan_level"`
		Mode     string `json:"scan_mode"`
	} `json:"config"`
	Finding *struct {
		OSV   string                                       `json:"osv"`
		Trace []struct{ Module, Version, Function string } `json:"trace"`
	} `json:"finding"`
}

func check(input io.Reader, output io.Writer) error {
	decoder := json.NewDecoder(input)
	configured := false
	seen := map[string]bool{}
	var blocked []string
	allowed := 0
	for {
		var event record
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid vulnerability report: %w", err)
		}
		if c := event.Config; c != nil {
			if c.Protocol != "v1.0.0" || c.Scanner != "govulncheck" || c.Level != "symbol" || c.Mode != "source" {
				return fmt.Errorf("require govulncheck source scan at symbol level")
			}
			configured = true
		}
		f := event.Finding
		if f == nil {
			continue
		}
		if f.OSV == "" || len(f.Trace) == 0 {
			return fmt.Errorf("incomplete vulnerability finding")
		}
		frame := f.Trace[0]
		if frame.Function == "" {
			continue
		}
		key := f.OSV + " " + frame.Module + "@" + frame.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		reason, ok := reviewed[key]
		if !ok {
			blocked = append(blocked, key)
			continue
		}
		if _, err := fmt.Fprintf(output, "Reviewed, not applicable: %s — %s\n", key, reason); err != nil {
			return err
		}
		allowed++
	}
	if !configured {
		return fmt.Errorf("missing govulncheck scan configuration")
	}
	if len(blocked) > 0 {
		sort.Strings(blocked)
		return fmt.Errorf("untriaged reachable vulnerabilities:\n%s", strings.Join(blocked, "\n"))
	}
	_, err := fmt.Fprintf(output, "No untriaged reachable vulnerabilities (%d reviewed findings).\n", allowed)
	return err
}

func main() {
	if err := check(os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
