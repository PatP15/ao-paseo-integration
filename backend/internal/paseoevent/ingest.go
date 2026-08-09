package paseoevent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// captureWindow is how many terminal lines one read asks for. It is a screen-line
// budget, not a report budget: the remote surface wraps at its column width, so a
// window this size holds several hundred frames and still costs one invocation.
const captureWindow = 2000

// Store is the durable surface ingest writes to.
//
// The method set is the safety boundary. There is nothing here that stops an
// agent, archives a workspace, answers a permission, merges, pushes, changes a
// host, or touches a retry budget, because a report may never cause any of those
// — the transport is forgeable, and Paseo's own activity read is cross-agent and
// unscoped, so one agent can replay another's reports. Anything irreversible
// stays gated on an AO record or a human decision.
type Store interface {
	GetLatestSessionBrief(context.Context, domain.SessionID) (domain.SessionBrief, bool, error)
	// RecordExecutionReport stores a report and its raw line, and reports
	// whether the caller still owes it an apply. A report already recorded and
	// already applied returns false.
	RecordExecutionReport(context.Context, domain.ExecutionReportEvent) (bool, error)
	MarkExecutionReportApplied(context.Context, domain.SessionID, string) error
	RecordExecutionObservation(context.Context, domain.ExecutionObservationEvent) (bool, error)
	OpenExecutionAgentQuestion(context.Context, domain.ExecutionAgentQuestion) (string, bool, error)
	RecordSessionCheckpoint(context.Context, domain.SessionCheckpoint) (bool, error)
	AdvanceExecutionEventCursor(context.Context, domain.SessionID, string, int64) error
}

// Lifecycle is the AO-owned reducer that turns a report into session state.
type Lifecycle interface {
	ApplyActivitySignal(context.Context, domain.SessionID, ports.ActivitySignal) error
}

// Source is one host's read-only report surface.
type Source interface {
	ports.ExecutionTerminalReader
	ports.ExecutionTranscriptReader
}

// SourceResolver returns the report surface for one host.
type SourceResolver interface {
	ResolveExecutionEventSource(domain.ExecutionHostID) (Source, bool)
}

// SourceResolverFunc adapts a plain lookup to SourceResolver.
type SourceResolverFunc func(domain.ExecutionHostID) (Source, bool)

// ResolveExecutionEventSource implements SourceResolver.
func (f SourceResolverFunc) ResolveExecutionEventSource(hostID domain.ExecutionHostID) (Source, bool) {
	return f(hostID)
}

// Result reports what one ingest pass found. Every counter is worth watching:
// Malformed rising means the emitter or the terminal is broken, and Foreign
// rising on a session with a valid contract means something is replaying frames
// that were minted for another launch.
type Result struct {
	Transport  domain.ExecutionEventTransport
	Applied    int
	Duplicate  int
	Rejected   int
	Malformed  int
	Foreign    int
	Incomplete int
	Gaps       int
}

// Ingestor reads agent-authored reports for bound remote sessions.
//
// Sequence state is in-memory and deliberately not durable. Losing it makes the
// next pass re-derive gaps from what it can see, which is the safe direction:
// both transports are full-replay safe and every durable write dedupes, so a
// restarted daemon re-reads rather than skips.
type Ingestor struct {
	store     Store
	lifecycle Lifecycle
	sources   SourceResolver
	logger    *slog.Logger
	now       func() time.Time
	window    int64

	// notifyQuestion, when set, announces a newly opened agent question to the
	// notification stream. Advisory by invariant: it identifies the question
	// and never triggers lifecycle action, and a failure inside it must not
	// fail the ingest, so it returns nothing.
	notifyQuestion func(ctx context.Context, sessionID domain.SessionID, workItemID, questionID, question string)

	highestSeq map[string]int64
}

// SetQuestionNotifier installs the advisory announcement hook. Injected by the
// daemon because building a notification needs the session's project, which
// this package deliberately does not read.
func (i *Ingestor) SetQuestionNotifier(notify func(ctx context.Context, sessionID domain.SessionID, workItemID, questionID, question string)) {
	i.notifyQuestion = notify
}

