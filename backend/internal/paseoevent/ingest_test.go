package paseoevent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var ingestNow = time.Date(2026, time.March, 2, 8, 0, 0, 0, time.UTC)

type recordedReport struct {
	event   domain.ExecutionReportEvent
	applied bool
}

type memoryIngestStore struct {
	brief      domain.SessionBrief
	briefFound bool
	briefErr   error

	reports      map[string]*recordedReport
	observations []domain.ExecutionObservationEvent
	questions    []domain.ExecutionAgentQuestion
	checkpoints  []domain.SessionCheckpoint
	cursor       int64
	cursorCalls  []int64

	order      []string
	recordErr  error
	applyOrder []string
}

func newMemoryIngestStore() *memoryIngestStore {
	return &memoryIngestStore{reports: make(map[string]*recordedReport)}
}

func (s *memoryIngestStore) GetLatestSessionBrief(context.Context, domain.SessionID) (domain.SessionBrief, bool, error) {
	if s.briefErr != nil {
		return domain.SessionBrief{}, false, s.briefErr
	}
	return s.brief, s.briefFound, nil
}

func (s *memoryIngestStore) RecordExecutionReport(_ context.Context, event domain.ExecutionReportEvent) (bool, error) {
	if s.recordErr != nil {
		return false, s.recordErr
	}
	s.order = append(s.order, "record:"+event.EventID)
	existing, found := s.reports[event.EventID]
	if !found {
		s.reports[event.EventID] = &recordedReport{event: event}
		return true, nil
	}
	return !existing.applied, nil
}

func (s *memoryIngestStore) MarkExecutionReportApplied(_ context.Context, _ domain.SessionID, eventID string) error {
	s.order = append(s.order, "mark:"+eventID)
	if report, found := s.reports[eventID]; found {
		report.applied = true
	}
	return nil
}

func (s *memoryIngestStore) RecordExecutionObservation(_ context.Context, event domain.ExecutionObservationEvent) (bool, error) {
	s.observations = append(s.observations, event)
	return true, nil
}

func (s *memoryIngestStore) OpenExecutionAgentQuestion(_ context.Context, question domain.ExecutionAgentQuestion) (bool, error) {
	s.order = append(s.order, "question:"+question.EventID)
	s.applyOrder = append(s.applyOrder, "question:"+question.EventID)
	s.questions = append(s.questions, question)
	return true, nil
}

func (s *memoryIngestStore) RecordSessionCheckpoint(_ context.Context, checkpoint domain.SessionCheckpoint) (bool, error) {
	s.order = append(s.order, fmt.Sprintf("checkpoint:%d", checkpoint.Sequence))
	s.applyOrder = append(s.applyOrder, fmt.Sprintf("checkpoint:%d", checkpoint.Sequence))
	s.checkpoints = append(s.checkpoints, checkpoint)
	return true, nil
}

func (s *memoryIngestStore) AdvanceExecutionEventCursor(_ context.Context, _ domain.SessionID, _ string, consumed int64) error {
	s.cursor = consumed
	s.cursorCalls = append(s.cursorCalls, consumed)
	return nil
}

type recordedSignal struct {
	sessionID domain.SessionID
	signal    ports.ActivitySignal
}

type memoryLifecycle struct {
	signals []recordedSignal
}

func (l *memoryLifecycle) ApplyActivitySignal(_ context.Context, sessionID domain.SessionID, signal ports.ActivitySignal) error {
	l.signals = append(l.signals, recordedSignal{sessionID: sessionID, signal: signal})
	return nil
}

type fakeSource struct {
	window      domain.ExecutionEventWindow
	windowErr   error
	transcript  string
	scriptErr   error
	captures    []string
	transcripts int
}

func (s *fakeSource) CaptureTerminal(
	_ context.Context,
	_ domain.ExecutionHostID,
	terminalID string,
	start, end int64,
) (domain.ExecutionEventWindow, error) {
	s.captures = append(s.captures, fmt.Sprintf("%s:%d:%d", terminalID, start, end))
	if s.windowErr != nil {
		return domain.ExecutionEventWindow{}, s.windowErr
	}
	window := s.window
	if window.TerminalID == "" {
		window.TerminalID = terminalID
	}
	return window, nil
}

