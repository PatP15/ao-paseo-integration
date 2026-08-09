package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func seedObservedHost(t *testing.T, s *sqlite.Store, serverID string) domain.ExecutionHost {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	host := domain.ExecutionHost{
		ID: "worker-1", Name: "Linux worker", BackendType: domain.ExecutionBackendPaseo,
		Transport: domain.ExecutionTransportTailscale, Endpoint: "worker:6767",
		TrustZone: domain.ExecutionTrustZoneHobby, Enabled: true, MaxConcurrentSessions: 2,
		ServerID: serverID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertExecutionHost(context.Background(), host, nil); err != nil {
		t.Fatalf("seed execution host: %v", err)
	}
	return host
}

func TestRecordExecutionHostProbeKeepsTheRegisteredIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedObservedHost(t, s, "server-1")
	at := time.Now().UTC().Truncate(time.Second)

	if err := s.RecordExecutionHostProbe(ctx, domain.ExecutionHostProbe{
		HostID: "worker-1", ServerID: "server-2", Version: "0.2.5", Reachable: true, ObservedAt: at,
	}); err != nil {
		t.Fatalf("record probe: %v", err)
	}
	got, _, _, err := s.GetExecutionHost(ctx, "worker-1")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	// Overwriting server_id would erase the evidence that the daemon was
	// replaced, and that fact invalidates every agent id AO holds for the host.
	if got.ServerID != "server-1" {
		t.Fatalf("server id = %q, want the registered one", got.ServerID)
	}
	if !got.LastSuccessfulProbeAt.Equal(at) || got.PaseoVersion != "0.2.5" || got.LastProbeError != "" {
		t.Fatalf("host = %#v", got)
	}

	failedAt := at.Add(time.Minute)
	if err := s.RecordExecutionHostProbe(ctx, domain.ExecutionHostProbe{
		HostID: "worker-1", Reachable: false, Error: "connection refused", ObservedAt: failedAt,
	}); err != nil {
		t.Fatalf("record failed probe: %v", err)
	}
	got, _, _, err = s.GetExecutionHost(ctx, "worker-1")
	if err != nil {
		t.Fatalf("get host after failure: %v", err)
	}
	if !got.LastFailedProbeAt.Equal(failedAt) || got.LastProbeError != "connection refused" {
		t.Fatalf("host = %#v", got)
	}
	// A failed probe does not disable the host or forget that it once answered.
	if !got.Enabled || !got.LastSuccessfulProbeAt.Equal(at) {
		t.Fatalf("a failed probe damaged the host row: %#v", got)
	}
}

func TestRecordExecutionHostProbeAdoptsAnUnknownIdentityOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedObservedHost(t, s, "")
	at := time.Now().UTC().Truncate(time.Second)

	if err := s.RecordExecutionHostProbe(ctx, domain.ExecutionHostProbe{
		HostID: "worker-1", ServerID: "server-1", Reachable: true, ObservedAt: at,
	}); err != nil {
		t.Fatalf("record probe: %v", err)
	}
	got, _, _, err := s.GetExecutionHost(ctx, "worker-1")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if got.ServerID != "server-1" {
		t.Fatalf("server id = %q, want the first observed identity", got.ServerID)
	}
}

func TestRecordExecutionObservationDedupesUnchangedFacts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	event := domain.ExecutionObservationEvent{
		SessionID: "ao-1", HostID: "worker-1", LaunchID: "launch-1",
		Type: domain.ExecutionObservedRunning, Transport: domain.ExecutionEventInspect,
		PayloadJSON: `{"status":"running"}`, ObservedAt: time.Now().UTC().Truncate(time.Second),
	}

	inserted, err := s.RecordExecutionObservation(ctx, event)
	if err != nil || !inserted {
		t.Fatalf("first observation: inserted=%v err=%v", inserted, err)
	}
	// The same fact re-read on the next tick is one durable row, not two.
	event.ObservedAt = event.ObservedAt.Add(5 * time.Second)
	inserted, err = s.RecordExecutionObservation(ctx, event)
	if err != nil || inserted {
		t.Fatalf("repeat observation: inserted=%v err=%v", inserted, err)
	}

	event.PayloadJSON = `{"status":"idle"}`
	event.Type = domain.ExecutionObservedIdle
	inserted, err = s.RecordExecutionObservation(ctx, event)
	if err != nil || !inserted {
		t.Fatalf("changed observation: inserted=%v err=%v", inserted, err)
	}

	// A different session observing the identical fact is its own row.
	event.SessionID = "ao-2"
	inserted, err = s.RecordExecutionObservation(ctx, event)
	if err != nil || !inserted {
		t.Fatalf("other session observation: inserted=%v err=%v", inserted, err)
	}

	if _, err := s.RecordExecutionObservation(ctx, domain.ExecutionObservationEvent{
		SessionID: "ao-1", Type: domain.ExecutionObservedIdle, Transport: domain.ExecutionEventInspect,
	}); err == nil {
		t.Fatal("expected an empty payload to be refused")
	}
}

func TestOpenExecutionPermissionQuestionIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	question := domain.ExecutionPermissionQuestion{
		SessionID: "ao-1", WorkItemID: "", ExternalID: "perm_c0ffee1234567890",
		ToolName: "Bash", Question: "The remote agent is requesting permission to use Bash.",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	_, opened, err := s.OpenExecutionPermissionQuestion(ctx, question)
	if err != nil || !opened {
		t.Fatalf("open question: opened=%v err=%v", opened, err)
	}
	_, opened, err = s.OpenExecutionPermissionQuestion(ctx, question)
	if err != nil || opened {
		t.Fatalf("re-observed question: opened=%v err=%v", opened, err)
	}

	open, err := s.ListOpenExecutionPermissionQuestions(ctx)
	if err != nil {
		t.Fatalf("list open questions: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open questions = %d, want 1", len(open))
	}
	// Paseo rejects a truncated request id, so the full one has to survive.
	if open[0].ExternalID != "perm_c0ffee1234567890" || open[0].SessionID != "ao-1" {
		t.Fatalf("question = %#v", open[0])
	}

	question.ExternalID = "perm_second"
	_, opened, err = s.OpenExecutionPermissionQuestion(ctx, question)
	if err != nil || !opened {
		t.Fatalf("second request: opened=%v err=%v", opened, err)
	}
}

func TestRecordExecutionOrphanReportsEachFindingOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	orphan := domain.ExecutionOrphan{
		Kind: domain.ExecutionOrphanAgent, HostID: "worker-1", SessionID: "ao-1",
		AgentID: "agent-stray", WorkspacePath: "/remote/worktree/ao-1",
		Detail: "unbound running agent", ObservedAt: time.Now().UTC().Truncate(time.Second),
	}

	recorded, err := s.RecordExecutionOrphan(ctx, orphan)
	if err != nil || !recorded {
		t.Fatalf("record orphan: recorded=%v err=%v", recorded, err)
	}
	// Re-seen on every five-minute sweep; the audit log records it once.
	recorded, err = s.RecordExecutionOrphan(ctx, orphan)
	if err != nil || recorded {
		t.Fatalf("repeat orphan: recorded=%v err=%v", recorded, err)
	}

	orphan.Kind = domain.ExecutionOrphanMissingAgent
	recorded, err = s.RecordExecutionOrphan(ctx, orphan)
	if err != nil || !recorded {
		t.Fatalf("different finding: recorded=%v err=%v", recorded, err)
	}
}

func TestMarkSessionExecutionObservedAdvancesLiveBindingsOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedObservedHost(t, s, "server-1")
	created := time.Now().UTC().Truncate(time.Second)
	binding := domain.SessionExecutionBinding{
		SessionID: "ao-1", BackendType: domain.ExecutionBackendPaseo, HostID: "worker-1",
		ExternalAgentID: "agent-1", CreatedAt: created,
	}
	if err := s.UpsertSessionExecutionBinding(ctx, binding); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	observed := created.Add(time.Minute)
	if err := s.MarkSessionExecutionObserved(ctx, "ao-1", observed); err != nil {
		t.Fatalf("mark observed: %v", err)
	}
	got, ok, err := s.GetSessionExecutionBinding(ctx, "ao-1")
	if err != nil || !ok {
		t.Fatalf("get binding: ok=%v err=%v", ok, err)
	}
	if !got.LastObservedAt.Equal(observed) {
		t.Fatalf("last observed = %v, want %v", got.LastObservedAt, observed)
	}

	archived := got
	archived.ArchivedAt = observed
	if err := s.UpsertSessionExecutionBinding(ctx, archived); err != nil {
		t.Fatalf("archive binding: %v", err)
	}
	if err := s.MarkSessionExecutionObserved(ctx, "ao-1", observed.Add(time.Hour)); err != nil {
		t.Fatalf("mark archived binding: %v", err)
	}
	got, _, err = s.GetSessionExecutionBinding(ctx, "ao-1")
	if err != nil {
		t.Fatalf("get archived binding: %v", err)
	}
	if !got.LastObservedAt.Equal(observed) {
		t.Fatalf("an archived binding was still advanced: %v", got.LastObservedAt)
	}
}
