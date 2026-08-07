package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func testBrief(version int, launch, nonce, supersedes string) domain.SessionBrief {
	return domain.SessionBrief{
		ID: "brief-" + launch, SessionID: "project-1", Version: version,
		SchemaVersion: "ao.run-brief.v1", BriefJSON: `{"launchId":"` + launch + `"}`,
		BriefSHA256: "sha-" + launch, ReportNonce: nonce,
		CreatedAt: time.Now().UTC().Truncate(time.Second), SupersedesBriefID: supersedes,
	}
}

func TestSessionBriefsAreImmutableAndVersioned(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first := testBrief(1, "launch-1", "a1b2c3d4e5f6", "")
	if err := s.SaveSessionBrief(ctx, first); err != nil {
		t.Fatalf("save first brief: %v", err)
	}
	// A second writer at the same version must lose rather than replace the
	// contract an agent may already be running under.
	clash := testBrief(1, "launch-2", "0f0f0f0f0f0f", "")
	if err := s.SaveSessionBrief(ctx, clash); err == nil {
		t.Fatal("want the duplicate version rejected")
	}

	second := testBrief(2, "launch-2", "0f0f0f0f0f0f", first.ID)
	if err := s.SaveSessionBrief(ctx, second); err != nil {
		t.Fatalf("save second brief: %v", err)
	}
	latest, found, err := s.GetLatestSessionBrief(ctx, "project-1")
	if err != nil || !found {
		t.Fatalf("get latest brief: %v found=%v", err, found)
	}
	if latest.Version != 2 || latest.ReportNonce != "0f0f0f0f0f0f" || latest.SupersedesBriefID != first.ID {
		t.Fatalf("latest = %#v", latest)
	}

	if _, found, err := s.GetLatestSessionBrief(ctx, "project-9"); err != nil || found {
		t.Fatalf("unknown session: found=%v err=%v", found, err)
	}
	for name, brief := range map[string]domain.SessionBrief{
		"no id":      {SessionID: "project-1", Version: 1, BriefJSON: "{}", ReportNonce: "a1b2c3d4e5f6"},
		"no nonce":   {ID: "b", SessionID: "project-1", Version: 1, BriefJSON: "{}"},
		"no version": {ID: "b", SessionID: "project-1", BriefJSON: "{}", ReportNonce: "a1b2c3d4e5f6"},
	} {
		if err := s.SaveSessionBrief(ctx, brief); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func reportEvent(eventID string, seq int64) domain.ExecutionReportEvent {
	return domain.ExecutionReportEvent{
		SessionID: "project-1", HostID: "worker-1", LaunchID: "launch-1", EventID: eventID, Seq: seq,
		Type: domain.ExecutionReportCheckpoint, Transport: domain.ExecutionEventTerminal,
		PayloadJSON: `{"summary":"ok"}`, RawLine: "AO_EVENT_a1b2c3d4e5f6 001/001 00000000 e30=;",
		ObservedAt: time.Now().UTC().Truncate(time.Second),
	}
}

func TestRecordExecutionReportDedupesOnTheEmitterMintedID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	needsApply, err := s.RecordExecutionReport(ctx, reportEvent("event-1", 1))
	if err != nil {
		t.Fatalf("record report: %v", err)
	}
	if !needsApply {
		t.Fatal("a newly recorded report still owes its apply")
	}
	// Both transports replay: the transcript has no cursor at all, and a
	// terminal window is re-read whenever a pass is interrupted. Re-ingesting an
	// unapplied report must still apply it exactly once.
	needsApply, err = s.RecordExecutionReport(ctx, reportEvent("event-1", 1))
	if err != nil {
		t.Fatalf("re-record report: %v", err)
	}
	if !needsApply {
		t.Fatal("a recorded but unapplied report is still owed an apply")
	}
	if err := s.MarkExecutionReportApplied(ctx, "project-1", "event-1"); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	needsApply, err = s.RecordExecutionReport(ctx, reportEvent("event-1", 1))
	if err != nil {
		t.Fatalf("record applied report: %v", err)
	}
	if needsApply {
		t.Fatal("an applied report must not be applied twice")
	}

	// A different report id is a different report, even with identical content.
	duplicateContent := reportEvent("event-2", 1)
	needsApply, err = s.RecordExecutionReport(ctx, duplicateContent)
	if err != nil || !needsApply {
		t.Fatalf("record second report: %v needsApply=%v", err, needsApply)
	}
	for name, event := range map[string]domain.ExecutionReportEvent{
		"no session":  {EventID: "e", Type: "checkpoint", Transport: "terminal", PayloadJSON: "{}"},
		"no event id": {SessionID: "project-1", Type: "checkpoint", Transport: "terminal", PayloadJSON: "{}"},
		"no payload":  {SessionID: "project-1", EventID: "e", Type: "checkpoint", Transport: "terminal"},
	} {
		if _, err := s.RecordExecutionReport(ctx, event); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func TestOpenExecutionAgentQuestionIsFiledOncePerReport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	question := domain.ExecutionAgentQuestion{
		SessionID: "project-1", EventID: "event-7", Question: "Preserve corrupt saves?",
		Recommendation: "Preserve", Options: []string{"reset", "preserve"},
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	opened, err := s.OpenExecutionAgentQuestion(ctx, question)
	if err != nil || !opened {
		t.Fatalf("open question: %v opened=%v", err, opened)
	}
	opened, err = s.OpenExecutionAgentQuestion(ctx, question)
	if err != nil {
		t.Fatalf("reopen question: %v", err)
	}
	if opened {
		t.Fatal("a replayed report must not file a second inbox entry")
	}
	// An agent question shares the inbox with host permission requests but not
	// their source: only a permission needs an explicit decision with a full
	// host-side request id.
	permissions, err := s.ListOpenExecutionPermissionQuestions(ctx)
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	if len(permissions) != 0 {
		t.Fatalf("agent question listed as a permission: %#v", permissions)
	}
}

func TestRecordSessionCheckpointIsIdempotentPerSequence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	checkpoint := domain.SessionCheckpoint{
		SessionID: "project-1", Sequence: 3, Summary: "schema written",
		CompletedSteps: []string{"migration"}, CommitSHA: "abc123", BranchPushed: true,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	recorded, err := s.RecordSessionCheckpoint(ctx, checkpoint)
	if err != nil || !recorded {
		t.Fatalf("record checkpoint: %v recorded=%v", err, recorded)
	}
	recorded, err = s.RecordSessionCheckpoint(ctx, checkpoint)
	if err != nil {
		t.Fatalf("re-record checkpoint: %v", err)
	}
	if recorded {
		t.Fatal("a replayed checkpoint must not duplicate progress")
	}
	for name, invalid := range map[string]domain.SessionCheckpoint{
		"no session":  {Sequence: 1, Summary: "x"},
		"no summary":  {SessionID: "project-1", Sequence: 1},
		"no sequence": {SessionID: "project-1", Summary: "x"},
	} {
		if _, err := s.RecordSessionCheckpoint(ctx, invalid); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func seedCursorBinding(t *testing.T, s *sqlite.Store, terminalID string, consumed int64) {
	t.Helper()
	seedObservedHost(t, s, "server-1")
	if err := s.UpsertSessionExecutionBinding(context.Background(), domain.SessionExecutionBinding{
		SessionID: "project-1", BackendType: domain.ExecutionBackendPaseo, HostID: "worker-1",
		TerminalID: terminalID, TerminalLinesConsumed: consumed,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
}

func TestAdvanceExecutionEventCursorOnlyMovesForwardOnItsOwnTerminal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedCursorBinding(t, s, "term-1", 10)

	if err := s.AdvanceExecutionEventCursor(ctx, "project-1", "term-1", 42); err != nil {
		t.Fatalf("advance cursor: %v", err)
	}
	binding, _, err := s.GetSessionExecutionBinding(ctx, "project-1")
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if binding.TerminalLinesConsumed != 42 {
		t.Fatalf("consumed = %d, want 42", binding.TerminalLinesConsumed)
	}

	// A stale pass must not rewind a cursor a later one already advanced.
	if err := s.AdvanceExecutionEventCursor(ctx, "project-1", "term-1", 20); err != nil {
		t.Fatalf("stale advance: %v", err)
	}
	// A cursor measured against a different terminal addresses nothing here.
	if err := s.AdvanceExecutionEventCursor(ctx, "project-1", "term-other", 99); err != nil {
		t.Fatalf("foreign advance: %v", err)
	}
	binding, _, err = s.GetSessionExecutionBinding(ctx, "project-1")
	if err != nil {
		t.Fatalf("get binding again: %v", err)
	}
	if binding.TerminalLinesConsumed != 42 || binding.TerminalID != "term-1" {
		t.Fatalf("binding = %#v, want the cursor untouched", binding)
	}

	// Zero is the one allowed rewind: it is how a caller that saw the cursor move
	// backwards restarts from the beginning of a replaced terminal.
	if err := s.AdvanceExecutionEventCursor(ctx, "project-1", "term-1", 0); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	binding, _, err = s.GetSessionExecutionBinding(ctx, "project-1")
	if err != nil {
		t.Fatalf("get binding after rewind: %v", err)
	}
	if binding.TerminalLinesConsumed != 0 {
		t.Fatalf("consumed = %d, want the rewind applied", binding.TerminalLinesConsumed)
	}
	for name, run := range map[string]func() error{
		"no terminal": func() error { return s.AdvanceExecutionEventCursor(ctx, "project-1", "", 5) },
		"negative consumed": func() error {
			return s.AdvanceExecutionEventCursor(ctx, "project-1", "term-1", -1)
		},
	} {
		if err := run(); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}
