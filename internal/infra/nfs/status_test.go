package nfs

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestStatusSeparatesInvalidRequestsFromFailures(t *testing.T) {
	if got := statusFor(fmt.Errorf("%w: identifier", errInvalid)); got != http.StatusBadRequest {
		t.Fatal(got)
	}
	if got := statusFor(errors.New("insufficient free backing storage")); got != http.StatusConflict {
		t.Fatal(got)
	}
}
