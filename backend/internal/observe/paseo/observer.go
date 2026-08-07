package paseo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe"
	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// commandLatency is the measured cost of one `paseo` invocation (spike
	// FINDINGS S10: 0.88-0.96 s), because the CLI is a shell shim that execs an
	// Electron helper. It is the unit the polling budget is denominated in.
	commandLatency = 900 * time.Millisecond
	// hotTick is the cadence for sessions whose agent is actively working.
	hotTick = 5 * time.Second
	// coldTick is the cadence for every other observed session. Idle, blocked,
	// and exited sessions change only when a human or AO acts, so polling them
	// at the hot rate would spend the whole budget learning nothing.
	coldTick = 30 * time.Second
	// sweepInterval is how often reconciliation lists a whole host. It costs one
	// invocation plus one per orphan candidate, so it runs far below the tick.
	sweepInterval = 5 * time.Minute
	// maxOrphanVerifications bounds the inspects one sweep may spend confirming
	// orphan candidates.
	maxOrphanVerifications = 8
)

// Store is the durable surface the observer writes facts to.
type Store interface {
	ListExecutionHosts(context.Context) ([]domain.ExecutionHost, error)
	ListActiveSessionExecutionBindingsByHost(context.Context, domain.ExecutionHostID) ([]domain.SessionExecutionBinding, error)
	RecordExecutionHostProbe(context.Context, domain.ExecutionHostProbe) error
	RecordExecutionObservation(context.Context, domain.ExecutionObservationEvent) (bool, error)
	OpenExecutionPermissionQuestion(context.Context, domain.ExecutionPermissionQuestion) (bool, error)
	RecordExecutionOrphan(context.Context, domain.ExecutionOrphan) (bool, error)
	MarkSessionExecutionObserved(context.Context, domain.SessionID, time.Time) error
}

// Lifecycle is the AO-owned reducer that turns an observation into session
// state. The observer never writes session rows itself.
type Lifecycle interface {
	ApplyActivitySignal(context.Context, domain.SessionID, ports.ActivitySignal) error
}

// ReportIngestor reads a session's agent-authored reports. It is optional: with
// no ingestor configured AO still derives almost every session fact from
// inspection alone, which is the floor the design deliberately keeps working.
type ReportIngestor interface {
	IngestSession(context.Context, domain.SessionExecutionBinding) (paseoevent.Result, error)
}

// ObserverResolver returns the read-only remote surface for one host.
type ObserverResolver interface {
	ResolveExecutionObserver(domain.ExecutionHostID) (ports.ExecutionObserver, bool)
}

// ObserverResolverFunc adapts a plain lookup to ObserverResolver.
type ObserverResolverFunc func(domain.ExecutionHostID) (ports.ExecutionObserver, bool)

// ResolveExecutionObserver implements ObserverResolver.
func (f ObserverResolverFunc) ResolveExecutionObserver(hostID domain.ExecutionHostID) (ports.ExecutionObserver, bool) {
	return f(hostID)
}

// Observer polls every registered remote host and records what it reads.
//
// Scheduling state (which session is due, when a host was last swept) is
// in-memory and deliberately not durable: losing it makes everything due at
// once, so a restarted daemon re-reads every session and ingests whatever it
// missed while it was down. The durable facts carry their own dedupe.
type Observer struct {
	store     Store
	lifecycle Lifecycle
	observers ObserverResolver
	reports   ReportIngestor
	logger    *slog.Logger
	now       func() time.Time

	hot, cold, sweepEvery, latency time.Duration

	due       map[domain.SessionID]time.Time
	lastSweep map[domain.ExecutionHostID]time.Time
}

// New constructs the observer. It performs no I/O; call Poll for one pass or
// Start for the supervised loop.
func New(store Store, lifecycle Lifecycle, observers ObserverResolver, logger *slog.Logger) *Observer {
	return NewWithReports(store, lifecycle, observers, nil, logger)
}

