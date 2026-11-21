package cloud

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

func groupPolicy(g Group) string {
	labels := fmt.Sprintf("{service=%q}", g.ID+"@file")
	healthy := "sum(traefik_service_server_up" + labels + ")"
	query := "(sum(rate(traefik_service_requests_total" + labels + "[30s])) / " + healthy +
		") and (" + healthy + " > 0) and on() (up{job=\"traefik\"} == 1 and time() - timestamp(up{job=\"traefik\"}) < 15)"
	return fmt.Sprintf(`scaling %q {
  enabled = true
  min = %d
  max = %d
  type = "cluster"
  policy {
    evaluation_interval = "5s"
    cooldown = "30s"
    check "requests-per-healthy-instance" {
      source = "prometheus"
      query_window = "instant"
      query = %s
      strategy "target-value" {
        target = %q
        max_scale_down = "1"
      }
    }
    target "lab-groups" {
      id = %q
    }
  }
}
`, g.ID, g.Min, g.Max, strconv.Quote(query), strconv.FormatFloat(g.TargetRPS, 'f', -1, 64), g.ID)
}

func (s *Service) writeGroupPoliciesLocked() (string, error) {
	var config strings.Builder
	if s.state.Autoscaling == "active" {
		for _, g := range s.state.Groups {
			if !g.Deleting {
				config.WriteString(groupPolicy(g))
			}
		}
	}
	policies := config.String()
	err := writeGroupFile(filepath.Join(s.groupFiles(), "policies", "groups.hcl"), []byte(policies))
	return policies, err
}

func (s *Service) publishGroupPolicies(ctx context.Context) error {
	if s.groupAutoscaler == "" {
		return nil
	}
	s.mu.Lock()
	if s.state.Autoscaling == "" {
		s.publishedPolicy = ""
		s.mu.Unlock()
		return nil
	}
	policy, err := s.writeGroupPoliciesLocked()
	s.mu.Unlock()
	if err != nil || s.publishedPolicy == policy {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.groupAutoscaler+"/v1/agent/reload", nil)
	if err != nil {
		return err
	}
	res, err := groupHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("reload instance group policies: %s", res.Status)
	}
	s.publishedPolicy = policy
	return nil
}
