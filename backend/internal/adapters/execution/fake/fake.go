// Package fake provides a deterministic, in-memory execution backend for
// tests. It models the complete execution lifecycle without a CLI, daemon, or
// network and can inject failures before or after state changes.
package fake

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Operation identifies one execution-port method for call inspection and
// failure injection.
type Operation string

// Execution operations supported by Backend.
const (
	OperationProvision   Operation = "provision"
	OperationLaunch      Operation = "launch"
	OperationStop        Operation = "stop"
	OperationAlive       Operation = "alive"
	OperationOutput      Operation = "output"
	OperationSendMessage Operation = "send_message"
	OperationStatus      Operation = "status"
	OperationListOwned   Operation = "list_owned"
	OperationInspect     Operation = "inspect"
)

var (
	// ErrHostUnreachable is returned by every host-bound operation while its
	// host is unreachable. In particular, Alive wraps this error instead of
	// returning (false, nil), which would be read as definitive death.
	ErrHostUnreachable = errors.New("fake execution host unreachable")
	// ErrNotFound is returned when a requested fake workspace or agent does
	// not exist.
	ErrNotFound = errors.New("fake execution resource not found")
	// ErrInvalidHandle is returned when a runtime handle is not namespaced as
	// <backend>:<host>/<agent>.
	ErrInvalidHandle = errors.New("fake execution runtime handle is invalid")
	// ErrInjected is used when a test requests a failure without supplying a
	// more specific error.
	ErrInjected = errors.New("fake execution injected failure")
)

// Call is an immutable record of one invocation. Exactly one request pointer
// is populated for Provision and Launch calls.
type Call struct {
	Operation   Operation
	HostID      domain.ExecutionHostID
	WorkspaceID domain.ExecutionWorkspaceID
	AgentID     domain.ExecutionAgentID
	Handle      ports.RuntimeHandle
	Lines       int
	Message     string
	Provision   *ports.ExecutionProvisionRequest
	Launch      *ports.ExecutionLaunchRequest
}

type plannedFailure struct {
	err           error
	afterMutation bool
}

type agentState struct {
	detail   domain.ExecutionAgentDetail
	alive    bool
	output   string
	messages []string
}

type listSnapshot struct {
	observations []domain.ExecutionAgentObservation
}

// Backend implements the execution backend, runtime, and observer ports with
// deterministic in-memory state. Its methods are safe for concurrent tests.
type Backend struct {
	mu sync.Mutex

	now             time.Time
	nextWorkspaceID int
	nextAgentID     int
	hosts           map[domain.ExecutionHostID]domain.ExecutionHostStatus
	workspaces      map[domain.ExecutionWorkspaceID]domain.ExecutionWorkspace
	agents          map[string]*agentState
	listSnapshots   map[domain.ExecutionHostID]listSnapshot
	failures        map[Operation][]plannedFailure
	calls           []Call
}

var _ ports.ExecutionBackend = (*Backend)(nil)
var _ ports.ExecutionRuntime = (*Backend)(nil)
var _ ports.ExecutionObserver = (*Backend)(nil)

// New returns an empty backend with a fixed clock and deterministic ID
// counters. Hosts are reachable by default until SetHostReachable says
// otherwise.
func New() *Backend {
	return &Backend{
		now:           time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		hosts:         make(map[domain.ExecutionHostID]domain.ExecutionHostStatus),
		workspaces:    make(map[domain.ExecutionWorkspaceID]domain.ExecutionWorkspace),
		agents:        make(map[string]*agentState),
		listSnapshots: make(map[domain.ExecutionHostID]listSnapshot),
		failures:      make(map[Operation][]plannedFailure),
	}
}

// Handle returns a namespaced runtime handle accepted by Backend.
func Handle(hostID domain.ExecutionHostID, agentID domain.ExecutionAgentID) ports.RuntimeHandle {
	return ports.RuntimeHandle{ID: "fake:" + string(hostID) + "/" + string(agentID)}
}

// SetTime replaces the fixed timestamp used by subsequently created fake
// resources and observations.
func (b *Backend) SetTime(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = now
}

// SetHostStatus installs the status returned for a host. A zero ObservedAt is
// filled from the fake clock.
func (b *Backend) SetHostStatus(status domain.ExecutionHostStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if status.ObservedAt.IsZero() {
		status.ObservedAt = b.now
	}
	b.hosts[status.HostID] = status
}