func (s *fakeSource) Transcript(context.Context, domain.ExecutionHostID, domain.ExecutionAgentID) (string, error) {
	s.transcripts++
	if s.scriptErr != nil {
		return "", s.scriptErr
	}
	return s.transcript, nil
}

func testIngestor(store Store, lifecycle Lifecycle, source Source) *Ingestor {
	ingestor := NewIngestor(store, lifecycle, SourceResolverFunc(
		func(domain.ExecutionHostID) (Source, bool) { return source, source != nil },
	), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ingestor.now = func() time.Time { return ingestNow }
	return ingestor
}

func testBinding() domain.SessionExecutionBinding {
	return domain.SessionExecutionBinding{
		SessionID: "project-1", WorkItemID: "work-1", HostID: "worker-1",
		ExternalAgentID: "agent-1", LaunchID: "launch-1", TerminalID: "term-1",
	}
}

func seedBrief(store *memoryIngestStore, nonce, launchID string) {
	brief := Brief{
		Schema: SchemaRunBrief, BriefID: "brief-1", SessionID: "project-1",
		LaunchID: launchID, ReportNonce: nonce, Reporting: BriefReporting{EventSchema: SchemaAgentEvent},
	}
	encoded, err := json.Marshal(brief)
	if err != nil {
		panic(err)
	}
	store.brief = domain.SessionBrief{
		ID: "brief-1", SessionID: "project-1", Version: 1, SchemaVersion: SchemaRunBrief,
		BriefJSON: string(encoded), ReportNonce: nonce, CreatedAt: ingestNow,
	}
	store.briefFound = true
}

func report(t *testing.T, reportType domain.ExecutionReportType, eventID string, seq int64, payload string) []string {
	t.Helper()
	raw := fmt.Sprintf(
		`{"schema":%q,"eventId":%q,"sessionId":"project-1","launchId":"launch-1","seq":%d,"type":%q,"payload":%s}`,
		SchemaAgentEvent, eventID, seq, reportType, payload)
	frames, err := EncodeFrames(testNonce, []byte(raw))
	if err != nil {
		t.Fatalf("encode report: %v", err)
	}
	return frames
}

func TestIngestRecordsTheRawLineBeforeApplyingIt(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	frames := report(t, domain.ExecutionReportCheckpoint, "e1", 1, `{"summary":"schema written","branchPushed":true}`)
	source := &fakeSource{window: domain.ExecutionEventWindow{Lines: frames, TotalLines: int64(len(frames))}}
	lifecycle := &memoryLifecycle{}

	result, err := testIngestor(store, lifecycle, source).IngestSession(context.Background(), testBinding())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Applied != 1 || result.Duplicate != 0 || result.Malformed != 0 {
		t.Fatalf("result = %#v", result)
	}
	// Record, then apply, then mark. A crash anywhere in that order replays; the
	// reverse order would lose the evidence of what AO acted on.
	want := []string{"record:e1", "checkpoint:1", "mark:e1"}
	if strings.Join(store.order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", store.order, want)
	}
	stored := store.reports["e1"].event
	if stored.RawLine != strings.Join(frames, "\n") {
		t.Fatalf("raw line = %q, want the frames as they arrived", stored.RawLine)
	}
	if stored.Transport != domain.ExecutionEventTerminal || stored.Seq != 1 || stored.LaunchID != "launch-1" {
		t.Fatalf("stored = %#v", stored)
	}
	if len(store.checkpoints) != 1 || store.checkpoints[0].Summary != "schema written" || !store.checkpoints[0].BranchPushed {
		t.Fatalf("checkpoints = %#v", store.checkpoints)
	}
	// A checkpoint is progress evidence. It says nothing about activity state and
	// nothing about completion.
	if len(lifecycle.signals) != 0 {
		t.Fatalf("signals = %#v, want none for a checkpoint", lifecycle.signals)
	}
	if store.cursor != int64(len(frames)) {
		t.Fatalf("cursor = %d, want %d", store.cursor, len(frames))
	}
}

func TestIngestAppliesAReportOnlyOnce(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	frames := report(t, domain.ExecutionReportCheckpoint, "e1", 1, `{"summary":"first"}`)
	source := &fakeSource{window: domain.ExecutionEventWindow{Lines: frames, TotalLines: int64(len(frames))}}
	ingestor := testIngestor(store, &memoryLifecycle{}, source)
	binding := testBinding()

	if _, err := ingestor.IngestSession(context.Background(), binding); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	// The uncursored transport replays everything on every poll, so a second
	// pass over the same bytes must be free rather than duplicated.
	result, err := ingestor.IngestSession(context.Background(), binding)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if result.Duplicate != 1 || result.Applied != 0 {
		t.Fatalf("result = %#v, want the replay recognised", result)
	}
	if len(store.checkpoints) != 1 {
		t.Fatalf("checkpoints = %d, want 1", len(store.checkpoints))
	}
}

func TestIngestReappliesAReportRecordedButNeverApplied(t *testing.T) {
	// Models a crash between the durable record and the apply: the row exists
	// with applied = 0, and the next pass still owes it.
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	frames := report(t, domain.ExecutionReportCheckpoint, "e1", 1, `{"summary":"first"}`)
	store.reports["e1"] = &recordedReport{applied: false}
	source := &fakeSource{window: domain.ExecutionEventWindow{Lines: frames, TotalLines: int64(len(frames))}}

	result, err := testIngestor(store, &memoryLifecycle{}, source).IngestSession(context.Background(), testBinding())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Applied != 1 {
		t.Fatalf("result = %#v, want the unapplied report applied", result)
	}
	if len(store.checkpoints) != 1 {
		t.Fatalf("checkpoints = %d, want 1", len(store.checkpoints))
	}
}

func TestIngestMapsAQuestionToWaitingInput(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	frames := report(t, domain.ExecutionReportQuestion, "e7", 1,
		`{"question":"Preserve corrupt saves?","recommendation":"Preserve","options":["reset","preserve"]}`)
	source := &fakeSource{window: domain.ExecutionEventWindow{Lines: frames, TotalLines: int64(len(frames))}}
	lifecycle := &memoryLifecycle{}

	if _, err := testIngestor(store, lifecycle, source).IngestSession(context.Background(), testBinding()); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(store.questions) != 1 {
		t.Fatalf("questions = %#v", store.questions)
	}
	question := store.questions[0]
	if question.EventID != "e7" || question.WorkItemID != "work-1" || len(question.Options) != 2 {
		t.Fatalf("question = %#v", question)
	}
	if len(lifecycle.signals) != 1 || lifecycle.signals[0].sessionID != "project-1" {
		t.Fatalf("signals = %#v", lifecycle.signals)
	}
	signal := lifecycle.signals[0].signal
	// WaitingInput, not Blocked: AO can answer this one with a message. Blocked
	// is reserved for a host-side permission, which text cannot answer at all.
	if signal.State != domain.ActivityWaitingInput || signal.LaunchID != "launch-1" || !signal.Valid {
		t.Fatalf("signal = %#v", signal)
	}
	// The question is filed before the state moves, so a crash cannot leave a
	// session waiting on a question nobody can see.
	if store.order[1] != "question:e7" {
		t.Fatalf("order = %v", store.order)
	}
}

func TestIngestTreatsAResultAsEvidenceOnly(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	frames := report(t, domain.ExecutionReportResult, "e9", 1, `{"summary":"done","evidence":["go test ./... exit 0"]}`)
	frames = append(frames, report(t, domain.ExecutionReportFollowUp, "e10", 2, `{"title":"Extract the loader"}`)...)
	source := &fakeSource{window: domain.ExecutionEventWindow{Lines: frames, TotalLines: int64(len(frames))}}
	lifecycle := &memoryLifecycle{}

	result, err := testIngestor(store, lifecycle, source).IngestSession(context.Background(), testBinding())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Applied != 2 {
		t.Fatalf("result = %#v", result)
	}
	// An agent saying it finished is not completion, and a proposal is not work.
	// The durable rows are the whole effect.
	if len(store.checkpoints) != 0 || len(store.questions) != 0 || len(lifecycle.signals) != 0 {
		t.Fatalf("a result or a proposal changed session state: %#v %#v %#v",
			store.checkpoints, store.questions, lifecycle.signals)
	}
	if len(store.applyOrder) != 0 {
		t.Fatalf("apply wrote %v", store.applyOrder)
	}
}

func TestIngestDetectsASequenceGapWithoutReconstructingIt(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	frames := report(t, domain.ExecutionReportCheckpoint, "e1", 1, `{"summary":"one"}`)
	frames = append(frames, report(t, domain.ExecutionReportCheckpoint, "e3", 3, `{"summary":"three"}`)...)
	source := &fakeSource{window: domain.ExecutionEventWindow{Lines: frames, TotalLines: int64(len(frames))}}

	result, err := testIngestor(store, &memoryLifecycle{}, source).IngestSession(context.Background(), testBinding())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Gaps != 1 {
		t.Fatalf("gaps = %d, want 1", result.Gaps)
	}
	if result.Applied != 2 {
		t.Fatalf("applied = %d: a gap does not stop the reports that did arrive", result.Applied)
	}
	if len(store.observations) != 1 {
		t.Fatalf("observations = %#v", store.observations)
	}
	gap := store.observations[0]
	if gap.Type != domain.ExecutionReportGap || gap.LaunchID != "launch-1" {
		t.Fatalf("gap = %#v", gap)
	}
	if !strings.Contains(gap.PayloadJSON, `"afterSeq":1`) || !strings.Contains(gap.PayloadJSON, `"observedSeq":3`) {
		t.Fatalf("gap payload = %s, want the hole recorded rather than filled", gap.PayloadJSON)
	}
	// Two checkpoints, at their own sequences. Nothing invented for seq 2.
	if len(store.checkpoints) != 2 || store.checkpoints[1].Sequence != 3 {
		t.Fatalf("checkpoints = %#v", store.checkpoints)
	}
}

func TestIngestDropsReportsFromAnotherLaunchOrSession(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	otherLaunch := fmt.Sprintf(
		`{"schema":%q,"eventId":"e2","sessionId":"project-1","launchId":"launch-0","seq":1,`+
			`"type":"checkpoint","payload":{"summary":"stale"}}`, SchemaAgentEvent)
	otherSession := fmt.Sprintf(
		`{"schema":%q,"eventId":"e3","sessionId":"project-9","launchId":"launch-1","seq":1,`+
			`"type":"checkpoint","payload":{"summary":"not mine"}}`, SchemaAgentEvent)
	var lines []string
	for _, raw := range []string{otherLaunch, otherSession} {
		frames, err := EncodeFrames(testNonce, []byte(raw))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		lines = append(lines, frames...)
	}
	source := &fakeSource{window: domain.ExecutionEventWindow{Lines: lines, TotalLines: int64(len(lines))}}

	result, err := testIngestor(store, &memoryLifecycle{}, source).IngestSession(context.Background(), testBinding())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Rejected != 2 || result.Applied != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(store.reports) != 0 || len(store.checkpoints) != 0 {
		t.Fatalf("a superseded launch or another session was applied: %#v %#v", store.reports, store.checkpoints)
	}
}

func TestIngestCountsMalformedFramesAndKeepsGoing(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	good := report(t, domain.ExecutionReportCheckpoint, "e1", 1, `{"summary":"ok"}`)
	notAReport, err := EncodeFrames(testNonce, []byte(`{"schema":"ao.agent-event.v1","type":"kill"}`))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	lines := append([]string{strings.TrimSuffix(good[0], terminator)}, notAReport...)
	lines = append(lines, good...)
	source := &fakeSource{window: domain.ExecutionEventWindow{Lines: lines, TotalLines: int64(len(lines))}}

	result, err := testIngestor(store, &memoryLifecycle{}, source).IngestSession(context.Background(), testBinding())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// One line truncated, one well-framed payload that is not a valid report.
	if result.Malformed != 2 {
		t.Fatalf("malformed = %d, want 2 (%#v)", result.Malformed, result)
	}
	if result.Applied != 1 || len(store.checkpoints) != 1 {
		t.Fatalf("a malformed neighbour blocked a valid report: %#v %#v", result, store.checkpoints)
	}
}

func TestIngestLeavesTheCursorAtAnIncompleteReport(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	frames := report(t, domain.ExecutionReportCheckpoint, "e1", 1,
		`{"summary":"`+strings.Repeat("s", 400)+`"}`)
	if len(frames) < 4 {
		t.Fatalf("want a multi-frame report, got %d", len(frames))
	}
	partial := append([]string{"noise"}, frames[:2]...)
	source := &fakeSource{window: domain.ExecutionEventWindow{Lines: partial, TotalLines: int64(len(partial))}}
	ingestor := testIngestor(store, &memoryLifecycle{}, source)
	binding := testBinding()

	result, err := ingestor.IngestSession(context.Background(), binding)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if result.Applied != 0 || result.Incomplete != 1 {
		t.Fatalf("result = %#v", result)
	}
	// Only the noise line is consumed; the report's first line stays unread so
	// the next window can see the whole thing.
	if store.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", store.cursor)
	}

	binding.TerminalLinesConsumed = store.cursor
	source.window = domain.ExecutionEventWindow{Lines: frames, TotalLines: int64(len(frames) + 1)}
	result, err = ingestor.IngestSession(context.Background(), binding)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if result.Applied != 1 {
		t.Fatalf("result = %#v, want the report completed on the next pass", result)
	}
	if store.cursor != 1+int64(len(frames)) {
		t.Fatalf("cursor = %d, want %d", store.cursor, 1+len(frames))
	}
}

