package cloud_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func TestMachineTypePrices(t *testing.T) {
	for name, want := range map[string]float64{"lab-small": 0.10, "lab-medium": 0.40} {
		kind, err := cloud.LookupMachineType(name)
		if err != nil || kind.PricePerHour != want {
			t.Fatalf("%s: %+v %v", name, kind, err)
		}
	}
}

type ledgerFile struct {
	ResetAt time.Time
	Entries []cloud.UsageEntry
}

func readLedger(t *testing.T, path string) ledgerFile {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- the test reads its own temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	var l ledgerFile
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func openEntry(l ledgerFile, kind, name string) *cloud.UsageEntry {
	for i, e := range l.Entries {
		if e.Kind == kind && e.Name == name && e.End == nil {
			return &l.Entries[i]
		}
	}
	return nil
}

func billingItem(t *testing.T, s *cloud.Service, kind, name string) (cloud.BillingItem, bool) {
	t.Helper()
	r, err := s.Billing(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(r.Items, func(it cloud.BillingItem) bool { return it.Kind == kind && it.Name == name })
	if i < 0 {
		return cloud.BillingItem{}, false
	}
	return r.Items[i], true
}

func reopen(t *testing.T, f *fake.Infra, dir string) *cloud.Service {
	t.Helper()
	s, err := cloud.NewService(f, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBillingStartsAtReset(t *testing.T) {
	before := time.Now()
	s, _, _ := newService(t)
	r, err := s.Billing(time.Time{})
	if err != nil || r.Total != 0 || len(r.Items) != 0 || r.ResetAt.Before(before) || r.ResetAt.After(time.Now()) || !r.Since.Equal(r.ResetAt) {
		t.Fatalf("report: %+v %v", r, err)
	}
}

func TestBillingTracksClusterMachines(t *testing.T) {
	s, _, _ := newService(t)
	create(t, s, 1)
	if it, ok := billingItem(t, s, "machine", "demo"); !ok || !it.Running || it.Type != "lab-small" || it.Cluster != "demo" {
		t.Fatalf("after create: %+v %v", it, ok)
	}
	op, err := s.DeleteCluster("demo", "student")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, s, op); op.Status != "DONE" {
		t.Fatalf("delete: %+v", op)
	}
	if it, ok := billingItem(t, s, "machine", "demo"); !ok || it.Running {
		t.Fatalf("after delete: %+v %v", it, ok)
	}
}

func TestBillingInstanceStopStart(t *testing.T) {
	s, _, dir := newService(t)
	createInstance(t, s, "vm1")
	for _, change := range []func(string, string) (cloud.Operation, error){s.StopInstance, s.StartInstance} {
		op, err := change("vm1", "student")
		if err != nil {
			t.Fatal(err)
		}
		if op = wait(t, s, op); op.Status != "DONE" {
			t.Fatalf("operation: %+v", op)
		}
	}
	l := readLedger(t, filepath.Join(dir, "billing", "usage.json"))
	r, err := s.Billing(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	n, want := 0, 0.0
	for _, e := range l.Entries {
		if e.Kind == "instance" && e.Name == "vm1" {
			end := r.Until
			if e.End != nil {
				end = *e.End
			}
			n++
			want += end.Sub(e.Start).Seconds()
		}
	}
	if n != 2 || len(r.Items) != 1 || !r.Items[0].Running || math.Abs(r.Items[0].Seconds-want) > 1e-6 {
		t.Fatalf("entries=%d want %vs report=%+v", n, want, r)
	}
}

func TestBillingSurvivesRestart(t *testing.T) {
	s, f, dir := newService(t)
	create(t, s, 1)
	path := filepath.Join(dir, "billing", "usage.json")
	before := openEntry(readLedger(t, path), "machine", "demo")
	if before == nil {
		t.Fatal("no open entry before restart")
	}
	reopen(t, f, dir)
	l := readLedger(t, path)
	after := openEntry(l, "machine", "demo")
	if after == nil || !after.Start.Equal(before.Start) || len(l.Entries) != 1 {
		t.Fatalf("after restart: %+v", l.Entries)
	}
}

func TestBillingResetWhenLedgerMissing(t *testing.T) {
	s, f, dir := newService(t)
	create(t, s, 1)
	path := filepath.Join(dir, "billing", "usage.json")
	first := readLedger(t, path).ResetAt
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	reopen(t, f, dir)
	l := readLedger(t, path)
	e := openEntry(l, "machine", "demo")
	if !l.ResetAt.After(first) || e == nil || !e.Start.Equal(l.ResetAt) {
		t.Fatalf("after reset: %+v", l)
	}
}

func TestBillingCorruptLedgerStartsFresh(t *testing.T) {
	for _, content := range []string{"{", "{}", "null"} {
		t.Run(content, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "billing"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "billing", "usage.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			before := time.Now()
			s := reopen(t, fake.New(), dir)
			if r, err := s.Billing(time.Time{}); err != nil || r.ResetAt.Before(before) {
				t.Fatalf("report: %+v %v", r, err)
			}
		})
	}
}

func TestBillingWriteFailureDoesNotFailOperations(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	s, _, dir := newService(t)
	billing := filepath.Join(dir, "billing")
	// #nosec G302 -- a read-only directory makes every ledger write fail.
	if err := os.Chmod(billing, 0o500); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- restore private directory permissions for test cleanup.
	t.Cleanup(func() { _ = os.Chmod(billing, 0o700) })
	create(t, s, 1)
	if l := readLedger(t, filepath.Join(billing, "usage.json")); len(l.Entries) != 0 {
		t.Fatalf("ledger written to a read-only directory: %+v", l)
	}
}

func TestBillingDirFromEnv(t *testing.T) {
	other := filepath.Join(t.TempDir(), "ledger")
	t.Setenv("CLOUD_BILLING_DIR", other)
	_, _, dir := newService(t)
	if _, err := os.Stat(filepath.Join(other, "usage.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "billing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default dir used: %v", err)
	}
}

func TestBillingDisks(t *testing.T) {
	t.Setenv("CLOUD_DISK_VOLUME", "test")
	s, f, dir := newService(t)
	if _, err := s.ChangeDisk(t.Context(), "create", "data", "1Gi", ""); err != nil {
		t.Fatal(err)
	}
	if it, ok := billingItem(t, s, "disk", "data"); !ok || !it.Running || it.Bytes != 1073741824 {
		t.Fatalf("after create: %+v %v", it, ok)
	}
	path := filepath.Join(dir, "billing", "usage.json")
	created := openEntry(readLedger(t, path), "disk", "data")
	if created == nil {
		t.Fatal("no open disk entry after create")
	}
	start := created.Start
	s = reopen(t, f, dir)
	if e := openEntry(readLedger(t, path), "disk", "data"); e == nil || !e.Start.Equal(start) {
		t.Fatalf("after restart: %+v", e)
	}
	if _, err := s.ChangeDisk(t.Context(), "delete", "data", "", ""); err != nil {
		t.Fatal(err)
	}
	if it, ok := billingItem(t, s, "disk", "data"); !ok || it.Running {
		t.Fatalf("after delete: %+v %v", it, ok)
	}
}

func TestBillingSinceValidation(t *testing.T) {
	s, _, _ := newService(t)
	if _, err := s.Billing(time.Now().Add(time.Minute)); !errors.Is(err, cloud.ErrInvalid) {
		t.Fatalf("future: %v", err)
	}
	r, err := s.Billing(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if early, err := s.Billing(r.ResetAt.Add(-time.Hour)); err != nil || !early.Since.Equal(r.ResetAt) {
		t.Fatalf("clamp: %+v %v", early, err)
	}
}
