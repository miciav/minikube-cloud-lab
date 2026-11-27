package cloud

import (
	"cmp"
	"slices"
	"time"
)

// UsageEntry is one accrual interval of a billable resource; End is nil while it accrues.
type UsageEntry struct {
	Kind    string     `json:"kind"`
	Name    string     `json:"name"`
	Cluster string     `json:"cluster,omitempty"`
	Type    string     `json:"type,omitempty"`
	Bytes   int64      `json:"bytes,omitempty"`
	Start   time.Time  `json:"start"`
	End     *time.Time `json:"end,omitempty"`
}

type usageLedger struct {
	ResetAt time.Time    `json:"resetAt"`
	Entries []UsageEntry `json:"entries"`
}

type usageResource struct {
	Kind, Name, Cluster, Type string
	Bytes                     int64
}

// BillingItem aggregates the cost of one resource over a report window.
type BillingItem struct {
	Kind    string  `json:"kind"`
	Name    string  `json:"name"`
	Cluster string  `json:"cluster,omitempty"`
	Type    string  `json:"type,omitempty"`
	Bytes   int64   `json:"bytes,omitempty"`
	Running bool    `json:"running"`
	Seconds float64 `json:"seconds"`
	Cost    float64 `json:"cost"`
}

// BillingReport lists resource costs between Since and Until.
type BillingReport struct {
	ResetAt  time.Time     `json:"resetAt"`
	Since    time.Time     `json:"since"`
	Until    time.Time     `json:"until"`
	Currency string        `json:"currency"`
	Total    float64       `json:"total"`
	Items    []BillingItem `json:"items"`
}

// billableResources returns machines, non-stopped instances and volumes keyed by Kind/Name.
func billableResources(st snapshot) map[string]usageResource {
	out := map[string]usageResource{}
	add := func(r usageResource) { out[r.Kind+"/"+r.Name] = r }
	for _, m := range st.Machines {
		add(usageResource{Kind: "machine", Name: m.Name, Cluster: m.Cluster, Type: m.MachineType})
	}
	for _, i := range st.Instances {
		if i.Status != "STOPPED" {
			add(usageResource{Kind: "instance", Name: i.Name, Type: i.MachineType})
		}
	}
	for _, v := range st.Volumes {
		n, _ := parseStorageBytes(v.Capacity) // unknown capacity bills nothing
		add(usageResource{Kind: "volume", Name: v.ID, Cluster: v.Cluster, Bytes: n})
	}
	return out
}

func (l *usageLedger) open(kind, name string) *UsageEntry {
	for i := range l.Entries {
		if e := &l.Entries[i]; e.End == nil && e.Kind == kind && e.Name == name {
			return e
		}
	}
	return nil
}

func (l *usageLedger) start(r usageResource, now time.Time) bool {
	if e := l.open(r.Kind, r.Name); e != nil {
		if e.Type == r.Type && e.Bytes == r.Bytes {
			return false
		}
		e.End = &now
	}
	l.Entries = append(l.Entries, UsageEntry{Kind: r.Kind, Name: r.Name, Cluster: r.Cluster, Type: r.Type, Bytes: r.Bytes, Start: now})
	return true
}

func (l *usageLedger) stop(kind, name string, now time.Time) bool {
	e := l.open(kind, name)
	if e == nil {
		return false
	}
	e.End = &now
	return true
}

// reconcile stops open entries of kinds missing from current and starts the rest.
func (l *usageLedger) reconcile(current map[string]usageResource, kinds []string, now time.Time) bool {
	changed := false
	for i := range l.Entries {
		e := &l.Entries[i]
		if _, ok := current[e.Kind+"/"+e.Name]; e.End == nil && !ok && slices.Contains(kinds, e.Kind) {
			e.End = &now
			changed = true
		}
	}
	keys := make([]string, 0, len(current))
	for k := range current {
		keys = append(keys, k)
	}
	slices.Sort(keys) // deterministic entry order
	for _, k := range keys {
		changed = l.start(current[k], now) || changed
	}
	return changed
}

func entryCost(e UsageEntry, hours float64) float64 {
	if e.Kind == "volume" || e.Kind == "disk" {
		return float64(e.Bytes) / (1 << 30) * StoragePricePerGiBMonth * hours / hoursPerMonth
	}
	kind, err := LookupMachineType(e.Type)
	if err != nil {
		return 0 // retired types are free
	}
	return hours * kind.PricePerHour
}

// report aggregates costs over [since, until] without mutating the ledger.
func (l usageLedger) report(since, until time.Time) BillingReport {
	r := BillingReport{ResetAt: l.ResetAt, Since: since, Until: until, Currency: "EUR", Items: []BillingItem{}}
	index := map[string]int{}
	latest := map[string]time.Time{}
	for _, e := range l.Entries {
		end := until
		if e.End != nil && e.End.Before(until) {
			end = *e.End
		}
		begin := e.Start
		if begin.Before(since) {
			begin = since
		}
		if !end.After(begin) {
			continue
		}
		key := e.Kind + "/" + e.Name
		i, ok := index[key]
		if !ok {
			i = len(r.Items)
			index[key] = i
			r.Items = append(r.Items, BillingItem{Kind: e.Kind, Name: e.Name})
		}
		it := &r.Items[i]
		if !e.Start.Before(latest[key]) {
			latest[key] = e.Start
			it.Cluster, it.Type, it.Bytes = e.Cluster, e.Type, e.Bytes
		}
		seconds := end.Sub(begin).Seconds()
		cost := entryCost(e, seconds/3600)
		it.Running = it.Running || e.End == nil
		it.Seconds += seconds
		it.Cost += cost
		r.Total += cost
	}
	slices.SortFunc(r.Items, func(a, b BillingItem) int {
		if a.Running != b.Running {
			if a.Running {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(b.Cost, a.Cost); c != 0 {
			return c
		}
		return cmp.Compare(a.Kind+"/"+a.Name, b.Kind+"/"+b.Name)
	})
	return r
}
