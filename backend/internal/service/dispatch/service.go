package dispatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type dispatchStore interface {
	RoutingStore
	CreateExecutionDispatch(context.Context, domain.ExecutionDispatchSeed) (domain.ExecutionDispatch, error)
}

// Request describes one approved work-item attempt. Dispatch never calls an
// execution backend; it returns only after all AO-owned facts are committed.
type Request struct {
	WorkItemID           string
	ProjectID            domain.ProjectID
	TrustZone            domain.ExecutionTrustZone
	RequiredCapabilities []string
	IssueID              domain.IssueID
	Harness              domain.AgentHarness
	DisplayName          string
	Branch               string
	Provider             string
	Model                string
	Mode                 string
	// ThinkingOptionID must be an id provider discovery reported for this
	// host+provider+model; Dispatch refuses anything else before committing.
	ThinkingOptionID string
	// Features are provider feature toggles (e.g. Codex fast_mode). The pinned
	// Paseo CLI has no feature discovery or forwarding surface, so any entry is
	// refused; the field exists so the API shape is stable when the CLI grows one.
	Features map[string]bool
	// SkillPolicyOverrides names policy-gated skills the operator explicitly
	// enabled for this task. They change nothing about the launch — a skill
	// reference is prompt text — but each override is recorded in the audit
	// log alongside the dispatch (F8).
	SkillPolicyOverrides []string
	Prompt               string
}

// SettingsValidator checks dispatch settings against what provider discovery
// reports for the selected host. It is injected by the daemon because
// discovery talks to the network and this package deliberately does not.
type SettingsValidator func(ctx context.Context, hostID domain.ExecutionHostID, provider, model, thinkingOptionID string) error

// Service enqueues execution commands. Every command is persisted before any
// remote call, so a crash between enqueue and delivery replays rather than
// silently dropping the command.
type Service struct {
	store  dispatchStore
	router *Router
	now    func() time.Time
	newID  func() string
	// settingsValidator, when set, validates settings against provider
	// discovery for the selected host. When unset, a request carrying settings
	// is refused outright: forwarding an unvalidated id is never an option.
	settingsValidator SettingsValidator
	// defaultActor, when set, names the identity audit rows carry when the
	// caller supplied none.
	defaultActor func() string
}

// SetDefaultActor installs the identity recorded on dispatch audit facts.
func (s *Service) SetDefaultActor(identity func() string) {
	s.defaultActor = identity
}

// SetSettingsValidator installs discovery-backed settings validation. Set
// after construction so New stays a pure store wrapper.
func (s *Service) SetSettingsValidator(validator SettingsValidator) {
	s.settingsValidator = validator
}

// New constructs the dispatch Service.
func New(store dispatchStore) *Service {
	return &Service{store: store, router: NewRouter(store), now: time.Now, newID: uuid.NewString}
}

func newService(store dispatchStore, now func() time.Time, newID func() string) *Service {
	return &Service{store: store, router: NewRouter(store), now: now, newID: newID}
}

// Dispatch selects a host and atomically creates exactly one AO session,
// active implementer claim, execution binding, and start_agent command.
func (s *Service) Dispatch(ctx context.Context, req Request) (domain.ExecutionDispatch, error) {
	if err := validateRequest(req); err != nil {
		return domain.ExecutionDispatch{}, err
	}
	selection, err := s.router.Select(ctx, RouteRequest{
		ProjectID: req.ProjectID, TrustZone: req.TrustZone,
		RequiredCapabilities: req.RequiredCapabilities,
	})
	if err != nil {
		return domain.ExecutionDispatch{}, err
	}
	if req.ThinkingOptionID != "" {
		if s.settingsValidator == nil {
			return domain.ExecutionDispatch{}, fmt.Errorf(
				"dispatch: settings cannot be validated: this daemon has no provider discovery wired, and an unvalidated thinking option is never forwarded")
		}
		if err := s.settingsValidator(ctx, selection.Host.ID, req.Provider, req.Model, req.ThinkingOptionID); err != nil {
			return domain.ExecutionDispatch{}, err
		}
	}
	actor := ""
	if s.defaultActor != nil {
		actor = strings.TrimSpace(s.defaultActor())
	}
	now := s.now().UTC()
	return s.store.CreateExecutionDispatch(ctx, domain.ExecutionDispatchSeed{
		WorkItemID: req.WorkItemID, Actor: actor,
		Session: domain.SessionRecord{
			ProjectID: req.ProjectID, IssueID: req.IssueID, Kind: domain.KindWorker,
			Harness: req.Harness, DisplayName: req.DisplayName,
		},
		HostID: selection.Host.ID, BoundServerID: selection.Host.ServerID,
		RequestedTrustZone: req.TrustZone, RequiredCapabilities: normalizedCapabilities(req.RequiredCapabilities),
		HostRepoPath: selection.Binding.HostRepoPath, BaseBranch: selection.Binding.BaseBranch,
		Branch: req.Branch, Provider: req.Provider, Model: req.Model, Mode: req.Mode,
		ThinkingOptionID: req.ThinkingOptionID, SkillPolicyOverrides: normalizeOverrides(req.SkillPolicyOverrides),
		Prompt:   req.Prompt,
		IntentID: domain.ExecutionIntentID(s.newID()), Attempt: 1, DispatchGeneration: 1,
		LaunchID: s.newID(), CommandID: s.newID(), CreatedAt: now,
	})
}

func normalizeOverrides(overrides []string) []string {
	out := make([]string, 0, len(overrides))
	for _, name := range overrides {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func validateRequest(req Request) error {
	for name, value := range map[string]string{
		"work item": req.WorkItemID, "project": string(req.ProjectID), "trust zone": string(req.TrustZone),
		"harness": string(req.Harness), "branch": req.Branch, "provider": req.Provider, "prompt": req.Prompt,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("dispatch: %s is required", name)
		}
	}
	// Refused unconditionally, not silently dropped: a caller who asked for
	// fast_mode and got a normal launch would trust a setting that never
	// applied. Paseo 0.2.5's CLI has no feature discovery (`inspect_provider`
	// is MCP-only) and `run` has no feature flag, so there is nothing to
	// validate against and no way to forward one.
	if len(req.Features) > 0 {
		return fmt.Errorf("dispatch: provider features are not supported by the pinned Paseo CLI; remove settings.features")
	}
	return nil
}
