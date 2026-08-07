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
	Prompt               string
}

// Service enqueues execution commands. Every command is persisted before any
// remote call, so a crash between enqueue and delivery replays rather than
// silently dropping the command.
type Service struct {
	store  dispatchStore
	router *Router
	now    func() time.Time
	newID  func() string
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
	now := s.now().UTC()
	return s.store.CreateExecutionDispatch(ctx, domain.ExecutionDispatchSeed{
		WorkItemID: req.WorkItemID,
		Session: domain.SessionRecord{
			ProjectID: req.ProjectID, IssueID: req.IssueID, Kind: domain.KindWorker,
			Harness: req.Harness, DisplayName: req.DisplayName,
		},
		HostID: selection.Host.ID, BoundServerID: selection.Host.ServerID,
		HostRepoPath: selection.Binding.HostRepoPath, BaseBranch: selection.Binding.BaseBranch,
		Branch: req.Branch, Provider: req.Provider, Model: req.Model, Mode: req.Mode, Prompt: req.Prompt,
		IntentID: domain.ExecutionIntentID(s.newID()), Attempt: 1, DispatchGeneration: 1,
		LaunchID: s.newID(), CommandID: s.newID(), CreatedAt: now,
	})
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
	return nil
}
