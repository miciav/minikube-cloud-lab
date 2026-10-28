package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

// A single strong ETag protects portal actions; absent If-Match keeps existing CLI behavior.
func clusterGeneration(r *http.Request) (*time.Time, error) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, cloud.ErrInvalid
	}
	tag, err := strconv.Unquote(values[0])
	if err != nil {
		return nil, cloud.ErrInvalid
	}
	value, err := time.Parse(time.RFC3339Nano, tag)
	if err != nil {
		return nil, cloud.ErrInvalid
	}
	return &value, nil
}
