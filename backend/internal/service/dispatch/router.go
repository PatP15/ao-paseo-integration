// Package dispatch selects execution hosts, atomically creates remote session
// attempts, and delivers their durable outbox commands.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ErrNoEligibleHost reports that no registered host satisfied the request.
// Callers must treat this as "do not dispatch yet", never as a task failure:
// a host may be temporarily offline or at capacity.
var ErrNoEligibleHost = errors.New("dispatch: no eligible execution host")

// RoutingStore supplies the durable facts used for host selection.
type RoutingStore interface {
	ListExecutionHosts(context.Context) ([]domain.ExecutionHost, error)
	GetExecutionHost(context.Context, domain.ExecutionHostID) (domain.ExecutionHost, []string, bool, error)
	ListProjectHostBindings(context.Context, domain.ProjectID) ([]domain.ProjectHostBinding, error)
	ListActiveSessionExecutionBindingsByHost(context.Context, domain.ExecutionHostID) ([]domain.SessionExecutionBinding, error)
}

// RouteRequest is the policy-relevant subset of a dispatch request.
type RouteRequest struct {
	ProjectID            domain.ProjectID
	TrustZone            domain.ExecutionTrustZone
	RequiredCapabilities []string
}

// Selection is the registered project clone and host chosen by the router.
type Selection struct {
	Host         domain.ExecutionHost
	Binding      domain.ProjectHostBinding
	Capabilities []string
	Active       int
}

// Router filters unsafe or unavailable hosts and ranks the survivors by the
// project's explicit priority, load ratio, capability specificity, and stable
// host ID.
type Router struct{ store RoutingStore }

// NewRouter constructs a Router over durable routing facts.
func NewRouter(store RoutingStore) *Router { return &Router{store: store} }

// Select returns the highest-ranked eligible host for req, or
// ErrNoEligibleHost when none qualifies. Selection is a read-only decision:
// it claims nothing, so the caller still owns the durable claim.
func (r *Router) Select(ctx context.Context, req RouteRequest) (Selection, error) {
	if req.ProjectID == "" || req.TrustZone == "" {
		return Selection{}, fmt.Errorf("%w: project and trust zone are required", ErrNoEligibleHost)
	}
	hosts, err := r.store.ListExecutionHosts(ctx)
	if err != nil {
		return Selection{}, err
	}
	bindings, err := r.store.ListProjectHostBindings(ctx, req.ProjectID)
	if err != nil {
		return Selection{}, err
	}
	hostByID := make(map[domain.ExecutionHostID]domain.ExecutionHost, len(hosts))
	for _, host := range hosts {
		hostByID[host.ID] = host
	}

	required := normalizedCapabilities(req.RequiredCapabilities)
	candidates := make([]Selection, 0, len(bindings))
	for _, binding := range bindings {
		host, ok := hostByID[binding.HostID]
		if !ok || !binding.Enabled || strings.TrimSpace(binding.HostRepoPath) == "" ||
			!host.Enabled || host.BackendType != domain.ExecutionBackendPaseo || host.MaxConcurrentSessions <= 0 ||
			!trustZoneMatches(req.TrustZone, host.TrustZone) || !hostOnline(host) {
			continue
		}
		_, capabilities, found, err := r.store.GetExecutionHost(ctx, host.ID)
		if err != nil {
			return Selection{}, err
		}
		if !found || !hasCapabilities(capabilities, required) {
			continue
		}
		active, err := r.store.ListActiveSessionExecutionBindingsByHost(ctx, host.ID)
		if err != nil {
			return Selection{}, err
		}
		if len(active) >= host.MaxConcurrentSessions {
			continue
		}
		candidates = append(candidates, Selection{
			Host: host, Binding: binding, Capabilities: normalizedCapabilities(capabilities), Active: len(active),
		})
	}
	if len(candidates) == 0 {
		return Selection{}, ErrNoEligibleHost
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.Binding.Priority != right.Binding.Priority {
			return left.Binding.Priority < right.Binding.Priority
		}
		// Compare utilization without floating point: left.Active/left.Max < right.Active/right.Max.
		leftLoad := left.Active * right.Host.MaxConcurrentSessions
		rightLoad := right.Active * left.Host.MaxConcurrentSessions
		if leftLoad != rightLoad {
			return leftLoad < rightLoad
		}
		leftExtra := len(left.Capabilities) - len(required)
		rightExtra := len(right.Capabilities) - len(required)
		if leftExtra != rightExtra {
			return leftExtra < rightExtra
		}
		return left.Host.ID < right.Host.ID
	})
	return candidates[0], nil
}

func hostOnline(host domain.ExecutionHost) bool {
	if host.LastSuccessfulProbeAt.IsZero() {
		return false
	}
	return host.LastFailedProbeAt.IsZero() || host.LastSuccessfulProbeAt.After(host.LastFailedProbeAt)
}

func trustZoneMatches(requested, actual domain.ExecutionTrustZone) bool {
	return actual == requested || actual == domain.ExecutionTrustZoneMixed
}

func normalizedCapabilities(capabilities []string) []string {
	set := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if capability != "" {
			set[capability] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for capability := range set {
		result = append(result, capability)
	}
	sort.Strings(result)
	return result
}

func hasCapabilities(available, required []string) bool {
	set := make(map[string]struct{}, len(available))
	for _, capability := range available {
		set[strings.TrimSpace(capability)] = struct{}{}
	}
	for _, capability := range required {
		if _, ok := set[capability]; !ok {
			return false
		}
	}
	return true
}
