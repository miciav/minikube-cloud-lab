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
	resp, err := http.Get(url + "/v1/billing")
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(resp, err)
	}
	_ = resp.Body.Close()
}

func TestBillingEndpointRejects(t *testing.T) {
	_, _, _, url := setup(t)
	for _, since := range []string{"tomorrow", "-5m", "2999-01-01T00:00:00Z"} {
		resp, err := http.Get(url + "/v1/billing?since=" + since)
		if err != nil || resp.StatusCode != 400 {
			t.Fatalf("%s: %v %v", since, resp, err)
		}
		_ = resp.Body.Close()
	}
}
