package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad-autoscaler/plugins"
	"github.com/hashicorp/nomad-autoscaler/plugins/base"
	"github.com/hashicorp/nomad-autoscaler/sdk"
)

const pluginName = "lab-groups"

type labTarget struct {
	mu       sync.Mutex
	address  string
	expected map[string]int
}

var client = &http.Client{Timeout: time.Second}

func (p *labTarget) SetConfig(config map[string]string) error {
	address, err := url.Parse(config["address"])
	if err != nil || address.Scheme != "http" || address.Host == "" {
		return fmt.Errorf("address must be a cloud HTTP endpoint")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.address = strings.TrimRight(address.String(), "/")
	p.expected = make(map[string]int)
	return nil
}

func (*labTarget) PluginInfo() (*base.PluginInfo, error) {
	return &base.PluginInfo{Name: pluginName, PluginType: sdk.PluginTypeTarget}, nil
}

func (p *labTarget) Status(config map[string]string) (*sdk.TargetStatus, error) {
	id := config["id"]
	p.mu.Lock()
	address := p.address
	p.mu.Unlock()
	res, err := client.Get(address + "/v1/groups/" + url.PathEscape(id))
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNotFound {
		return &sdk.TargetStatus{}, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read cloud group status: %s", res.Status)
	}
	var group struct {
		Desired      int
		ScalingReady bool
		LastScaleAt  time.Time
	}
	if err := json.NewDecoder(res.Body).Decode(&group); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.expected[id] = group.Desired
	p.mu.Unlock()
	status := &sdk.TargetStatus{Ready: group.ScalingReady, Count: int64(group.Desired)}
	if !group.LastScaleAt.IsZero() {
		status.Meta = map[string]string{sdk.TargetStatusMetaKeyLastEvent: strconv.FormatInt(group.LastScaleAt.UnixNano(), 10)}
	}
	return status, nil
}

func (p *labTarget) Scale(action sdk.ScalingAction, config map[string]string) error {
	id := config["id"]
	p.mu.Lock()
	address := p.address
	expected := p.expected[id]
	p.mu.Unlock()
	body, err := json.Marshal(map[string]any{"desired": action.Count, "expected": expected, "reason": action.Reason})
	if err != nil {
		return err
	}
	res, err := client.Post(address+"/v1/groups/"+url.PathEscape(id)+"/scale", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	switch res.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound, http.StatusPreconditionFailed:
		return sdk.NewTargetScalingNoOpError("cloud group changed before scale: %s", res.Status)
	default:
		return fmt.Errorf("resize cloud group: %s", res.Status)
	}
}

func main() {
	plugins.Serve(func(hclog.Logger) interface{} { return &labTarget{} })
}