// SetHostReachable changes host reachability while preserving its other
// status fields. Unknown hosts start with deterministic status metadata.
func (b *Backend) SetHostReachable(hostID domain.ExecutionHostID, reachable bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	status := b.hostStatusLocked(hostID)
	status.Reachable = reachable
	status.ObservedAt = b.now
	b.hosts[hostID] = status
}

// SetListOwned installs an exact observer snapshot for a host. An empty slice
// deliberately models the ambiguous "reachable host, zero matches" result;
// repeated observations deliberately model a duplicate match.
func (b *Backend) SetListOwned(hostID domain.ExecutionHostID, observations []domain.ExecutionAgentObservation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listSnapshots[hostID] = listSnapshot{observations: cloneObservations(observations)}
}

// ClearListOwned removes an installed snapshot so ListOwned once again derives
// its result from the fake's current agent state.
func (b *Backend) ClearListOwned(hostID domain.ExecutionHostID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.listSnapshots, hostID)
}

// SetAgent stores an exact observer detail and liveness value. It is useful for
// arranging recovery states that do not start with Launch.
func (b *Backend) SetAgent(detail domain.ExecutionAgentDetail, alive bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	detail.PendingPermissions = clonePermissions(detail.PendingPermissions)
	b.agents[agentKey(detail.HostID, detail.AgentID)] = &agentState{detail: detail, alive: alive}
}

// SetOutput replaces the transcript returned for an existing agent.
func (b *Backend) SetOutput(hostID domain.ExecutionHostID, agentID domain.ExecutionAgentID, output string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	agent, ok := b.agents[agentKey(hostID, agentID)]
	if !ok {
		return resourceNotFound("agent", string(agentID))
	}
	agent.output = output
	return nil
}

// SetAlive replaces the liveness result for an existing agent.
func (b *Backend) SetAlive(hostID domain.ExecutionHostID, agentID domain.ExecutionAgentID, alive bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	agent, ok := b.agents[agentKey(hostID, agentID)]
	if !ok {
		return resourceNotFound("agent", string(agentID))
	}
	agent.alive = alive
	return nil
}

// Messages returns a copy of messages sent to an existing agent.
func (b *Backend) Messages(hostID domain.ExecutionHostID, agentID domain.ExecutionAgentID) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	agent := b.agents[agentKey(hostID, agentID)]
	if agent == nil {
		return nil
	}
	return append([]string(nil), agent.messages...)
}

// Calls returns a deep copy of the ordered invocation log.
func (b *Backend) Calls() []Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	got := make([]Call, len(b.calls))
	for i := range b.calls {
		got[i] = cloneCall(b.calls[i])
	}
	return got
}

// FailNext makes the next call to operation fail before changing fake state.
// A nil error is replaced with ErrInjected.
func (b *Backend) FailNext(operation Operation, err error) {
	b.queueFailure(operation, err, false)
}

// FailNextAfterMutation makes the next mutating call apply its state change and
// then return an error. For Launch this models a provider failure that leaves a
// labeled zombie agent behind for reconciliation and cleanup.
func (b *Backend) FailNextAfterMutation(operation Operation, err error) {
	b.queueFailure(operation, err, true)
}

// Provision materializes one deterministic fake workspace.
func (b *Backend) Provision(ctx context.Context, req ports.ExecutionProvisionRequest) (domain.ExecutionWorkspace, error) {
	failure, err := b.begin(ctx, Call{Operation: OperationProvision, HostID: req.HostID, Provision: &req})
	if err != nil {
		return domain.ExecutionWorkspace{}, err
	}
	b.mu.Lock()
	b.nextWorkspaceID++
	workspace := domain.ExecutionWorkspace{
		HostID: req.HostID, WorkspaceID: domain.ExecutionWorkspaceID(fmt.Sprintf("fake-workspace-%03d", b.nextWorkspaceID)),
		Title: req.WorkspaceTitle, RepoPath: req.RepoPath, Branch: req.Branch,
		Provider: req.Provider, Model: req.Model, Mode: req.Mode, CreatedAt: b.now,
	}
	b.workspaces[workspace.WorkspaceID] = workspace
	b.mu.Unlock()
	if failure.afterMutation {
		return domain.ExecutionWorkspace{}, failure.err
	}
	return workspace, nil
}