// NewIngestor constructs the ingestor. It performs no I/O.
func NewIngestor(store Store, lifecycle Lifecycle, sources SourceResolver, logger *slog.Logger) *Ingestor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Ingestor{
		store: store, lifecycle: lifecycle, sources: sources, logger: logger,
		now: time.Now, window: captureWindow, highestSeq: make(map[string]int64),
	}
}

// IngestSession reads one bound session's reports and applies them.
//
// A remote read failure is not a session fact: it returns the pass's counters
// and no error, leaving the cursor where it was so the next pass re-reads. It
// owns the sequence map and is not safe for concurrent use on one instance.
func (i *Ingestor) IngestSession(ctx context.Context, binding domain.SessionExecutionBinding) (Result, error) {
	var result Result
	if binding.TerminalID == "" && binding.ExternalAgentID == "" {
		// Dispatched but not launched: there is nothing to read yet.
		return result, nil
	}
	row, found, err := i.store.GetLatestSessionBrief(ctx, binding.SessionID)
	if err != nil {
		return result, fmt.Errorf("load report contract for session %s: %w", binding.SessionID, err)
	}
	if !found {
		i.logger.Debug("paseo reports: session has no brief; nothing to ingest", "session", binding.SessionID)
		return result, nil
	}
	brief, err := DecodeBrief(row.BriefJSON)
	if err != nil {
		// Without a readable contract the nonce is unknown, and ingesting under
		// an unknown nonce is exactly how AO would read somebody else's frames.
		i.logger.Warn("paseo reports: unreadable brief; ingest skipped",
			"session", binding.SessionID, "brief", row.ID, "err", err)
		return result, nil
	}
	if binding.LaunchID != "" && brief.LaunchID != binding.LaunchID {
		i.logger.Warn("paseo reports: stored contract belongs to another launch; ingest skipped",
			"session", binding.SessionID, "brief_launch", brief.LaunchID, "binding_launch", binding.LaunchID)
		return result, nil
	}
	// The contract's launch is the authority on which reports are current, so a
	// binding that has not recorded one yet does not widen what is accepted.
	launchID := binding.LaunchID
	if launchID == "" {
		launchID = brief.LaunchID
	}

	lines, transport, ok := i.read(ctx, binding)
	if !ok {
		return result, nil
	}
	result.Transport = transport

	decoded := Decode(row.ReportNonce, lines)
	result.Malformed, result.Foreign, result.Incomplete = decoded.Malformed, decoded.Foreign, decoded.Incomplete
	if decoded.Malformed > 0 {
		i.logger.Warn("paseo reports: malformed frames dropped",
			"session", binding.SessionID, "count", decoded.Malformed, "transport", transport)
	}

	var errs []error
	for _, payload := range decoded.Payloads {
		if err := i.ingestOne(ctx, binding, launchID, transport, payload, &result); err != nil {
			errs = append(errs, err)
		}
	}
	if transport == domain.ExecutionEventTerminal {
		if err := i.advance(ctx, binding, lines, decoded); err != nil {
			errs = append(errs, err)
		}
	}
	return result, errors.Join(errs...)
}

// read returns the lines to decode for one session, preferring the cursored
// transport. The uncursored transcript is the fallback rather than the default:
// it re-renders everything on every call, so it costs more and carries no
// guarantee that an old report is still present.
func (i *Ingestor) read(
	ctx context.Context,
	binding domain.SessionExecutionBinding,
) ([]string, domain.ExecutionEventTransport, bool) {
	source, ok := i.sources.ResolveExecutionEventSource(binding.HostID)
	if !ok {
		i.logger.Debug("paseo reports: no report source for host", "host", binding.HostID)
		return nil, "", false
	}
	if binding.TerminalID != "" {
		start := binding.TerminalLinesConsumed
		window, err := source.CaptureTerminal(ctx, binding.HostID, binding.TerminalID, start, start+i.window)
		if err != nil {
			i.logger.Debug("paseo reports: terminal capture failed; cursor unchanged",
				"session", binding.SessionID, "terminal", binding.TerminalID, "err", err)
			return nil, "", false
		}
		if window.TotalLines < start {
			// A monotonic cursor cannot move backwards, so the terminal AO was
			// reading is gone and this is a different one. Rewind rather than
			// read a foreign offset as if it were this session's history.
			i.logger.Warn("paseo reports: terminal cursor rewound; restarting from the beginning",
				"session", binding.SessionID, "consumed", start, "total", window.TotalLines)
			if err := i.store.AdvanceExecutionEventCursor(ctx, binding.SessionID, binding.TerminalID, 0); err != nil {
				i.logger.Warn("paseo reports: cursor rewind failed", "session", binding.SessionID, "err", err)
			}
			return nil, "", false
		}
		return window.Lines, domain.ExecutionEventTerminal, true
	}
	transcript, err := source.Transcript(ctx, binding.HostID, binding.ExternalAgentID)
	if err != nil {
		i.logger.Debug("paseo reports: transcript read failed; nothing ingested",
			"session", binding.SessionID, "agent", binding.ExternalAgentID, "err", err)
		return nil, "", false
	}
	return strings.Split(transcript, "\n"), domain.ExecutionEventSentinel, true
}