func TestIngestRewindsWhenTheTerminalWasReplaced(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	source := &fakeSource{window: domain.ExecutionEventWindow{TotalLines: 4}}
	binding := testBinding()
	binding.TerminalLinesConsumed = 900

	result, err := testIngestor(store, &memoryLifecycle{}, source).IngestSession(context.Background(), binding)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Applied != 0 {
		t.Fatalf("result = %#v", result)
	}
	// A monotonic cursor cannot move backwards, so this is a different terminal.
	// Reading its lines at the old offset would attribute another terminal's
	// bytes to this session.
	if len(store.cursorCalls) != 1 || store.cursorCalls[0] != 0 {
		t.Fatalf("cursor calls = %v, want a single rewind to 0", store.cursorCalls)
	}
}

func TestIngestTreatsAReadFailureAsNoNews(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	source := &fakeSource{windowErr: errors.New("host unreachable")}
	binding := testBinding()
	binding.TerminalLinesConsumed = 12

	result, err := testIngestor(store, &memoryLifecycle{}, source).IngestSession(context.Background(), binding)
	// A host that cannot be reached is a fact about the host. It is not a report,
	// not an error for the session, and above all not a reason to move the cursor.
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Applied != 0 || len(store.cursorCalls) != 0 {
		t.Fatalf("result = %#v, cursor calls = %v", result, store.cursorCalls)
	}
}

