package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/atomicfile"
)

const (
	groupWarmup        = 10 * time.Second
	groupHealthGrace   = 30 * time.Second
	groupFailureWindow = 10 * time.Second
)

var groupHTTP = &http.Client{
	Timeout:       time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	Transport:     &http.Transport{},
}

// ConfigureInstanceGroupRouting connects the group controller to the lab's Traefik.
func (s *Service) ConfigureInstanceGroupRouting(proxyURL, metricsURL, autoscalerURL string) {
	s.groupProxy = proxyURL
	s.groupMetrics = metricsURL
	s.groupAutoscaler = autoscalerURL
}

func (s *Service) groupFiles() string { return filepath.Join(s.dir, "autoscaling") }

func backendURL(member Instance, group Group) (string, bool) {
	ip, err := netip.ParseAddr(member.IP)
	if err != nil {
		return "", false
	}
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(group.Port)), true
}

func writeGroupFile(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(data, existing) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return atomicfile.Write(path, data)
}

type proxyServer struct {
	URL string `json:"url"`
}

func (s *Service) writeGroupRoutesLocked() error {
	routers := map[string]any{}
	services := map[string]any{}
	middlewares := map[string]any{}
	for _, g := range s.state.Groups {
		prefix := "/groups/" + g.Name
		routers[g.ID] = map[string]any{"rule": "PathPrefix(`" + prefix + "/`)", "entryPoints": []string{"web"}, "service": g.ID, "middlewares": []string{g.ID}}
		middlewares[g.ID] = map[string]any{"stripPrefix": map[string]any{"prefixes": []string{prefix}}}
		servers := []proxyServer{}
		for _, i := range s.state.Instances {
			if i.GroupID == g.ID && i.Status == "RUNNING" && !i.ReadyAt.IsZero() && !i.Draining {
				if address, ok := backendURL(i, g); ok {
					servers = append(servers, proxyServer{URL: address})
				}
			}
		}
		services[g.ID] = map[string]any{"loadBalancer": map[string]any{
			"servers": servers, "healthCheck": map[string]any{"path": g.HealthPath, "interval": "2s", "timeout": "1s", "followRedirects": false},
		}}
	}
	config := map[string]any{}
	if len(routers) != 0 {
		config["http"] = map[string]any{"routers": routers, "services": services, "middlewares": middlewares}
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return writeGroupFile(filepath.Join(s.groupFiles(), "traefik", "groups.yml"), data)
}

func groupGet(ctx context.Context, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	return groupHTTP.Do(req)
}

func probeGroupMember(ctx context.Context, address string) bool {
	response, err := groupGet(ctx, address)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 400
}

func (s *Service) groupBackendAbsent(ctx context.Context, groupID, backend string) bool {
	res, err := groupGet(ctx, s.groupProxy+"/api/http/services/"+groupID+"@file")
	if err != nil {
		// If the proxy has stopped accepting connections there are no new requests.
		var op *net.OpError
		return errors.As(err, &op) && op.Op == "dial" && !op.Timeout()
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNotFound {
		return true
	}
	var service struct {
		LoadBalancer struct {
			Servers []proxyServer `json:"servers"`
		} `json:"loadBalancer"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&service) != nil {
		return false
	}
	for _, server := range service.LoadBalancer.Servers {
		if server.URL == backend {
			return false
		}
	}
	return true
}

func (s *Service) observeGroupRouting(ctx context.Context) error {
	s.mu.Lock()
	state := s.state.clone()
	s.mu.Unlock()
	for _, member := range state.Instances {
		if member.GroupID == "" || state.instanceBusy(member.Name) {
			continue
		}
		group, err := state.group(member.GroupID)
		if err != nil {
			continue
		}
		backend, ok := backendURL(member, *group)
		if !ok {
			continue
		}
		nextMember := member
		now := time.Now().UTC()
		switch {
		case member.Draining && member.DrainUntil.IsZero():
			if s.groupBackendAbsent(ctx, group.ID, backend) {
				nextMember.DrainUntil = now.Add(time.Duration(group.DrainSeconds) * time.Second)
			}
		case !member.Draining && !group.Deleting && member.Status == "RUNNING":
			address, err := url.JoinPath(backend, group.HealthPath)
			if err != nil {
				return fmt.Errorf("group health URL: %w", err)
			}
			if probeGroupMember(ctx, address) {
				if nextMember.ReadyAt.IsZero() || !nextMember.FailedHealthAt.IsZero() {
					nextMember.ReadyAt = now
				}
				nextMember.FailedHealthAt = time.Time{}
			} else if nextMember.FailedHealthAt.IsZero() {
				nextMember.FailedHealthAt = now
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if nextMember == member {
			continue
		}
		s.mu.Lock()
		next := s.state.clone()
		if current, err := next.instance(member.Name); err == nil && *current == member && !next.instanceBusy(member.Name) {
			*current = nextMember
			err = s.store(next)
			s.mu.Unlock()
			if err != nil {
				return err
			}
		} else {
			s.mu.Unlock()
		}
	}
	return nil
}

func groupMemberFailed(i Instance) bool {
	now := time.Now()
	return !i.FailedHealthAt.IsZero() && now.Sub(i.BootedAt) >= groupHealthGrace &&
		now.Sub(i.FailedHealthAt) >= groupFailureWindow
}
