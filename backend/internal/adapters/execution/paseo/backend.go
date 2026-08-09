package paseo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/executionerror"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adoptionClockSkew = 5 * time.Minute

// ExecutionStore is the durable state needed to fence remote creation and bind
// Paseo-minted identifiers before later lifecycle operations may use them.
type ExecutionStore interface {
	GetExecutionHost(context.Context, domain.ExecutionHostID) (domain.ExecutionHost, []string, bool, error)
	GetSessionExecutionBinding(context.Context, domain.SessionID) (domain.SessionExecutionBinding, bool, error)
	UpsertSessionExecutionBinding(context.Context, domain.SessionExecutionBinding) error
}

type executionClient interface {
	Version() string
	Status(context.Context) (DaemonStatus, error)
	CreateWorkspace(context.Context, WorkspaceCreateRequest) (Workspace, error)
	ListWorkspaces(context.Context) ([]Workspace, error)
	Run(context.Context, RunRequest) (RunResult, error)
	ListProviders(context.Context) ([]Provider, error)
	ProviderModels(context.Context, string) ([]ProviderModel, error)
	ListSchedules(context.Context) ([]Schedule, error)
	DeleteSchedule(context.Context, string) (ScheduleDeleteResult, error)
	ListAgents(context.Context, string) ([]Agent, error)
	Inspect(context.Context, string) (AgentDetail, error)
	Stop(context.Context, string) error
	Delete(context.Context, string) error
	Logs(context.Context, string) (string, error)
	Send(context.Context, string, string) error
	CaptureTerminal(context.Context, string, int, int) (TerminalCapture, error)
}

// Backend implements AO's remote execution port using the pinned Paseo CLI
// client and the fork-owned execution binding store.
type Backend struct {
	client executionClient
	store  ExecutionStore
	now    func() time.Time
}

var _ ports.ExecutionBackend = (*Backend)(nil)
var _ ports.ExecutionRuntime = (*Backend)(nil)

// NewBackend returns a Paseo execution backend. The client is already pinned
// to a supported CLI version by NewClient.
func NewBackend(client *Client, store ExecutionStore) *Backend {
	return newBackend(client, store, time.Now)
}

func newBackend(client executionClient, store ExecutionStore, now func() time.Time) *Backend {
	return &Backend{client: client, store: store, now: now}
}