// Launch starts one deterministic fake agent in an existing workspace.
func (b *Backend) Launch(ctx context.Context, req ports.ExecutionLaunchRequest) (domain.ExecutionAgent, error) {
	failure, err := b.begin(ctx, Call{Operation: OperationLaunch, HostID: req.HostID, WorkspaceID: req.WorkspaceID, Launch: &req})
	if err != nil {
		return domain.ExecutionAgent{}, err
	}
	b.mu.Lock()
	workspace, ok := b.workspaces[req.WorkspaceID]
	if !ok || workspace.HostID != req.HostID {
		b.mu.Unlock()
		return domain.ExecutionAgent{}, resourceNotFound("workspace", string(req.WorkspaceID))
	}
	b.nextAgentID++
	agent := domain.ExecutionAgent{
		HostID: req.HostID, AgentID: domain.ExecutionAgentID(fmt.Sprintf("fake-agent-%03d", b.nextAgentID)),
		ParentAgentID: req.ParentAgentID, WorkspaceID: req.WorkspaceID, Branch: workspace.Branch,
		Cwd: workspace.RepoPath, Provider: req.Provider, Model: req.Model, Mode: req.Mode, LaunchedAt: b.now,
	}
	worktree := req.Labels["paseo.worktree"]
	if worktree == "" {
		worktree = string(req.WorkspaceID)
	}
	b.agents[agentKey(req.HostID, agent.AgentID)] = &agentState{
		alive: true,
		detail: domain.ExecutionAgentDetail{ExecutionAgentObservation: domain.ExecutionAgentObservation{
			HostID: req.HostID, AgentID: agent.AgentID, ParentAgentID: req.ParentAgentID,
			WorkspaceID: req.WorkspaceID, Status: domain.ExecutionAgentRunning, Worktree: worktree,
			Cwd: workspace.RepoPath, CreatedAt: b.now,
		}},
	}
	b.mu.Unlock()
	if failure.afterMutation {
		return domain.ExecutionAgent{}, failure.err
	}
	return agent, nil
}

// Stop marks an agent closed and not alive.
func (b *Backend) Stop(ctx context.Context, handle ports.RuntimeHandle) error {
	hostID, agentID, err := parseHandle(handle)
	if err != nil {
		return err
	}
	failure, err := b.begin(ctx, Call{Operation: OperationStop, HostID: hostID, AgentID: agentID, Handle: handle})
	if err != nil {
		return err
	}
	b.mu.Lock()
	agent, ok := b.agents[agentKey(hostID, agentID)]
	if !ok {
		b.mu.Unlock()
		return resourceNotFound("agent", string(agentID))
	}
	agent.alive = false
	agent.detail.Status = domain.ExecutionAgentClosed
	b.mu.Unlock()
	if failure.afterMutation {
		return failure.err
	}
	return nil
}

// Alive reports deterministic agent liveness. Host outages always return a
// non-nil ErrHostUnreachable error.
func (b *Backend) Alive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	hostID, agentID, err := parseHandle(handle)
	if err != nil {
		return false, err
	}
	_, err = b.begin(ctx, Call{Operation: OperationAlive, HostID: hostID, AgentID: agentID, Handle: handle})
	if err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	agent, ok := b.agents[agentKey(hostID, agentID)]
	if !ok {
		return false, nil
	}
	return agent.alive, nil
}

// Output returns at most the requested number of trailing transcript lines.
func (b *Backend) Output(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	hostID, agentID, err := parseHandle(handle)
	if err != nil {
		return "", err
	}
	if lines <= 0 {
		return "", fmt.Errorf("lines must be greater than zero")
	}
	_, err = b.begin(ctx, Call{Operation: OperationOutput, HostID: hostID, AgentID: agentID, Handle: handle, Lines: lines})
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	agent, ok := b.agents[agentKey(hostID, agentID)]
	if !ok {
		return "", resourceNotFound("agent", string(agentID))
	}
	return tailLines(agent.output, lines), nil
}

// SendMessage records a message for an existing agent.
func (b *Backend) SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error {
	hostID, agentID, err := parseHandle(handle)
	if err != nil {
		return err
	}
	failure, err := b.begin(ctx, Call{Operation: OperationSendMessage, HostID: hostID, AgentID: agentID, Handle: handle, Message: message})
	if err != nil {
		return err
	}
	b.mu.Lock()
	agent, ok := b.agents[agentKey(hostID, agentID)]
	if !ok {
		b.mu.Unlock()
		return resourceNotFound("agent", string(agentID))
	}
	agent.messages = append(agent.messages, message)
	b.mu.Unlock()
	if failure.afterMutation {
		return failure.err
	}
	return nil
}

