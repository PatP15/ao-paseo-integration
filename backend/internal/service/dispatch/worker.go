package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/executionerror"
	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/runtimehandle"
)

const (
	defaultLease          = 30 * time.Second
	defaultBaseBackoff    = 5 * time.Second
	defaultMaxAttempts    = 5
	defaultMaxEscalations = 2
)

type commandStore interface {
	ClaimNextExecutionCommand(context.Context, time.Time, time.Time) (domain.ExecutionCommand, bool, error)
	RetryExecutionCommand(context.Context, domain.ExecutionCommand, time.Time, error) error
	FailExecutionCommand(context.Context, domain.ExecutionCommand, error) error
	EscalateExecutionAttempt(context.Context, domain.SessionID) error
	AcknowledgeExecutionStart(context.Context, string, domain.SessionID, string, string, time.Time) error
}

// BriefWriter commits the immutable instruction package for a launch. It is
// optional: without one the agent is launched on the bare work prompt and has no
// way to report back, which is the transport ladder's floor.
type BriefWriter interface {
	Ensure(context.Context, paseoevent.BriefRequest) (paseoevent.Brief, error)
}

// ReportTransportPreparer is an optional backend capability. A failure is
// surfaced to the daemon but never turns a successfully launched agent into a
// failed command: inspect-based observation is the transport ladder's floor.
type ReportTransportPreparer interface {
	PrepareReportTransport(context.Context, domain.SessionID, domain.ExecutionWorkspaceID, string, string) error
}

// BackendResolver returns the host-scoped execution backend used for a
// command. Host selection itself remains the router's responsibility.
type BackendResolver interface {
	ResolveExecutionBackend(domain.ExecutionHostID) (ports.ExecutionBackend, bool)
}

// BackendResolverFunc adapts a plain lookup function to BackendResolver so the
// daemon can wire a map or a closure without declaring a type.
type BackendResolverFunc func(domain.ExecutionHostID) (ports.ExecutionBackend, bool)

// ResolveExecutionBackend implements BackendResolver.
func (f BackendResolverFunc) ResolveExecutionBackend(hostID domain.ExecutionHostID) (ports.ExecutionBackend, bool) {
	return f(hostID)
}

type deliveryCheckpoint string

const (
	checkpointClaimed     deliveryCheckpoint = "claimed"
	checkpointProvisioned deliveryCheckpoint = "provisioned"
	checkpointLaunched    deliveryCheckpoint = "launched"
)

// Worker leases and delivers durable commands. checkpoint is an internal test
// seam that models process loss: a returned error deliberately leaves the row
// delivering until its lease expires.
type Worker struct {
	store          commandStore
	backends       BackendResolver
	briefs         BriefWriter
	now            func() time.Time
	lease          time.Duration
	baseBackoff    time.Duration
	maxAttempts    int
	maxEscalations int
	checkpoint     func(deliveryCheckpoint) error
}

// NewWorker constructs the outbox drain. It performs no I/O; call Drain to run
// one pass over due commands.
func NewWorker(store commandStore, backends BackendResolver) *Worker {
	return NewWorkerWithBriefs(store, backends, nil)
}

// NewWorkerWithBriefs constructs an outbox drain that commits each launch's
// brief before the launch happens.
func NewWorkerWithBriefs(store commandStore, backends BackendResolver, briefs BriefWriter) *Worker {
	return &Worker{
		store: store, backends: backends, briefs: briefs, now: time.Now, lease: defaultLease,
		baseBackoff: defaultBaseBackoff, maxAttempts: defaultMaxAttempts, maxEscalations: defaultMaxEscalations,
	}
}