// Provision creates or recovers the single Paseo-owned worktree for an AO
// attempt and persists its ID before returning it to a caller.
func (b *Backend) Provision(ctx context.Context, req ports.ExecutionProvisionRequest) (domain.ExecutionWorkspace, error) {
	if err := validateProvisionRequest(req); err != nil {
		return domain.ExecutionWorkspace{}, err
	}
	status, err := b.guardHost(ctx, req.HostID, "")
	if err != nil {
		return domain.ExecutionWorkspace{}, err
	}

	binding, found, err := b.store.GetSessionExecutionBinding(ctx, req.SessionID)
	if err != nil {
		return domain.ExecutionWorkspace{}, fmt.Errorf("load execution binding: %w", err)
	}
	fresh := !found
	if fresh {
		attempt, attemptErr := workspaceAttempt(req.WorkspaceTitle, req.SessionID)
		if attemptErr != nil {
			return domain.ExecutionWorkspace{}, attemptErr
		}
		binding = domain.SessionExecutionBinding{
			SessionID: req.SessionID, BackendType: domain.ExecutionBackendPaseo,
			HostID: req.HostID, BoundServerID: status.ServerID,
			WorkspaceTitle: req.WorkspaceTitle, BranchName: req.Branch,
			Provider: req.Provider, Model: req.Model, Mode: req.Mode,
			Attempt: attempt, CreatedAt: b.now().UTC(),
		}
		if err := b.store.UpsertSessionExecutionBinding(ctx, binding); err != nil {
			return domain.ExecutionWorkspace{}, fmt.Errorf("seed execution binding: %w", err)
		}
	} else if err := validateProvisionBinding(binding, req, status.ServerID); err != nil {
		return domain.ExecutionWorkspace{}, err
	}

	if binding.ExternalWorkspaceID != "" {
		return workspaceFromBinding(binding, req), nil
	}

	workspaces, err := b.client.ListWorkspaces(ctx)
	if err != nil {
		return domain.ExecutionWorkspace{}, fmt.Errorf("list workspaces before create: %w", err)
	}
	workspace, matched, err := workspaceByTitle(workspaces, req.WorkspaceTitle)
	if err != nil {
		return domain.ExecutionWorkspace{}, err
	}
	if matched {
		return b.bindWorkspace(ctx, &binding, req, workspace)
	}
	// Safe to create: this binding carries no workspace id, and the remote list
	// above found nothing under our title.
	//
	// This used to refuse whenever a binding row already existed
	// (`fresh := !found`), which made the happy path unreachable — the dispatch
	// service commits the binding BEFORE enqueuing the command, precisely so a
	// crash between the two replays rather than vanishes, so a binding always
	// exists by the time Provision runs. Every dispatch therefore failed with
	// "outcome is unknown" and no workspace was ever created.
	//
	// The title is what makes creating safe rather than reckless: it is
	// "ao:<session>:<attempt>", unique per attempt, so a miss in the list means
	// this attempt has not created anything — not merely that some earlier
	// attempt's workspace was archived. An ambiguous outcome is still possible
	// if a create is in flight and not yet listable, and that is handled where
	// it actually arises: the post-create error path below re-lists once and
	// adopts an exact title match rather than retrying the create.
	_ = fresh

	workspace, createErr := b.client.CreateWorkspace(ctx, WorkspaceCreateRequest{
		RepoPath: req.RepoPath, Branch: req.Branch, BaseBranch: req.BaseBranch,
		WorktreeSlug: workspaceSlug(req), Title: req.WorkspaceTitle,
	})
	if createErr == nil {
		return b.bindWorkspace(ctx, &binding, req, workspace)
	}

	// A timeout or hard error is not proof that Paseo failed before creating the
	// worktree. Re-list once and bind an exact title match; never retry create.
	workspaces, reconcileErr := b.client.ListWorkspaces(ctx)
	if reconcileErr != nil {
		return domain.ExecutionWorkspace{}, errors.Join(
			executionerror.ErrProvisionOutcomeUnknown,
			createErr,
			fmt.Errorf("reconcile workspace create: %w", reconcileErr),
		)
	}
	workspace, matched, reconcileErr = workspaceByTitle(workspaces, req.WorkspaceTitle)
	if reconcileErr != nil {
		return domain.ExecutionWorkspace{}, errors.Join(executionerror.ErrProvisionOutcomeUnknown, createErr, reconcileErr)
	}
	if !matched {
		return domain.ExecutionWorkspace{}, errors.Join(executionerror.ErrProvisionOutcomeUnknown, createErr)
	}
	return b.bindWorkspace(ctx, &binding, req, workspace)
}

