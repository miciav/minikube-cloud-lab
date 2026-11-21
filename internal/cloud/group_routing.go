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
	"syscall"
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
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(data, existing) { // #nosec G304 -- generated configuration belongs to cloud state.
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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

type groupHealth int

const (
	healthUnknown groupHealth = iota
	healthOK
	healthFailed
)

type proxyGroupView struct {
	LoadBalancer *struct {
		Servers []proxyServer `json:"servers"`
	} `json:"loadBalancer"`
	ServerStatus map[string]string `json:"serverStatus"`
}

func (s *Service) groupProxyView(ctx context.Context, groupID string) (*proxyGroupView, bool) {
	res, err := groupGet(ctx, s.groupProxy+"/api/http/services/"+groupID+"@file")
	if err != nil {
		return nil, false
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNotFound {
		return nil, true
	}
	var service proxyGroupView
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&service) != nil || service.LoadBalancer == nil {
		return nil, false
	}
	return &service, true
}

func groupMemberHealth(ctx context.Context, address string, service *proxyGroupView, backend string, registered bool) groupHealth {
	if registered && service != nil {
		switch service.ServerStatus[backend] {
		case "UP":
			return healthOK
		case "DOWN":
			return healthFailed
		}
	}
	response, err := groupGet(ctx, address)
	if err != nil {
		if !registered && errors.Is(err, syscall.ECONNREFUSED) {
			return healthFailed
		}
		return healthUnknown
	}
	_ = response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 400 {
		return healthOK
	}
	return healthFailed
}

func (s *Service) groupBackendAbsent(ctx context.Context, groupID, backend string) bool {
	service, known := s.groupProxyView(ctx, groupID)
	if !known {
		if s.instances == nil {
			return false
		}
		running, err := s.instances.InstanceLoadBalancerRunning(ctx)
		return err == nil && !running
	}
	if service != nil {
		for _, server := range service.LoadBalancer.Servers {
			if server.URL == backend {
				return false
			}
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
		next, err := s.observeMember(ctx, member, *group, backend)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if next != member {
			if err := s.saveMemberObservation(member, next); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) observeMember(ctx context.Context, member Instance, group Group, backend string) (Instance, error) {
	now := time.Now().UTC()
	if member.Draining {
		if member.DrainUntil.IsZero() && s.groupBackendAbsent(ctx, group.ID, backend) {
			member.DrainUntil = now.Add(time.Duration(group.DrainSeconds) * time.Second)
		}
		return member, nil
	}
	if group.Deleting || member.Status != "RUNNING" {
		return member, nil
	}
	address, err := url.JoinPath(backend, group.HealthPath)
	if err != nil {
		return member, fmt.Errorf("group health URL: %w", err)
	}
	var proxy *proxyGroupView
	registered := !member.ReadyAt.IsZero()
	if registered {
		proxy, _ = s.groupProxyView(ctx, group.ID)
	}
	switch groupMemberHealth(ctx, address, proxy, backend, registered) {
	case healthOK:
		if !registered || !member.FailedHealthAt.IsZero() {
			member.ReadyAt = now
		}
		member.FailedHealthAt = time.Time{}
	case healthFailed:
		if member.FailedHealthAt.IsZero() {
			member.FailedHealthAt = now
		}
	case healthUnknown:
		member.FailedHealthAt = time.Time{}
	}
	return member, nil
}

func (s *Service) saveMemberObservation(previous, observation Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state.clone()
	current, err := next.instance(previous.Name)
	if err != nil || *current != previous || next.instanceBusy(previous.Name) {
		return nil
	}
	*current = observation
	return s.store(next)
}

func groupMemberFailed(i Instance) bool {
	now := time.Now()
	return !i.FailedHealthAt.IsZero() && now.Sub(i.BootedAt) >= groupHealthGrace &&
		now.Sub(i.FailedHealthAt) >= groupFailureWindow
}
