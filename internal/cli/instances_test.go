package cli

import (
	"strings"
	"testing"
)

func TestImagesList(t *testing.T) {
	_, url := setup(t, nil)
	out, err := execute(t, url, "images", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "SSH_USER", "debian-12", "docker"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestInstanceCommands(t *testing.T) {
	s, url := setup(t, nil)
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"instances", "create", "vm1"}, "Created instance vm1 at 172.30.0.200\n"},
		{[]string{"instances", "stop", "vm1"}, "Stopped instance vm1\n"},
		{[]string{"instances", "start", "vm1"}, "Started instance vm1\n"},
	}
	for _, step := range steps {
		if out, err := execute(t, url, step.args...); err != nil || out != step.want {
			t.Fatalf("%v: %q %v", step.args, out, err)
		}
	}
	for _, args := range [][]string{{"instances", "list"}, {"instances", "describe", "vm1"}} {
		out, err := execute(t, url, args...)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"NAME", "IP", "vm1", "RUNNING", "debian-12", "lab-small", "172.30.0.200"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v: missing %q in:\n%s", args, want, out)
			}
		}
	}
	out, err := execute(t, url, "instances", "stop", "vm1", "--async")
	if err != nil || !strings.HasPrefix(out, "op-") {
		t.Fatalf("async stop: %q %v", out, err)
	}
	if op, err := s.Operation(strings.TrimSpace(out)); err != nil {
		t.Fatalf("async stop operation: %v", err)
	} else {
		wait(t, s, op)
	}
	if out, err := execute(t, url, "instances", "delete", "vm1"); err != nil || out != "Deleted instance vm1\n" {
		t.Fatalf("delete: %q %v", out, err)
	}
	out, err = execute(t, url, "instances", "create", "vm2", "--async")
	if err != nil || !strings.HasPrefix(out, "op-") {
		t.Fatalf("async: %q %v", out, err)
	}
	if op, err := s.Operation(strings.TrimSpace(out)); err != nil {
		t.Fatalf("async operation: %v", err)
	} else {
		wait(t, s, op)
	}
	if _, err := execute(t, url, "instances", "create", "Bad_Name"); err == nil || !strings.Contains(err.Error(), "invalid request") {
		t.Fatalf("invalid name: %v", err)
	}
}