// Status returns deterministic status for a reachable host.
func (b *Backend) Status(ctx context.Context, hostID domain.ExecutionHostID) (domain.ExecutionHostStatus, error) {
	_, err := b.begin(ctx, Call{Operation: OperationStatus, HostID: hostID})
	if err != nil {
		return domain.ExecutionHostStatus{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hostStatusLocked(hostID), nil
}

// ListOwned returns either an explicitly installed snapshot or a stable,
// agent-ID-sorted view of current fake state.
func (b *Backend) ListOwned(ctx context.Context, hostID domain.ExecutionHostID) ([]domain.ExecutionAgentObservation, error) {
	_, err := b.begin(ctx, Call{Operation: OperationListOwned, HostID: hostID})
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if snapshot, ok := b.listSnapshots[hostID]; ok {
		return cloneObservations(snapshot.observations), nil
	}
	result := make([]domain.ExecutionAgentObservation, 0)
	for _, agent := range b.agents {
		if agent.detail.HostID == hostID {
			result = append(result, agent.detail.ExecutionAgentObservation)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AgentID < result[j].AgentID })
	return result, nil
}

// Inspect returns a copy of one agent's full observer detail.
func (b *Backend) Inspect(ctx context.Context, hostID domain.ExecutionHostID, agentID domain.ExecutionAgentID) (domain.ExecutionAgentDetail, error) {
	_, err := b.begin(ctx, Call{Operation: OperationInspect, HostID: hostID, AgentID: agentID})
	if err != nil {
		return domain.ExecutionAgentDetail{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	agent, ok := b.agents[agentKey(hostID, agentID)]
	if !ok {
		return domain.ExecutionAgentDetail{}, resourceNotFound("agent", string(agentID))
	}
	detail := agent.detail
	detail.PendingPermissions = clonePermissions(detail.PendingPermissions)
	return detail, nil
}

func (b *Backend) queueFailure(operation Operation, err error, afterMutation bool) {
	if err == nil {
		err = ErrInjected
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures[operation] = append(b.failures[operation], plannedFailure{err: err, afterMutation: afterMutation})
}

func (b *Backend) begin(ctx context.Context, call Call) (plannedFailure, error) {
	if err := ctx.Err(); err != nil {
		return plannedFailure{}, err
	}
	b.mu.Lock()
	b.calls = append(b.calls, cloneCall(call))
	var failure plannedFailure
	if queue := b.failures[call.Operation]; len(queue) > 0 {
		failure = queue[0]
		b.failures[call.Operation] = queue[1:]
	}
	reachable := b.hostStatusLocked(call.HostID).Reachable
	b.mu.Unlock()
	if failure.err != nil && !failure.afterMutation {
		return failure, failure.err
	}
	if !reachable {
		return failure, fmt.Errorf("%w: %s", ErrHostUnreachable, call.HostID)
	}
	return failure, nil
}

func (b *Backend) hostStatusLocked(hostID domain.ExecutionHostID) domain.ExecutionHostStatus {
	if status, ok := b.hosts[hostID]; ok {
		return status
	}
	return domain.ExecutionHostStatus{
		HostID: hostID, Reachable: true, ServerID: "fake-server-" + string(hostID),
		Version: "fake", ObservedAt: b.now,
	}
}

func parseHandle(handle ports.RuntimeHandle) (domain.ExecutionHostID, domain.ExecutionAgentID, error) {
	_, rest, ok := strings.Cut(handle.ID, ":")
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidHandle, handle.ID)
	}
	host, agent, ok := strings.Cut(rest, "/")
	if !ok || host == "" || agent == "" {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidHandle, handle.ID)
	}
	return domain.ExecutionHostID(host), domain.ExecutionAgentID(agent), nil
}

func agentKey(hostID domain.ExecutionHostID, agentID domain.ExecutionAgentID) string {
	return string(hostID) + "\x00" + string(agentID)
}

func resourceNotFound(kind, id string) error {
	return fmt.Errorf("%w: %s %q", ErrNotFound, kind, id)
}

func cloneCall(call Call) Call {
	if call.Provision != nil {
		request := *call.Provision
		call.Provision = &request
	}
	if call.Launch != nil {
		request := *call.Launch
		request.Labels = cloneLabels(request.Labels)
		call.Launch = &request
	}
	return call
}

func cloneLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		result[key] = value
	}
	return result
}

func cloneObservations(observations []domain.ExecutionAgentObservation) []domain.ExecutionAgentObservation {
	return append([]domain.ExecutionAgentObservation(nil), observations...)
}

func clonePermissions(permissions []domain.ExecutionPermission) []domain.ExecutionPermission {
	return append([]domain.ExecutionPermission(nil), permissions...)
}

func tailLines(output string, count int) string {
	trimmed := strings.TrimRight(output, "\r\n")
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}