// Launch starts or reconciles one agent in a previously persisted workspace.
// Mandatory AO labels are persisted before `run`, making a repeated call a
// reconciliation operation rather than a blind second create.
func (b *Backend) Launch(ctx context.Context, req ports.ExecutionLaunchRequest) (domain.ExecutionAgent, error) {
	if err := validateLaunchRequest(req); err != nil {
		return domain.ExecutionAgent{}, err
	}
	status, err := b.guardHost(ctx, req.HostID, req.SessionID)
	if err != nil {
		return domain.ExecutionAgent{}, err
	}
	binding, found, err := b.store.GetSessionExecutionBinding(ctx, req.SessionID)
	if err != nil {
		return domain.ExecutionAgent{}, fmt.Errorf("load execution binding: %w", err)
	}
	if !found {
		return domain.ExecutionAgent{}, fmt.Errorf("execution binding for session %s does not exist", req.SessionID)
	}
	if err := validateLaunchBinding(binding, req, status.ServerID); err != nil {
		return domain.ExecutionAgent{}, err
	}
	if err := validateRequiredLabels(req, binding.Attempt); err != nil {
		return domain.ExecutionAgent{}, err
	}

	if binding.ExternalAgentID != "" {
		detail, err := b.client.Inspect(ctx, string(binding.ExternalAgentID))
		if err != nil {
			return domain.ExecutionAgent{}, fmt.Errorf("inspect bound Paseo agent: %w", err)
		}
		return b.verifiedAgent(binding, req, detail)
	}

	if len(binding.LabelsWritten) != 0 {
		return b.reconcilePriorLaunch(ctx, &binding, req)
	}

	binding.IntentID = req.IntentID
	binding.LabelsWritten = cloneLabels(req.Labels)
	binding.ExternalParentAgentID = req.ParentAgentID
	binding.Provider, binding.Model, binding.Mode = req.Provider, req.Model, req.Mode
	if err := b.store.UpsertSessionExecutionBinding(ctx, binding); err != nil {
		return domain.ExecutionAgent{}, fmt.Errorf("persist launch intent: %w", err)
	}

	result, runErr := b.client.Run(ctx, RunRequest{
		WorkspaceID: string(req.WorkspaceID), Provider: req.Provider, Model: req.Model,
		Mode: req.Mode, Thinking: req.ThinkingOptionID,
		Title: binding.WorkspaceTitle, Labels: sortedLabels(req.Labels), Prompt: req.Prompt,
	})
	if runErr != nil {
		return b.reconcileRunFailure(ctx, &binding, req, runErr)
	}
	if result.AgentID == "" {
		return domain.ExecutionAgent{}, fmt.Errorf("paseo run response omitted agent id")
	}

	binding.ExternalAgentID = domain.ExecutionAgentID(result.AgentID)
	if result.Cwd != "" && binding.HostWorkspacePath != "" && result.Cwd != binding.HostWorkspacePath {
		return domain.ExecutionAgent{}, fmt.Errorf("paseo run returned a different workspace path")
	}
	if err := b.store.UpsertSessionExecutionBinding(ctx, binding); err != nil {
		return domain.ExecutionAgent{}, fmt.Errorf("persist launched agent id: %w", err)
	}
	return agentFromRun(binding, req, result, b.now().UTC()), nil
}

// registeredHost is the cheap, purely local half of the host check: the row
// exists, is enabled, and is ours. Read paths stop here; mutating paths
// continue through guardHost, which additionally probes the live daemon.
func (b *Backend) registeredHost(ctx context.Context, hostID domain.ExecutionHostID) (domain.ExecutionHost, error) {
	host, _, found, err := b.store.GetExecutionHost(ctx, hostID)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("load execution host %s: %w", hostID, err)
	}
	if !found || !host.Enabled || host.BackendType != domain.ExecutionBackendPaseo {
		return domain.ExecutionHost{}, fmt.Errorf("execution host %s is not an enabled Paseo host", hostID)
	}
	return host, nil
}

func (b *Backend) guardHost(ctx context.Context, hostID domain.ExecutionHostID, sessionID domain.SessionID) (DaemonStatus, error) {
	host, err := b.registeredHost(ctx, hostID)
	if err != nil {
		return DaemonStatus{}, err
	}
	status, err := b.client.Status(ctx)
	if err != nil {
		return DaemonStatus{}, fmt.Errorf("probe execution host %s: %w", hostID, err)
	}
	if err := validateDaemonStatus(status); err != nil {
		return DaemonStatus{}, err
	}
	// Refuse only when Paseo actually SAID the daemon is desktop-managed.
	//
	// GET /api/status — the only surface that reaches a remote host — omits
	// desktopManaged entirely; `paseo status --json` reports it but cannot
	// target a remote daemon. So for any remote host this is simply unknown,
	// and treating unknown as "managed" would refuse every host while
	// dereferencing it unconditionally panics the daemon, which is how this was
	// found: one nil field took the whole process down mid-delivery.
	//
	// The hazard — AO driving the operator's own daemon — is caught below by
	// the ServerID comparison, which identifies the specific daemon rather than
	// a class of them and works over the surface that reaches a remote host.
	if managed, known := status.IsDesktopManaged(); known && managed {
		return DaemonStatus{}, fmt.Errorf("refusing desktop-managed Paseo host %s", hostID)
	}
	if status.Version != SupportedVersion || b.client.Version() != SupportedVersion {
		return DaemonStatus{}, &Error{Kind: ErrorUnsupportedVersion, Message: "unsupported Paseo host or CLI version"}
	}
	if host.ServerID != "" && host.ServerID != status.ServerID {
		return DaemonStatus{}, fmt.Errorf("paseo server identity changed for host %s", hostID)
	}
	if sessionID != "" {
		binding, bindingFound, loadErr := b.store.GetSessionExecutionBinding(ctx, sessionID)
		if loadErr != nil {
			return DaemonStatus{}, fmt.Errorf("load execution binding identity: %w", loadErr)
		}
		if bindingFound && binding.BoundServerID != "" && binding.BoundServerID != status.ServerID {
			return DaemonStatus{}, fmt.Errorf("paseo server identity changed for session %s", sessionID)
		}
	}
	return status, nil
}

