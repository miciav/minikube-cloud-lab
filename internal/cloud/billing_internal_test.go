package cloud

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func ended(t time.Time) *time.Time { return &t }

func TestBillableResources(t *testing.T) {
	got := billableResources(snapshot{
		Machines:  []Machine{{Name: "demo", Cluster: "lab", MachineType: "lab-small", Status: "DELETING"}},
		Instances: []Instance{{Name: "web", Status: "RUNNING", MachineType: "lab-medium"}, {Name: "off", Status: "STOPPED", MachineType: "lab-small"}},
		Volumes:   []Volume{{ID: "pvc-1", Cluster: "lab", Capacity: "2Gi"}},
	})
	want := map[string]usageResource{
		"machine/demo": {Kind: "machine", Name: "demo", Cluster: "lab", Type: "lab-small"},
		"instance/web": {Kind: "instance", Name: "web", Type: "lab-medium"},
		"volume/pvc-1": {Kind: "volume", Name: "pvc-1", Cluster: "lab", Bytes: 2147483648},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %+v want %+v", k, got[k], v)
		}
	}
}

func TestBillableVolumeWithoutCapacity(t *testing.T) {
	got := billableResources(snapshot{Volumes: []Volume{{ID: "pvc-1", Capacity: ""}}})
	if r, ok := got["volume/pvc-1"]; !ok || r.Bytes != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestLedgerStartStop(t *testing.T) {
	var l usageLedger
	r := usageResource{Kind: "machine", Name: "demo", Type: "lab-small"}
	if !l.start(r, t0) || l.start(r, t0.Add(time.Minute)) || len(l.Entries) != 1 {
		t.Fatalf("start: %+v", l.Entries)
	}
	if !l.stop("machine", "demo", t0.Add(time.Hour)) || l.stop("machine", "demo", t0.Add(2*time.Hour)) {
		t.Fatal("stop")
	}
	if len(l.Entries) != 1 || l.Entries[0].End == nil || !l.Entries[0].End.Equal(t0.Add(time.Hour)) {
		t.Fatalf("entries: %+v", l.Entries)
	}
}

func TestLedgerRateChange(t *testing.T) {
	var l usageLedger
	l.start(usageResource{Kind: "volume", Name: "pvc-1", Bytes: 1 << 30}, t0)
	if !l.start(usageResource{Kind: "volume", Name: "pvc-1", Bytes: 2 << 30}, t0.Add(time.Hour)) || len(l.Entries) != 2 {
		t.Fatalf("entries: %+v", l.Entries)
	}
	first, second := l.Entries[0], l.Entries[1]
	if first.End == nil || !first.End.Equal(t0.Add(time.Hour)) || second.End != nil || !second.Start.Equal(t0.Add(time.Hour)) || second.Bytes != 2<<30 {
		t.Fatalf("entries: %+v", l.Entries)
	}
}

func TestReconcileScopesKinds(t *testing.T) {
	var l usageLedger
	l.start(usageResource{Kind: "disk", Name: "data", Bytes: 1 << 30}, t0)
	l.start(usageResource{Kind: "machine", Name: "demo", Type: "lab-small"}, t0)
	if !l.reconcile(map[string]usageResource{}, []string{"machine", "instance", "volume"}, t0.Add(time.Hour)) {
		t.Fatal("expected change")
	}
	if l.Entries[0].End != nil || l.Entries[1].End == nil {
		t.Fatalf("entries: %+v", l.Entries)
	}
	web := usageResource{Kind: "instance", Name: "web", Type: "lab-small"}
	current := map[string]usageResource{"instance/web": web}
	if !l.reconcile(current, []string{"instance"}, t0.Add(time.Hour)) || l.reconcile(current, []string{"instance"}, t0.Add(2*time.Hour)) {
		t.Fatalf("entries: %+v", l.Entries)
	}
}

func TestReportCosts(t *testing.T) {
	l := usageLedger{ResetAt: t0, Entries: []UsageEntry{
		{Kind: "machine", Name: "demo", Type: "lab-small", Start: t0, End: ended(t0.Add(time.Hour))},
		{Kind: "instance", Name: "web", Type: "lab-medium", Start: t0, End: ended(t0.Add(30 * time.Minute))},
		{Kind: "volume", Name: "pvc-1", Bytes: 10 << 30, Start: t0},
		{Kind: "disk", Name: "data", Bytes: 10 << 30, Start: t0, End: ended(t0.Add(73 * time.Hour))},
	}}
	r := l.report(t0, t0.Add(73*time.Hour))
	if r.Currency != "EUR" || !near(r.Total, 0.50) || len(r.Items) != 4 {
		t.Fatalf("report: %+v", r)
	}
	want := []struct {
		name string
		cost float64
	}{{"pvc-1", 0.10}, {"web", 0.20}, {"data", 0.10}, {"demo", 0.10}}
	for i, w := range want {
		if it := r.Items[i]; it.Name != w.name || !near(it.Cost, w.cost) {
			t.Fatalf("item %d: %+v", i, it)
		}
	}
	if !r.Items[0].Running || r.Items[1].Running {
		t.Fatalf("running: %+v", r.Items)
	}
	if len(l.Entries) != 4 || l.Entries[2].End != nil {
		t.Fatal("report mutated ledger")
	}
}

func TestReportWindow(t *testing.T) {
	l := usageLedger{ResetAt: t0, Entries: []UsageEntry{
		{Kind: "machine", Name: "demo", Type: "lab-small", Start: t0, End: ended(t0.Add(2 * time.Hour))},
		{Kind: "machine", Name: "old", Type: "lab-small", Start: t0, End: ended(t0.Add(time.Hour))},
	}}
	r := l.report(t0.Add(90*time.Minute), t0.Add(3*time.Hour))
	if len(r.Items) != 1 || r.Items[0].Name != "demo" || !near(r.Items[0].Seconds, 1800) {
		t.Fatalf("report: %+v", r)
	}
	if empty := (usageLedger{}).report(t0, t0.Add(time.Hour)); empty.Items == nil {
		t.Fatal("items must be non-nil")
	}
}

func TestReportInstanceTwoIntervals(t *testing.T) {
	l := usageLedger{Entries: []UsageEntry{
		{Kind: "instance", Name: "web", Type: "lab-small", Start: t0, End: ended(t0.Add(10 * time.Minute))},
		{Kind: "instance", Name: "web", Type: "lab-small", Start: t0.Add(20 * time.Minute), End: ended(t0.Add(30 * time.Minute))},
	}}
	r := l.report(t0, t0.Add(time.Hour))
	if len(r.Items) != 1 || !near(r.Items[0].Seconds, 1200) || r.Items[0].Running {
		t.Fatalf("report: %+v", r)
	}
}

func TestReportUnknownTypeIsFree(t *testing.T) {
	l := usageLedger{Entries: []UsageEntry{{Kind: "machine", Name: "demo", Type: "retired", Start: t0}}}
	r := l.report(t0, t0.Add(time.Hour))
	if len(r.Items) != 1 || r.Items[0].Cost != 0 || r.Total != 0 || !r.Items[0].Running {
		t.Fatalf("report: %+v", r)
	}
}