func TestIngestFallsBackToTheTranscriptWithoutATerminal(t *testing.T) {
	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	frames := report(t, domain.ExecutionReportCheckpoint, "e1", 1, `{"summary":"from the transcript"}`)
	// Paseo's curator prefixes only the first line of a message, so a frame can
	// arrive with output glued to its left.
	transcript := "[User] launch prompt\n[Thought] considering\n" + strings.Join(frames, "\n") + "\n"
	source := &fakeSource{transcript: transcript}
	binding := testBinding()
	binding.TerminalID = ""

	result, err := testIngestor(store, &memoryLifecycle{}, source).IngestSession(context.Background(), binding)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Transport != domain.ExecutionEventSentinel || result.Applied != 1 {
		t.Fatalf("result = %#v", result)
	}
	if source.transcripts != 1 || len(source.captures) != 0 {
		t.Fatalf("reads = %d transcripts, %d captures", source.transcripts, len(source.captures))
	}
	// There is no cursor on this transport, so nothing pretends there is one.
	if len(store.cursorCalls) != 0 {
		t.Fatalf("cursor calls = %v, want none", store.cursorCalls)
	}
}

func TestIngestRefusesToReadWithoutAContract(t *testing.T) {
	frames := report(t, domain.ExecutionReportCheckpoint, "e1", 1, `{"summary":"ok"}`)
	for name, arrange := range map[string]func(*memoryIngestStore){
		"no brief at all": func(s *memoryIngestStore) { s.briefFound = false },
		"unreadable brief": func(s *memoryIngestStore) {
			s.briefFound = true
			s.brief = domain.SessionBrief{ID: "brief-1", BriefJSON: "{not json"}
		},
		"brief for another launch": func(s *memoryIngestStore) { seedBrief(s, testNonce, "launch-0") },
	} {
		store := newMemoryIngestStore()
		arrange(store)
		source := &fakeSource{window: domain.ExecutionEventWindow{Lines: frames, TotalLines: int64(len(frames))}}

		result, err := testIngestor(store, &memoryLifecycle{}, source).IngestSession(context.Background(), testBinding())
		if err != nil {
			t.Fatalf("%s: ingest: %v", name, err)
		}
		if result.Applied != 0 || len(store.reports) != 0 {
			t.Fatalf("%s: ingested under an unknown contract: %#v", name, result)
		}
		if len(source.captures) != 0 || source.transcripts != 0 {
			t.Fatalf("%s: read the host before establishing the contract", name)
		}
	}
}