func (b *Backend) bindWorkspace(ctx context.Context, binding *domain.SessionExecutionBinding, req ports.ExecutionProvisionRequest, workspace Workspace) (domain.ExecutionWorkspace, error) {
	if workspace.WorkspaceID == "" || workspace.Name != req.WorkspaceTitle || workspace.Cwd == "" || workspace.Isolation != "worktree" {
		return domain.ExecutionWorkspace{}, fmt.Errorf("paseo workspace did not match the requested worktree")
	}
	binding.ExternalWorkspaceID = domain.ExecutionWorkspaceID(workspace.WorkspaceID)
	binding.HostWorkspacePath = workspace.Cwd
	if err := b.store.UpsertSessionExecutionBinding(ctx, *binding); err != nil {
		return domain.ExecutionWorkspace{}, fmt.Errorf("persist workspace id: %w", err)
	}
	return domain.ExecutionWorkspace{
		HostID: req.HostID, WorkspaceID: binding.ExternalWorkspaceID, Title: workspace.Name,
		RepoPath: req.RepoPath, Branch: req.Branch, Provider: req.Provider, Model: req.Model,
		Mode: req.Mode, CreatedAt: b.now().UTC(),
	}, nil
}

func (b *Backend) reconcilePriorLaunch(ctx context.Context, binding *domain.SessionExecutionBinding, req ports.ExecutionLaunchRequest) (domain.ExecutionAgent, error) {
	candidates, err := b.verifiedCandidates(ctx, *binding, req)
	if err != nil {
		return domain.ExecutionAgent{}, err
	}
	if len(candidates) != 1 {
		return domain.ExecutionAgent{}, fmt.Errorf("previous Paseo launch outcome is unresolved: found %d verified candidates", len(candidates))
	}
	return b.adoptCandidate(ctx, binding, req, candidates[0])
}

func (b *Backend) reconcileRunFailure(ctx context.Context, binding *domain.SessionExecutionBinding, req ports.ExecutionLaunchRequest, runErr error) (domain.ExecutionAgent, error) {
	candidates, reconcileErr := b.verifiedCandidates(ctx, *binding, req)
	if IsKind(runErr, ErrorNetwork) {
		if reconcileErr != nil {
			return domain.ExecutionAgent{}, errors.Join(runErr, reconcileErr)
		}
		if len(candidates) != 1 {
			return domain.ExecutionAgent{}, errors.Join(runErr, fmt.Errorf("timed-out Paseo launch found %d verified candidates", len(candidates)))
		}
		return b.adoptCandidate(ctx, binding, req, candidates[0])
	}

	// Paseo can persist a labeled idle zombie before returning a hard prompt
	// failure. Sweep every verified candidate even though the run did not report
	// an agent ID; unverified matches are never touched.
	var sweepErrs []error
	for _, candidate := range candidates {
		if err := b.client.Stop(ctx, candidate.ID); err != nil {
			sweepErrs = append(sweepErrs, fmt.Errorf("stop possibly-created agent: %w", err))
		}
		if err := b.client.Delete(ctx, candidate.ID); err != nil {
			sweepErrs = append(sweepErrs, fmt.Errorf("delete possibly-created agent: %w", err))
		}
	}
	return domain.ExecutionAgent{}, errors.Join(runErr, reconcileErr, errors.Join(sweepErrs...))
}