func (i *Ingestor) ingestOne(
	ctx context.Context,
	binding domain.SessionExecutionBinding,
	launchID string,
	transport domain.ExecutionEventTransport,
	payload DecodedPayload,
	result *Result,
) error {
	event, err := DecodeEvent(payload.Data)
	if err != nil {
		// A frame that carried the right nonce but not a report AO understands.
		result.Malformed++
		i.logger.Warn("paseo reports: report dropped", "session", binding.SessionID, "err", err)
		return nil
	}
	if event.SessionID != "" && event.SessionID != string(binding.SessionID) {
		result.Rejected++
		i.logger.Warn("paseo reports: report names another session; dropped",
			"session", binding.SessionID, "claimed", event.SessionID)
		return nil
	}
	if event.LaunchID != launchID {
		// A superseded launch's report. Applying it would let a killed attempt
		// keep talking about the current one.
		result.Rejected++
		return nil
	}

	if gap, from := i.checkSequence(event); gap {
		result.Gaps++
		if err := i.recordGap(ctx, binding, transport, event, from); err != nil {
			return err
		}
	}

	// Recorded, raw line and all, before anything is applied. A crash between
	// the two leaves the row unapplied, and the next pass applies it.
	needsApply, err := i.store.RecordExecutionReport(ctx, domain.ExecutionReportEvent{
		SessionID: binding.SessionID, HostID: binding.HostID, LaunchID: event.LaunchID,
		EventID: event.EventID, Seq: event.Seq, Type: event.Type, Transport: transport,
		PayloadJSON: string(payload.Data), RawLine: payload.Raw, ObservedAt: i.now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("record report %s for session %s: %w", event.EventID, binding.SessionID, err)
	}
	if !needsApply {
		result.Duplicate++
		return nil
	}
	if err := i.apply(ctx, binding, event); err != nil {
		return err
	}
	if err := i.store.MarkExecutionReportApplied(ctx, binding.SessionID, event.EventID); err != nil {
		return fmt.Errorf("mark report %s applied: %w", event.EventID, err)
	}
	result.Applied++
	return nil
}

// apply turns one report into AO facts. Every branch here is reversible by
// construction: a question a human can ignore, a progress row, or nothing at
// all. A result or a failure is recorded as the agent's account and nothing
// more — a process that stopped has not thereby finished its task, and whether
// to retry stays AO's decision.
func (i *Ingestor) apply(ctx context.Context, binding domain.SessionExecutionBinding, event Event) error {
	switch event.Type {
	case domain.ExecutionReportQuestion, domain.ExecutionReportBlocked:
		question, err := event.Question()
		if err != nil {
			return err
		}
		questionID, opened, err := i.store.OpenExecutionAgentQuestion(ctx, domain.ExecutionAgentQuestion{
			SessionID: binding.SessionID, WorkItemID: binding.WorkItemID, EventID: event.EventID,
			Question: question.Question, Recommendation: question.Recommendation,
			Options: question.Options, CreatedAt: i.now().UTC(),
		})
		if err != nil {
			return fmt.Errorf("open agent question for session %s: %w", binding.SessionID, err)
		}
		// Advisory only, and only on the transition to open: a replayed report
		// must not re-announce a question the human already saw.
		if opened && i.notifyQuestion != nil {
			i.notifyQuestion(ctx, binding.SessionID, binding.WorkItemID, questionID, question.Question)
		}
		// WaitingInput, not Blocked: AO answers this one with a message. Blocked
		// is reserved for a host-side permission request, which cannot be
		// answered with text at all.
		return i.signal(ctx, binding, domain.ActivityWaitingInput)
	case domain.ExecutionReportCheckpoint:
		checkpoint, err := event.Checkpoint()
		if err != nil {
			return err
		}
		if _, err := i.store.RecordSessionCheckpoint(ctx, domain.SessionCheckpoint{
			SessionID: binding.SessionID, Sequence: event.Seq, Summary: checkpoint.Summary,
			CompletedSteps: checkpoint.CompletedSteps, RemainingSteps: checkpoint.RemainingSteps,
			TestEvidence: checkpoint.TestEvidence, CommitSHA: checkpoint.CommitSHA,
			BranchPushed: checkpoint.BranchPushed, CreatedAt: i.now().UTC(),
		}); err != nil {
			return fmt.Errorf("record checkpoint for session %s: %w", binding.SessionID, err)
		}
		return nil
	default:
		// result, failure, follow_up_proposal: the durable report row is the
		// whole effect. Completion, retries, and new work items are AO's.
		return nil
	}
}

func (i *Ingestor) signal(ctx context.Context, binding domain.SessionExecutionBinding, state domain.ActivityState) error {
	if err := i.lifecycle.ApplyActivitySignal(ctx, binding.SessionID, ports.ActivitySignal{
		Valid: true, State: state, Timestamp: i.now().UTC(), LaunchID: binding.LaunchID,
	}); err != nil {
		return fmt.Errorf("apply activity for session %s: %w", binding.SessionID, err)
	}
	return nil
}

// checkSequence reports whether event skipped a sequence number for its launch.
// Detection only: a hole records that something was missed, and the missing
// report is asked for again rather than inferred.
func (i *Ingestor) checkSequence(event Event) (bool, int64) {
	key := event.LaunchID
	highest := i.highestSeq[key]
	gap := event.Seq > highest+1
	if event.Seq > highest {
		i.highestSeq[key] = event.Seq
	}
	return gap, highest
}

func (i *Ingestor) recordGap(
	ctx context.Context,
	binding domain.SessionExecutionBinding,
	transport domain.ExecutionEventTransport,
	event Event,
	from int64,
) error {
	payload, err := json.Marshal(map[string]any{
		"launchId": event.LaunchID, "afterSeq": from, "observedSeq": event.Seq,
	})
	if err != nil {
		return fmt.Errorf("marshal report gap: %w", err)
	}
	i.logger.Warn("paseo reports: report sequence gap",
		"session", binding.SessionID, "launch", event.LaunchID, "after", from, "observed", event.Seq)
	if _, err := i.store.RecordExecutionObservation(ctx, domain.ExecutionObservationEvent{
		SessionID: binding.SessionID, HostID: binding.HostID, LaunchID: event.LaunchID,
		Type: domain.ExecutionReportGap, Transport: transport,
		PayloadJSON: string(payload), ObservedAt: i.now().UTC(),
	}); err != nil {
		return fmt.Errorf("record report gap for session %s: %w", binding.SessionID, err)
	}
	return nil
}

// advance moves the durable cursor over the lines this pass consumed, stopping
// short of any frame group that is still missing chunks — a report split across
// a window boundary must be re-read whole, not half applied and half lost.
func (i *Ingestor) advance(
	ctx context.Context,
	binding domain.SessionExecutionBinding,
	lines []string,
	decoded DecodeResult,
) error {
	consumed := int64(len(lines))
	if decoded.FirstIncompleteLine >= 0 && int64(decoded.FirstIncompleteLine) < consumed {
		consumed = int64(decoded.FirstIncompleteLine)
	}
	if consumed <= 0 {
		return nil
	}
	next := binding.TerminalLinesConsumed + consumed
	if err := i.store.AdvanceExecutionEventCursor(ctx, binding.SessionID, binding.TerminalID, next); err != nil {
		return fmt.Errorf("advance report cursor for session %s: %w", binding.SessionID, err)
	}
	return nil
}
