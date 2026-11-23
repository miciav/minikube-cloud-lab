package cloud

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

// MaxGroupBootstrapBytes bounds scripts stored with a group template.
const MaxGroupBootstrapBytes = 16 * 1024

// GroupSpec configures an HTTP service and the instances that serve it.
type GroupSpec struct {
	Name         string  `json:"name"`
	Image        string  `json:"image"`
	MachineType  string  `json:"machineType"`
	Bootstrap    string  `json:"bootstrap"`
	Min          int     `json:"min"`
	Desired      int     `json:"desired"`
	Max          int     `json:"max"`
	Port         int     `json:"port"`
	HealthPath   string  `json:"healthPath"`
	TargetRPS    float64 `json:"targetRPS"`
	DrainSeconds int     `json:"drainSeconds,omitempty"`
}

// Group stores a versioned template and its desired capacity.
type Group struct {
	GroupSpec
	ID              string    `json:"id"`
	Ready           int       `json:"ready"`
	Pending         int       `json:"pending"`
	ScalingReady    bool      `json:"scalingReady"`
	TemplateVersion int       `json:"templateVersion"`
	Deleting        bool      `json:"deleting,omitempty"`
	LastError       string    `json:"lastError,omitempty"`
	LastScaleAt     time.Time `json:"lastScaleAt,omitzero"`
	RetryAt         time.Time `json:"retryAt,omitzero"`
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
		if s.groupProxy != "" {
			return fmt.Errorf("%w: prepare instance autoscaling before activation", ErrFailedPrecondition)
		}
		fallthrough
	case "installing":
		next := s.state.clone()
		next.Autoscaling = "active"
		return s.store(next)
	default:
		return fmt.Errorf("%w: finish uninstalling instance autoscaling first", ErrFailedPrecondition)
	}
}

func (spec GroupSpec) validate(st *snapshot) error {
	if len(validation.IsDNS1123Label(spec.Name)) > 0 || strings.HasPrefix(spec.Name, "asg-") {
		return fmt.Errorf("%w: group name must be a DNS-1123 label outside the asg- namespace", ErrInvalid)
	}
	img, err := st.lookupImage(spec.Image)
	if err != nil {
		return err
	}
	if _, err := LookupMachineType(spec.MachineType); err != nil {
		return err
	}
	if spec.Min < 1 || spec.Min > spec.Desired || spec.Desired > spec.Max || spec.Max > MaxInstances {
		return fmt.Errorf("%w: require 1 <= min <= desired <= max <= %d", ErrInvalid, MaxInstances)
	}
	if spec.DrainSeconds < 0 || spec.DrainSeconds > 120 {
		return fmt.Errorf("%w: drain timeout must be between 0 and 120 seconds", ErrInvalid)
	}
	if (strings.TrimSpace(spec.Bootstrap) == "" && img.Source == "") || len(spec.Bootstrap) > MaxGroupBootstrapBytes || strings.ContainsRune(spec.Bootstrap, 0) {
		return fmt.Errorf("%w: bootstrap must contain 1–%d bytes of script", ErrInvalid, MaxGroupBootstrapBytes)
	}
	return spec.validateHTTP()
}

func (spec GroupSpec) validateHTTP() error {
	path, err := url.ParseRequestURI(spec.HealthPath)
	if err != nil || !strings.HasPrefix(spec.HealthPath, "/") || strings.HasPrefix(spec.HealthPath, "//") || path.RawQuery != "" || strings.ContainsAny(spec.HealthPath, "?#") {
		return fmt.Errorf("%w: health check requires an absolute URL path", ErrInvalid)
	}
	if spec.Port < 1 || spec.Port > 65535 || spec.TargetRPS <= 0 || math.IsNaN(spec.TargetRPS) || math.IsInf(spec.TargetRPS, 0) {
		return fmt.Errorf("%w: require a valid port and a positive requests-per-second target", ErrInvalid)
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
	if err := spec.validate(&s.state); err != nil {
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
	groups := make([]Group, 0, len(s.state.Groups))
	for _, group := range s.state.Groups {
		groups = append(groups, s.groupStatus(group))
	}
	return groups
}

// Group looks up a group by user-visible name or immutable ID.
func (s *Service) Group(key string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.state.group(key)
	if err != nil {
		return Group{}, err
	}
	return s.groupStatus(*g), nil
}

func (s *Service) groupStatus(g Group) Group {
	now := time.Now()
	for _, member := range s.state.Instances {
		if member.GroupID != g.ID || member.Draining {
			continue
		}
		if member.Status == "RUNNING" && !member.ReadyAt.IsZero() && member.FailedHealthAt.IsZero() && now.Sub(member.ReadyAt) >= groupWarmup {
			g.Ready++
		} else {
			g.Pending++
		}
	}
	quotaBlocked := g.Ready > 0 && g.Ready < g.Desired && len(s.state.Instances) >= MaxInstances
	g.ScalingReady = s.state.Autoscaling == "active" && !g.Deleting && g.Pending == 0 &&
		(g.Ready == g.Desired || quotaBlocked) && now.Sub(g.LastScaleAt) >= groupCooldown
	return g
}

// ResizeGroup sets absolute capacity and records changes in operation history.
func (s *Service) ResizeGroup(key string, desired int, requester, reason string) (Group, error) {
	return s.resizeGroup(key, desired, requester, reason, nil)
}

// ApplyGroupPolicy updates the group's capacity only while the policy's observation is current.
func (s *Service) ApplyGroupPolicy(id string, expected, desired int, reason string) error {
	if !strings.HasPrefix(id, "asg-") {
		return ErrNotFound
	}
	_, err := s.resizeGroup(id, desired, "instance-autoscaler", reason, &expected)
	return err
}

func (s *Service) resizeGroup(key string, desired int, requester, reason string, expected *int) (Group, error) {
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
		return s.groupStatus(*g), nil
	}
	if expected != nil {
		if *expected != g.Desired || !s.groupStatus(*g).ScalingReady {
			return Group{}, fmt.Errorf("%w: group changed, is converging or is in cooldown", ErrFailedPrecondition)
		}
		if desired > g.Desired {
			free := MaxInstances - len(s.state.Instances)
			if free <= 0 {
				return Group{}, fmt.Errorf("%w: instance quota reached", ErrFailedPrecondition)
			}
			desired = min(desired, g.Desired+free)
		}
	}
	g.Desired = desired
	op := next.newOperation("RESIZE_GROUP", g.Name, requester)
	entry := next.operation(op.ID)
	now := time.Now().UTC()
	g.LastScaleAt = now
	entry.Status, entry.EndedAt, entry.Reason = "DONE", &now, reason
	return s.groupStatus(*g), s.store(next)
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
	g.Deleting = true
	g.RetryAt = time.Time{}
	return s.store(next)
}