func TestIngestSurfacesADurableWriteFailure(t *testing.T) {
	// A store failure is AO's own problem, not news about the remote agent: it
	// must reach the caller rather than being logged and swallowed like a host
	// outage, or reports would be silently dropped while the cursor moved on.
	frames := report(t, domain.ExecutionReportCheckpoint, "e1", 1, `{"summary":"ok"}`)
	window := domain.ExecutionEventWindow{Lines: frames, TotalLines: int64(len(frames))}

	store := newMemoryIngestStore()
	seedBrief(store, testNonce, "launch-1")
	store.recordErr = errors.New("disk full")
	if _, err := testIngestor(store, &memoryLifecycle{}, &fakeSource{window: window}).
		IngestSession(context.Background(), testBinding()); err == nil {
		t.Fatal("want the record failure returned")
	}

	failing := newMemoryIngestStore()
	failing.briefErr = errors.New("database is locked")
	source := &fakeSource{window: window}
	if _, err := testIngestor(failing, &memoryLifecycle{}, source).
		IngestSession(context.Background(), testBinding()); err == nil {
		t.Fatal("want the contract read failure returned")
	}
	if len(source.captures) != 0 {
		t.Fatalf("read the host with no contract established: %v", source.captures)
	}
}

func TestIngestSkipsASessionWithNothingLaunched(t *testing.T) {
	store := newMemoryIngestStore()
	source := &fakeSource{}
	binding := testBinding()
	binding.TerminalID = ""
	binding.ExternalAgentID = ""

	result, err := testIngestor(store, &memoryLifecycle{}, source).IngestSession(context.Background(), binding)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Applied != 0 || len(source.captures) != 0 || source.transcripts != 0 {
		t.Fatalf("result = %#v, captures = %v", result, source.captures)
	}
}

func TestStoreInterfaceCannotAuthorizeAnythingIrreversible(t *testing.T) {
	// The ingest path's safety is structural rather than conditional: a report
	// cannot stop, archive, merge, push, approve, or re-host anything because the
	// Store it writes through has no method that could. The transport is
	// forgeable — Paseo's activity read is cross-agent and unscoped — so this is
	// the property that keeps a replayed report harmless.
	methods := map[string]bool{
		"GetLatestSessionBrief": true, "RecordExecutionReport": true,
		"MarkExecutionReportApplied": true, "RecordExecutionObservation": true,
		"OpenExecutionAgentQuestion": true, "RecordSessionCheckpoint": true,
		"AdvanceExecutionEventCursor": true,
	}
	storeType := reflect.TypeOf((*Store)(nil)).Elem()
	if storeType.NumMethod() != len(methods) {
		t.Fatalf("Store has %d methods, want exactly %d: a new one needs a safety review",
			storeType.NumMethod(), len(methods))
	}
	for index := 0; index < storeType.NumMethod(); index++ {
		name := storeType.Method(index).Name
		if !methods[name] {
			t.Fatalf("Store gained method %q; a report may not reach it without review", name)
		}
	}
}
