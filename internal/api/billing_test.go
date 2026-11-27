package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestBillingEndpoint(t *testing.T) {
	c, _, _, url := setup(t)
	for _, since := range []string{"", "30m"} {
		report, err := c.Billing(t.Context(), since)
		if err != nil || report.Currency != "EUR" || report.ResetAt.IsZero() {
			t.Fatalf("%q: %+v %v", since, report, err)
		}
		// fresh service: resetAt is newer than now-30m, so since clamps to it
		if report.Since.Before(report.ResetAt) || report.Since.After(report.ResetAt.Add(time.Second)) {
			t.Fatalf("%q: since %v resetAt %v", since, report.Since, report.ResetAt)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/v1/billing", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatal(resp, err)
	}
	_ = resp.Body.Close()
}

func TestBillingEndpointRejects(t *testing.T) {
	_, _, _, url := setup(t)
	for _, since := range []string{"tomorrow", "-5m", "2999-01-01T00:00:00Z"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/v1/billing?since="+since, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %v %v", since, resp, err)
		}
		_ = resp.Body.Close()
	}
}

func TestBillingSinceRFC3339(t *testing.T) {
	c, _, _, _ := setup(t)
	base, err := c.Billing(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	// earlier than resetAt: clamped
	early := base.ResetAt.Add(-time.Hour).UTC().Format(time.RFC3339)
	r, err := c.Billing(t.Context(), early)
	if err != nil || !r.Since.Equal(base.ResetAt) {
		t.Fatalf("clamp: %+v %v", r, err)
	}
	// a "+" offset must survive URL escaping; the instant is just after resetAt and in the past
	at := base.ResetAt.Add(time.Millisecond).In(time.FixedZone("", 2*3600))
	time.Sleep(10 * time.Millisecond)
	r, err = c.Billing(t.Context(), at.Format(time.RFC3339Nano))
	if err != nil || !r.Since.Equal(at) {
		t.Fatalf("offset: %v want %v err %v", r.Since, at, err)
	}
}