// DeliverOne processes at most one due command. The bool reports whether a row
// was claimed; backend failures are persisted for retry and returned.
func (w *Worker) DeliverOne(ctx context.Context) (bool, error) {
	now := w.now().UTC()
	command, found, err := w.store.ClaimNextExecutionCommand(ctx, now, now.Add(w.lease))
	if err != nil || !found {
		return found, err
	}
	if err := w.atCheckpoint(checkpointClaimed); err != nil {
		return true, err
	}
	if command.Type != domain.ExecutionCommandStartAgent {
		err := fmt.Errorf("unsupported execution command type %q", command.Type)
		return true, errorsJoin(err, w.store.FailExecutionCommand(ctx, command, err))
	}
	backend, ok := w.backends.ResolveExecutionBackend(command.HostID)
	if !ok {
		err := fmt.Errorf("no execution backend for host %s", command.HostID)
		return true, w.retryOrFail(ctx, command, err)
	}
	payload, err := decodeStartPayload(command.PayloadJSON)
	if err != nil {
		return true, errorsJoin(err, w.store.FailExecutionCommand(ctx, command, err))
	}
	// The brief is committed before anything remote exists. It is what carries
	// the launch's report contract and its nonce, so it cannot be minted after
	// the agent has started: a crash in between would leave a running agent
	// reporting under a nonce AO never recorded. Re-delivery replays onto the
	// same brief rather than issuing a second one.
	prompt := payload.Prompt
	var launchBrief *paseoevent.Brief
	if w.briefs != nil {
		brief, err := w.briefs.Ensure(ctx, briefRequest(command, payload))
		if err != nil {
			return true, w.retryOrFail(ctx, command, err)
		}
		launchBrief = &brief
		prompt = brief.Prompt()
	}
	workspace, err := backend.Provision(ctx, ports.ExecutionProvisionRequest{
		SessionID: command.SessionID, ProjectID: payload.ProjectID, HostID: command.HostID,
		WorkspaceTitle: fmt.Sprintf("ao:%s:%d", command.SessionID, payload.Attempt),
		RepoPath:       payload.RepoPath, Branch: payload.Branch, BaseBranch: payload.BaseBranch,
		Provider: payload.Provider, Model: payload.Model, Mode: payload.Mode,
	})
	if err != nil {
		if errors.Is(err, executionerror.ErrProvisionOutcomeUnknown) {
			return true, w.escalateOrFail(ctx, command, payload, err)
		}
		return true, w.retryOrFail(ctx, command, err)
	}
	if err := w.atCheckpoint(checkpointProvisioned); err != nil {
		return true, err
	}
	var reportErr error
	if launchBrief != nil {
		if reporter, ok := backend.(ReportTransportPreparer); ok {
			reportErr = reporter.PrepareReportTransport(
				ctx, command.SessionID, workspace.WorkspaceID, launchBrief.LaunchID, launchBrief.ReportNonce,
			)
			if reportErr != nil {
				reportErr = fmt.Errorf("prepare report transport: %w", reportErr)
			}
		}
	}
	agent, err := backend.Launch(ctx, ports.ExecutionLaunchRequest{
		SessionID: command.SessionID, HostID: command.HostID, WorkspaceID: workspace.WorkspaceID,
		IntentID: payload.IntentID, Prompt: prompt,
		Labels: map[string]string{
			"ao.session": string(command.SessionID), "ao.attempt": strconv.Itoa(payload.Attempt),
			"ao.intent": string(payload.IntentID), "paseo.worktree": fmt.Sprintf("%s:%d", command.SessionID, payload.Attempt),
		},
		Provider: payload.Provider, Model: payload.Model, Mode: payload.Mode,
	})
	if err != nil {
		return true, w.retryOrFail(ctx, command, err)
	}
	if err := w.atCheckpoint(checkpointLaunched); err != nil {
		return true, err
	}
	handle, err := runtimehandle.New(domain.ExecutionBackendPaseo, command.HostID, agent.AgentID)
	if err != nil {
		return true, w.retryOrFail(ctx, command, err)
	}
	if err := w.store.AcknowledgeExecutionStart(ctx, command.ID, command.SessionID, handle.ID, payload.LaunchID, w.now().UTC()); err != nil {
		return true, err
	}
	// Report setup is deliberately best-effort. Returning it after acknowledge
	// makes the degradation visible in daemon logs without retrying start_agent
	// or sacrificing rung-2 status observation for a healthy remote agent.
	return true, reportErr
}

func (w *Worker) escalateOrFail(
	ctx context.Context,
	command domain.ExecutionCommand,
	payload domain.ExecutionStartPayload,
	deliveryErr error,
) error {
	if payload.Attempt >= 1+w.maxEscalations {
		err := fmt.Errorf("provision remained ambiguous after %d escalations: %w", w.maxEscalations, deliveryErr)
		return errorsJoin(err, w.store.FailExecutionCommand(ctx, command, err))
	}
	return errorsJoin(deliveryErr, w.store.EscalateExecutionAttempt(ctx, command.SessionID))
}

func (w *Worker) retryOrFail(ctx context.Context, command domain.ExecutionCommand, deliveryErr error) error {
	if command.AttemptCount >= w.maxAttempts {
		return errorsJoin(deliveryErr, w.store.FailExecutionCommand(ctx, command, deliveryErr))
	}
	delay := w.baseBackoff
	for attempt := 1; attempt < command.AttemptCount; attempt++ {
		if delay >= time.Hour/2 {
			delay = time.Hour
			break
		}
		delay *= 2
	}
	return errorsJoin(deliveryErr, w.store.RetryExecutionCommand(ctx, command, w.now().UTC().Add(delay), deliveryErr))
}

func (w *Worker) atCheckpoint(checkpoint deliveryCheckpoint) error {
	if w.checkpoint == nil {
		return nil
	}
	return w.checkpoint(checkpoint)
}

func briefRequest(command domain.ExecutionCommand, payload domain.ExecutionStartPayload) paseoevent.BriefRequest {
	return paseoevent.BriefRequest{
		SessionID: command.SessionID, ProjectID: payload.ProjectID, HostID: command.HostID,
		LaunchID: payload.LaunchID, Attempt: payload.Attempt, Branch: payload.Branch,
		BaseBranch: payload.BaseBranch, Provider: payload.Provider, Model: payload.Model,
		Mode: payload.Mode, Goal: payload.Prompt, Policy: paseoevent.DefaultPolicy(),
	}
}

func decodeStartPayload(raw string) (domain.ExecutionStartPayload, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var payload domain.ExecutionStartPayload
	if err := decoder.Decode(&payload); err != nil {
		return domain.ExecutionStartPayload{}, fmt.Errorf("decode start_agent payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return domain.ExecutionStartPayload{}, fmt.Errorf("decode start_agent payload: trailing JSON")
	}
	if payload.ProjectID == "" || payload.RepoPath == "" || payload.Branch == "" || payload.Provider == "" ||
		payload.Prompt == "" || payload.IntentID == "" || payload.Attempt < 1 || payload.LaunchID == "" {
		return domain.ExecutionStartPayload{}, fmt.Errorf("decode start_agent payload: required field missing")
	}
	return payload, nil
}

func errorsJoin(primary, secondary error) error {
	if secondary == nil {
		return primary
	}
	// Both errors are load-bearing: the primary explains why delivery failed and
	// the secondary why that failure could not be persisted. errors.Join keeps
	// both inspectable with errors.Is/As rather than flattening one to text.
	return errors.Join(primary, fmt.Errorf("persist delivery state: %w", secondary))
}
