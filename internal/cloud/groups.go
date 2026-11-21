package cloud

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

// MaxGroupBootstrapBytes bounds scripts stored with a group template.
const MaxGroupBootstrapBytes = 16 * 1024

// GroupSpec configures an HTTP service and the instances that serve it.
type GroupSpec struct {
	Name        string  `json:"name"`
	Image       string  `json:"image"`
	MachineType string  `json:"machineType"`
	Bootstrap   string  `json:"bootstrap"`
	Min         int     `json:"min"`
	Desired     int     `json:"desired"`
	Max         int     `json:"max"`
	Port        int     `json:"port"`
	HealthPath  string  `json:"healthPath"`
	TargetRPS   float64 `json:"targetRPS"`
}

// Group stores a versioned template and its desired capacity.
type Group struct {
	GroupSpec
	ID              string `json:"id"`
	TemplateVersion int    `json:"templateVersion"`
	Deleting        bool   `json:"deleting,omitempty"`
}

func (st *snapshot) group(key string) (*Group, error) {
	for i := range st.Groups {
		g := &st.Groups[i]
		if g.ID == key || g.Name == key {
			return g, nil
		}
	}
	return nil, fmt.Errorf("%w: group %s", ErrNotFound, key)
}

func (st *snapshot) requireAutoscaling() error {
	if st.Autoscaling != "active" {
		return fmt.Errorf("%w: instance autoscaling is not active", ErrFailedPrecondition)
	}
	return nil
}

// ActivateInstanceAutoscaling enables creation and resizing of groups.
func (s *Service) ActivateInstanceAutoscaling() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instances == nil {
		return errNoInstances
	}
	switch s.state.Autoscaling {
	case "active":
		return nil
	case "":
		next := s.state.clone()
		next.Autoscaling = "active"
		return s.store(next)
	default:
		return fmt.Errorf("%w: finish uninstalling instance autoscaling first", ErrFailedPrecondition)
	}
}

func (spec GroupSpec) validate() error {
	if len(validation.IsDNS1123Label(spec.Name)) > 0 || strings.HasPrefix(spec.Name, "asg-") {
		return fmt.Errorf("%w: group name must be a DNS-1123 label outside the asg- namespace", ErrInvalid)
	}
	if _, err := LookupImage(spec.Image); err != nil {
		return err
	}
	if _, err := LookupMachineType(spec.MachineType); err != nil {
		return err
	}
	if spec.Min < 1 || spec.Min > spec.Desired || spec.Desired > spec.Max || spec.Max > MaxInstances {
		return fmt.Errorf("%w: require 1 <= min <= desired <= max <= %d", ErrInvalid, MaxInstances)
	}
	path, err := url.ParseRequestURI(spec.HealthPath)
	if err != nil || !strings.HasPrefix(spec.HealthPath, "/") || strings.HasPrefix(spec.HealthPath, "//") || path.RawQuery != "" || strings.ContainsAny(spec.HealthPath, "?#") {
		return fmt.Errorf("%w: health check requires an absolute URL path", ErrInvalid)
	}
	if spec.Port < 1 || spec.Port > 65535 || spec.TargetRPS <= 0 || math.IsNaN(spec.TargetRPS) || math.IsInf(spec.TargetRPS, 0) {
		return fmt.Errorf("%w: require a valid port and a positive requests-per-second target", ErrInvalid)
	}
	if strings.TrimSpace(spec.Bootstrap) == "" || len(spec.Bootstrap) > MaxGroupBootstrapBytes || strings.ContainsRune(spec.Bootstrap, 0) {
		return fmt.Errorf("%w: bootstrap must contain 1–%d bytes of script", ErrInvalid, MaxGroupBootstrapBytes)
	}
	return nil
}

// CreateGroup persists an immutable launch template with initial desired capacity.
func (s *Service) CreateGroup(spec GroupSpec) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.state.requireAutoscaling(); err != nil {
		return Group{}, err
	}
	if err := spec.validate(); err != nil {
		return Group{}, err
	}
	if _, err := s.state.group(spec.Name); err == nil {
		return Group{}, fmt.Errorf("%w: group %s already exists", ErrConflict, spec.Name)
	}
	var id [12]byte
	rand.Read(id[:])
	g := Group{GroupSpec: spec, ID: "asg-" + hex.EncodeToString(id[:]), TemplateVersion: 1}
	next := s.state.clone()
	next.Groups = append(next.Groups, g)
	return g, s.store(next)
}

// Groups returns the durable groups.
func (s *Service) Groups() []Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Group{}, s.state.Groups...)
}

// Group looks up a group by user-visible name or immutable ID.
func (s *Service) Group(key string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.state.group(key)
	if err != nil {
		return Group{}, err
	}
	return *g, nil
}

// ResizeGroup sets absolute capacity and records changes in operation history.
func (s *Service) ResizeGroup(key string, desired int, requester, reason string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.state.requireAutoscaling(); err != nil {
		return Group{}, err
	}
	next := s.state.clone()
	g, err := next.group(key)
	if err != nil {
		return Group{}, err
	}
	if g.Deleting {
		return Group{}, fmt.Errorf("%w: group %s is deleting", ErrFailedPrecondition, g.Name)
	}
	if desired < g.Min || desired > g.Max {
		return Group{}, fmt.Errorf("%w: desired capacity must be between %d and %d", ErrInvalid, g.Min, g.Max)
	}
	if desired == g.Desired {
		return *g, nil
	}
	g.Desired = desired
	op := next.newOperation("RESIZE_GROUP", g.Name, requester)
	entry := next.operation(op.ID)
	now := time.Now().UTC()
	entry.Status, entry.EndedAt, entry.Reason = "DONE", &now, reason
	return *g, s.store(next)
}

// DeleteGroup marks a group for removal by its controller.
func (s *Service) DeleteGroup(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.state.requireAutoscaling(); err != nil {
		return err
	}
	next := s.state.clone()
	g, err := next.group(key)
	if err != nil {
		return err
	}
	if g.Deleting {
		return nil
	}
	if len(next.Instances) == 0 {
		id := g.ID
		next.Groups = slices.DeleteFunc(next.Groups, func(g Group) bool { return g.ID == id })
	} else {
		g.Deleting = true
	}
	return s.store(next)
}