func (b *Backend) verifiedCandidates(ctx context.Context, binding domain.SessionExecutionBinding, req ports.ExecutionLaunchRequest) ([]AgentDetail, error) {
	agents, err := b.client.ListAgents(ctx, "ao.intent="+string(req.IntentID))
	if err != nil {
		return nil, fmt.Errorf("list launch candidates: %w", err)
	}
	if len(agents) > 32 {
		return nil, fmt.Errorf("refusing unexpectedly broad Paseo launch candidate set")
	}
	verified := make([]AgentDetail, 0, len(agents))
	var invalid []error
	for _, agent := range agents {
		detail, inspectErr := b.client.Inspect(ctx, agent.ID)
		if inspectErr != nil {
			invalid = append(invalid, inspectErr)
			continue
		}
		if verifyErr := verifyAdoptionCandidate(binding, req, detail, b.now().UTC()); verifyErr != nil {
			invalid = append(invalid, verifyErr)
			continue
		}
		verified = append(verified, detail)
	}
	if len(agents) != len(verified) {
		return verified, fmt.Errorf("one or more Paseo launch candidates failed verification: %w", errors.Join(invalid...))
	}
	return verified, nil
}

func (b *Backend) adoptCandidate(ctx context.Context, binding *domain.SessionExecutionBinding, req ports.ExecutionLaunchRequest, detail AgentDetail) (domain.ExecutionAgent, error) {
	binding.ExternalAgentID = domain.ExecutionAgentID(detail.ID)
	if detail.ParentAgentID != nil {
		binding.ExternalParentAgentID = domain.ExecutionAgentID(*detail.ParentAgentID)
	}
	if err := b.store.UpsertSessionExecutionBinding(ctx, *binding); err != nil {
		return domain.ExecutionAgent{}, fmt.Errorf("persist reconciled agent id: %w", err)
	}
	return b.verifiedAgent(*binding, req, detail)
}

func (b *Backend) verifiedAgent(binding domain.SessionExecutionBinding, req ports.ExecutionLaunchRequest, detail AgentDetail) (domain.ExecutionAgent, error) {
	if err := verifyAdoptionCandidate(binding, req, detail, b.now().UTC()); err != nil {
		return domain.ExecutionAgent{}, err
	}
	parentID := req.ParentAgentID
	if detail.ParentAgentID != nil {
		parentID = domain.ExecutionAgentID(*detail.ParentAgentID)
	}
	return domain.ExecutionAgent{
		HostID: req.HostID, AgentID: domain.ExecutionAgentID(detail.ID), ParentAgentID: parentID,
		WorkspaceID: req.WorkspaceID, Branch: binding.BranchName, Cwd: detail.Cwd,
		Provider: detail.Provider, Model: detail.Model, Mode: detail.Mode, LaunchedAt: detail.CreatedAt,
	}, nil
}

func verifyAdoptionCandidate(binding domain.SessionExecutionBinding, req ports.ExecutionLaunchRequest, detail AgentDetail, now time.Time) error {
	if detail.ID == "" || detail.Worktree != req.Labels["paseo.worktree"] {
		return fmt.Errorf("paseo adoption candidate has the wrong worktree binding")
	}
	if binding.HostWorkspacePath == "" || detail.Cwd != binding.HostWorkspacePath {
		return fmt.Errorf("paseo adoption candidate has the wrong workspace path")
	}
	if detail.Archived || detail.ArchivedAt != nil {
		return fmt.Errorf("paseo adoption candidate is archived")
	}
	if detail.CreatedAt.IsZero() || detail.CreatedAt.After(now.Add(adoptionClockSkew)) ||
		(!binding.CreatedAt.IsZero() && detail.CreatedAt.Before(binding.CreatedAt.Add(-adoptionClockSkew))) {
		return fmt.Errorf("paseo adoption candidate creation time is implausible")
	}
	return nil
}

func validateProvisionRequest(req ports.ExecutionProvisionRequest) error {
	if req.SessionID == "" || req.HostID == "" || req.WorkspaceTitle == "" || req.RepoPath == "" ||
		req.Branch == "" || req.BaseBranch == "" || req.Provider == "" {
		return fmt.Errorf("invalid execution provision request: required field is empty")
	}
	return nil
}

func validateLaunchRequest(req ports.ExecutionLaunchRequest) error {
	if req.SessionID == "" || req.HostID == "" || req.WorkspaceID == "" || req.IntentID == "" ||
		req.Prompt == "" || req.Provider == "" {
		return fmt.Errorf("invalid execution launch request: required field is empty")
	}
	return nil
}