// NewWithReports constructs an observer that also ingests agent-authored
// reports. Reading reports costs a second remote invocation per session, so it
// halves how many sessions one host tick can cover; the budget below accounts
// for that rather than quietly overrunning the interval.
func NewWithReports(
	store Store,
	lifecycle Lifecycle,
	observers ObserverResolver,
	reports ReportIngestor,
	logger *slog.Logger,
) *Observer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Observer{
		store: store, lifecycle: lifecycle, observers: observers, reports: reports,
		logger: logger, now: time.Now,
		hot: hotTick, cold: coldTick, sweepEvery: sweepInterval, latency: commandLatency,
		due: make(map[domain.SessionID]time.Time), lastSweep: make(map[domain.ExecutionHostID]time.Time),
	}
}

// Start runs Poll immediately and then on every hot tick until ctx is done.
func (o *Observer) Start(ctx context.Context) <-chan struct{} {
	return observe.StartPollLoop(ctx, o.hot, o.Poll, o.logger, "paseo execution observer")
}

// Poll observes every enabled remote host once. It owns the observer's
// scheduling maps and is not safe for concurrent use; Start drives it from a
// single goroutine.
func (o *Observer) Poll(ctx context.Context) error {
	hosts, err := o.store.ListExecutionHosts(ctx)
	if err != nil {
		return fmt.Errorf("list execution hosts: %w", err)
	}
	var errs []error
	for _, host := range hosts {
		if !host.Enabled || host.BackendType == domain.ExecutionBackendLocal {
			continue
		}
		if err := o.pollHost(ctx, host); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (o *Observer) pollHost(ctx context.Context, host domain.ExecutionHost) error {
	remote, ok := o.observers.ResolveExecutionObserver(host.ID)
	if !ok {
		return fmt.Errorf("no execution observer for host %s", host.ID)
	}
	now := o.now().UTC()

	status, err := remote.Status(ctx, host.ID)
	// A backend that reports unreachability as a flag rather than an error is
	// treated the same way. Reading (Reachable:false, nil) as anything other
	// than an outage is the same mistake as reading (false, nil) from Alive as
	// death.
	if err != nil || !status.Reachable {
		// An unreachable host is a fact about the host. Return here, before any
		// session is touched: the sessions on it are still running and still
		// owned by AO.
		reason := "execution host reported itself unreachable"
		if err != nil {
			reason = err.Error()
		}
		o.logger.Debug("paseo observer: host probe failed; sessions left untouched",
			"host", host.ID, "err", reason)
		return o.store.RecordExecutionHostProbe(ctx, domain.ExecutionHostProbe{
			HostID: host.ID, Reachable: false, Error: reason, ObservedAt: now,
		})
	}
	if host.ServerID != "" && status.ServerID != host.ServerID {
		// A new server id means a different daemon: every agent id AO holds for
		// this host addresses something that no longer exists, so inspecting
		// them would map another daemon's state onto AO's sessions.
		detail := fmt.Sprintf("registered server %s, observed %s", host.ServerID, status.ServerID)
		if _, orphanErr := o.store.RecordExecutionOrphan(ctx, domain.ExecutionOrphan{
			Kind: domain.ExecutionOrphanServerIdentity, HostID: host.ID,
			Detail: detail, ObservedAt: now,
		}); orphanErr != nil {
			return orphanErr
		}
		return o.store.RecordExecutionHostProbe(ctx, domain.ExecutionHostProbe{
			HostID: host.ID, Reachable: false, Error: "paseo server identity changed: " + detail,
			ObservedAt: now,
		})
	}
	if err := o.store.RecordExecutionHostProbe(ctx, domain.ExecutionHostProbe{
		HostID: host.ID, ServerID: status.ServerID, Version: status.Version,
		Reachable: true, ObservedAt: now,
	}); err != nil {
		return err
	}

	bindings, err := o.store.ListActiveSessionExecutionBindingsByHost(ctx, host.ID)
	if err != nil {
		return fmt.Errorf("list bindings for host %s: %w", host.ID, err)
	}
	budget := o.inspectBudget()
	cost := o.sessionCost()
	var errs []error
	deferred := 0
	for _, binding := range bindings {
		if binding.ExternalAgentID == "" {
			// Dispatched but not yet launched: the command outbox still owns it.
			continue
		}
		if !o.isDue(binding.SessionID, now) {
			continue
		}
		if budget < cost {
			deferred++
			continue
		}
		budget -= cost
		if err := o.observeSession(ctx, remote, host, status, binding, now); err != nil {
			errs = append(errs, err)
		}
	}
	if deferred > 0 {
		o.logger.Warn("paseo observer: polling budget exhausted; sessions deferred to the next tick",
			"host", host.ID, "deferred", deferred, "budget", o.inspectBudget())
	}

	// Reconciliation runs on its own cadence and its own cap, so it neither
	// starves the session polls nor gets starved by them. On a host already at
	// its per-tick limit it waits for slack rather than pushing the tick over.
	if o.sweepDue(host.ID, now) {
		if deferred > 0 {
			o.logger.Warn("paseo observer: reconciliation sweep deferred; host is at its polling limit", "host", host.ID)
		} else if err := o.sweep(ctx, remote, host, bindings, status.ServerID, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// observeSession reads one bound agent and records what it says. Every remote
// failure returns nil: the session keeps its current facts and is retried.
func (o *Observer) observeSession(
	ctx context.Context,
	remote ports.ExecutionObserver,
	host domain.ExecutionHost,
	status domain.ExecutionHostStatus,
	binding domain.SessionExecutionBinding,
	now time.Time,
) error {
	if binding.BoundServerID != "" && binding.BoundServerID != status.ServerID {
		o.schedule(binding.SessionID, now, o.cold)
		_, err := o.store.RecordExecutionOrphan(ctx, domain.ExecutionOrphan{
			Kind: domain.ExecutionOrphanServerIdentity, HostID: host.ID, SessionID: binding.SessionID,
			AgentID: binding.ExternalAgentID, ObservedAt: now,
			Detail: fmt.Sprintf("session bound to server %s, observed %s", binding.BoundServerID, status.ServerID),
		})
		return err
	}

	detail, err := remote.Inspect(ctx, host.ID, binding.ExternalAgentID)
	if err != nil {
		// Not a death conclusion: a timeout, an archived agent, and a genuinely
		// missing one are indistinguishable over this CLI.
		o.logger.Debug("paseo observer: inspect failed; session facts unchanged",
			"session", binding.SessionID, "agent", binding.ExternalAgentID, "err", err)
		o.schedule(binding.SessionID, now, o.cold)
		return nil
	}
	facts, ok := DeriveSessionFacts(detail)
	if !ok {
		o.logger.Warn("paseo observer: unreadable remote status; session facts unchanged",
			"session", binding.SessionID, "status", detail.Status)
		o.schedule(binding.SessionID, now, o.cold)
		return nil
	}

	payload, err := encodeObservation(detail, facts)
	if err != nil {
		return err
	}
	// Recorded before it is applied, so a crash between the two replays the
	// observation rather than losing it. A repeat of an unchanged fact is
	// dropped by the store's content hash.
	if _, err := o.store.RecordExecutionObservation(ctx, domain.ExecutionObservationEvent{
		SessionID: binding.SessionID, HostID: host.ID, LaunchID: binding.LaunchID,
		Type: facts.EventType, Transport: domain.ExecutionEventInspect,
		PayloadJSON: payload, ObservedAt: now,
	}); err != nil {
		return err
	}

	var errs []error
	for _, permission := range detail.PendingPermissions {
		if err := o.openQuestion(ctx, binding, permission, now); err != nil {
			errs = append(errs, err)
		}
	}

	// The activity signal is applied on every observation, not only on a newly
	// recorded one: the event row is the audit trail, the session row is the
	// current reading, and they dedupe on different things.
	if err := o.lifecycle.ApplyActivitySignal(ctx, binding.SessionID, ports.ActivitySignal{
		Valid: true, State: facts.Activity, Timestamp: now, LaunchID: binding.LaunchID,
	}); err != nil {
		errs = append(errs, fmt.Errorf("apply activity for session %s: %w", binding.SessionID, err))
	}
	if err := o.store.MarkSessionExecutionObserved(ctx, binding.SessionID, now); err != nil {
		errs = append(errs, err)
	}
	// Reports are read after the inspection they accompany, and their failures
	// are logged rather than returned: an unreadable report channel says nothing
	// about the session, whose facts this tick already established.
	if o.reports != nil {
		if result, err := o.reports.IngestSession(ctx, binding); err != nil {
			o.logger.Warn("paseo observer: report ingest failed; session facts unchanged",
				"session", binding.SessionID, "err", err)
		} else if result.Applied > 0 || result.Malformed > 0 || result.Gaps > 0 {
			o.logger.Debug("paseo observer: reports ingested", "session", binding.SessionID,
				"applied", result.Applied, "malformed", result.Malformed, "gaps", result.Gaps)
		}
	}

	next := o.cold
	if facts.Activity == domain.ActivityActive {
		next = o.hot
	}
	o.schedule(binding.SessionID, now, next)
	return errors.Join(errs...)
}

func (o *Observer) openQuestion(
	ctx context.Context,
	binding domain.SessionExecutionBinding,
	permission domain.ExecutionPermission,
	now time.Time,
) error {
	if permission.ID == "" {
		// Paseo requires the full request id to record a decision, so a
		// permission without one cannot be answered. Surface it rather than
		// filing an inbox entry that can never be delivered.
		o.logger.Warn("paseo observer: pending permission has no request id; cannot be answered",
			"session", binding.SessionID, "tool", permission.ToolName)
		return nil
	}
	_, err := o.store.OpenExecutionPermissionQuestion(ctx, domain.ExecutionPermissionQuestion{
		SessionID: binding.SessionID, WorkItemID: binding.WorkItemID, ExternalID: permission.ID,
		ToolName: permission.ToolName, Question: permissionQuestion(permission), CreatedAt: now,
	})
	return err
}

func permissionQuestion(permission domain.ExecutionPermission) string {
	question := "The remote agent is requesting permission"
	if permission.ToolName != "" {
		question += " to use " + permission.ToolName
	}
	if reason := strings.TrimSpace(permission.Reason); reason != "" {
		question += ": " + reason
	}
	return question + "."
}

// sweep reconciles the host's agent list against AO's bindings. It only
// reports; nothing here stops, deletes, or archives anything.
func (o *Observer) sweep(
	ctx context.Context,
	remote ports.ExecutionObserver,
	host domain.ExecutionHost,
	bindings []domain.SessionExecutionBinding,
	serverID string,
	now time.Time,
) error {
	owned, err := remote.ListOwned(ctx, host.ID)
	if err != nil {
		o.logger.Debug("paseo observer: host listing failed; reconciliation deferred", "host", host.ID, "err", err)
		return nil
	}
	o.lastSweep[host.ID] = now

	boundAgents := make(map[domain.ExecutionAgentID]domain.SessionID, len(bindings))
	workspaces := make(map[string]domain.SessionID, len(bindings))
	for _, binding := range bindings {
		if binding.ExternalAgentID != "" {
			boundAgents[binding.ExternalAgentID] = binding.SessionID
		}
		if binding.HostWorkspacePath != "" {
			workspaces[binding.HostWorkspacePath] = binding.SessionID
		}
	}

	// A listing is never evidence on its own. It only nominates candidates,
	// which a second, independent probe has to confirm — an empty or truncated
	// `ls` is the same shape as a host with nothing on it, and this fork does
	// not conclude from an ambiguous empty result.
	var errs []error
	verifier := &orphanVerifier{remote: remote, hostID: host.ID, logger: o.logger}
	present := make(map[domain.ExecutionAgentID]struct{}, len(owned))
	for _, agent := range owned {
		present[agent.AgentID] = struct{}{}
		if _, bound := boundAgents[agent.AgentID]; bound {
			continue
		}
		sessionID, inAOWorkspace := workspaces[agent.Cwd]
		if !inAOWorkspace {
			// Another tenant's agent on a shared daemon. Not AO's to report.
			continue
		}
		detail, verified := verifier.verify(ctx, agent.AgentID)
		// The list shape carries no archive flag, so an unverified candidate may
		// simply be a finished attempt's archived agent.
		if !verified || detail.Archived {
			continue
		}
		if _, err := o.store.RecordExecutionOrphan(ctx, domain.ExecutionOrphan{
			Kind: domain.ExecutionOrphanAgent, HostID: host.ID, SessionID: sessionID,
			AgentID: agent.AgentID, WorkspacePath: agent.Cwd, ObservedAt: now,
			Detail: fmt.Sprintf("unbound %s agent in the workspace of session %s", detail.Status, sessionID),
		}); err != nil {
			errs = append(errs, err)
		}
	}

	for _, binding := range bindings {
		if binding.ExternalAgentID == "" {
			continue
		}
		if binding.BoundServerID != "" && binding.BoundServerID != serverID {
			// Its agent id belongs to a daemon that no longer answers here, so
			// its absence says nothing. Already surfaced as an identity change.
			continue
		}
		if _, found := present[binding.ExternalAgentID]; found {
			continue
		}
		// Absence from the listing alone would be read as death, and a broken
		// or truncated query produces exactly that. Report the agent gone only
		// when a direct inspect also cannot find it: two independent negatives
		// on a daemon that answered its status probe this tick.
		if _, verified := verifier.verify(ctx, binding.ExternalAgentID); verified {
			o.logger.Debug("paseo observer: host listing omitted a live agent; not reported as lost",
				"host", host.ID, "agent", binding.ExternalAgentID)
			continue
		}
		if verifier.exhausted {
			continue
		}
		if _, err := o.store.RecordExecutionOrphan(ctx, domain.ExecutionOrphan{
			Kind: domain.ExecutionOrphanMissingAgent, HostID: host.ID, SessionID: binding.SessionID,
			AgentID: binding.ExternalAgentID, WorkspacePath: binding.HostWorkspacePath, ObservedAt: now,
			Detail: "bound agent is absent from both the host listing and a direct inspect",
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// orphanVerifier re-reads an orphan candidate, spending at most
// maxOrphanVerifications inspects per sweep. When it runs out it says so rather
// than letting an unverified candidate through.
type orphanVerifier struct {
	remote    ports.ExecutionObserver
	hostID    domain.ExecutionHostID
	spent     int
	exhausted bool
	logger    *slog.Logger
}

func (v *orphanVerifier) verify(ctx context.Context, agentID domain.ExecutionAgentID) (domain.ExecutionAgentDetail, bool) {
	if v.spent >= maxOrphanVerifications {
		if !v.exhausted {
			v.exhausted = true
			v.logger.Warn("paseo observer: orphan verification budget spent; remaining candidates deferred",
				"host", v.hostID, "verified", v.spent)
		}
		return domain.ExecutionAgentDetail{}, false
	}
	v.spent++
	detail, err := v.remote.Inspect(ctx, v.hostID, agentID)
	if err != nil {
		return domain.ExecutionAgentDetail{}, false
	}
	return detail, true
}

// inspectBudget is how many remote invocations fit in one tick, minus the host
// status probe that every tick already spends.
func (o *Observer) inspectBudget() int {
	budget := int(o.hot/o.latency) - 1
	if budget < 1 {
		return 1
	}
	return budget
}

// sessionCost is how many invocations one due session spends: its inspect, plus
// a report read when reports are configured.
func (o *Observer) sessionCost() int {
	if o.reports == nil {
		return 1
	}
	return 2
}

func (o *Observer) isDue(sessionID domain.SessionID, now time.Time) bool {
	next, scheduled := o.due[sessionID]
	return !scheduled || !now.Before(next)
}

func (o *Observer) schedule(sessionID domain.SessionID, now time.Time, in time.Duration) {
	o.due[sessionID] = now.Add(in)
}

func (o *Observer) sweepDue(hostID domain.ExecutionHostID, now time.Time) bool {
	last, swept := o.lastSweep[hostID]
	return !swept || !now.Before(last.Add(o.sweepEvery))
}