func validateRequiredLabels(req ports.ExecutionLaunchRequest, attempt int) error {
	for key, value := range req.Labels {
		if err := validateLabel(key + "=" + value); err != nil {
			return fmt.Errorf("invalid execution launch labels: %w", err)
		}
	}
	want := map[string]string{
		"ao.session":     string(req.SessionID),
		"ao.intent":      string(req.IntentID),
		"ao.attempt":     strconv.Itoa(attempt),
		"paseo.worktree": string(req.SessionID) + ":" + strconv.Itoa(attempt),
	}
	for key, value := range want {
		if req.Labels[key] != value {
			return fmt.Errorf("invalid execution launch labels: %s does not match request", key)
		}
	}
	return nil
}

func validateProvisionBinding(binding domain.SessionExecutionBinding, req ports.ExecutionProvisionRequest, serverID string) error {
	if binding.BackendType != domain.ExecutionBackendPaseo || binding.HostID != req.HostID || binding.SessionID != req.SessionID {
		return fmt.Errorf("existing execution binding does not match provision request")
	}
	if binding.BoundServerID != "" && binding.BoundServerID != serverID {
		return fmt.Errorf("paseo server identity changed for session %s", req.SessionID)
	}
	if binding.WorkspaceTitle != req.WorkspaceTitle || binding.BranchName != req.Branch {
		return fmt.Errorf("existing execution binding belongs to a different attempt")
	}
	return nil
}

func validateLaunchBinding(binding domain.SessionExecutionBinding, req ports.ExecutionLaunchRequest, serverID string) error {
	if binding.BackendType != domain.ExecutionBackendPaseo || binding.HostID != req.HostID || binding.BoundServerID != serverID {
		return fmt.Errorf("execution binding does not match the current Paseo host")
	}
	if binding.ExternalWorkspaceID == "" || binding.ExternalWorkspaceID != req.WorkspaceID {
		return fmt.Errorf("launch requires the persisted Paseo workspace id")
	}
	return nil
}

func workspaceByTitle(workspaces []Workspace, title string) (Workspace, bool, error) {
	var match Workspace
	count := 0
	for _, workspace := range workspaces {
		if workspace.Name == title {
			match = workspace
			count++
		}
	}
	if count > 1 {
		return Workspace{}, false, fmt.Errorf("multiple Paseo workspaces share AO title %q", title)
	}
	return match, count == 1, nil
}

func workspaceSlug(req ports.ExecutionProvisionRequest) string {
	slug := strings.ToLower(strings.TrimSpace(req.WorkspaceTitle))
	slug = strings.NewReplacer(":", "-", "/", "-", "_", "-").Replace(slug)
	return slug
}

func workspaceAttempt(title string, sessionID domain.SessionID) (int, error) {
	prefix := "ao:" + string(sessionID) + ":"
	if !strings.HasPrefix(title, prefix) {
		return 0, fmt.Errorf("invalid workspace title: expected %q prefix", prefix)
	}
	attempt, err := strconv.Atoi(strings.TrimPrefix(title, prefix))
	if err != nil || attempt < 1 {
		return 0, fmt.Errorf("invalid workspace title: attempt must be a positive integer")
	}
	return attempt, nil
}

func workspaceFromBinding(binding domain.SessionExecutionBinding, req ports.ExecutionProvisionRequest) domain.ExecutionWorkspace {
	return domain.ExecutionWorkspace{
		HostID: req.HostID, WorkspaceID: binding.ExternalWorkspaceID, Title: binding.WorkspaceTitle,
		RepoPath: req.RepoPath, Branch: binding.BranchName, Provider: binding.Provider,
		Model: binding.Model, Mode: binding.Mode, CreatedAt: binding.CreatedAt,
	}
}

func agentFromRun(binding domain.SessionExecutionBinding, req ports.ExecutionLaunchRequest, result RunResult, launchedAt time.Time) domain.ExecutionAgent {
	return domain.ExecutionAgent{
		HostID: req.HostID, AgentID: domain.ExecutionAgentID(result.AgentID), ParentAgentID: req.ParentAgentID,
		WorkspaceID: req.WorkspaceID, Branch: binding.BranchName, Cwd: binding.HostWorkspacePath,
		Provider: req.Provider, Model: req.Model, Mode: req.Mode, LaunchedAt: launchedAt,
	}
}

func sortedLabels(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+labels[key])
	}
	return result
}

func cloneLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		result[key] = value
	}
	return result
}
