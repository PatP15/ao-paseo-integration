package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestListSessionExecutionEventsPagesInIngestionOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC)

	// Three agent-authored reports over the terminal transport plus one
	// observer transition, interleaved in time across two sessions.
	for i := 0; i < 3; i++ {
		if _, err := s.RecordExecutionReport(ctx, domain.ExecutionReportEvent{
			SessionID: "session-1", HostID: "host-1", LaunchID: "launch-1",
			EventID: fmt.Sprintf("evt-%d", i), Seq: int64(i + 1),
			Type: domain.ExecutionReportType("checkpoint"), Transport: domain.ExecutionEventTerminal,
			PayloadJSON: fmt.Sprintf(`{"summary":"step %d"}`, i),
			RawLine:     "PASEO1 ...", ObservedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RecordExecutionObservation(ctx, domain.ExecutionObservationEvent{
		SessionID: "session-1", HostID: "host-1", Type: "status_transition",
		Transport: domain.ExecutionEventInspect, PayloadJSON: `{"from":"running","to":"idle"}`,
		ObservedAt: base.Add(90 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordExecutionObservation(ctx, domain.ExecutionObservationEvent{
		SessionID: "session-2", HostID: "host-1", Type: "status_transition",
		Transport: domain.ExecutionEventInspect, PayloadJSON: `{"from":"idle","to":"running"}`,
		ObservedAt: base,
	}); err != nil {
		t.Fatal(err)
	}

	firstPage, err := s.ListSessionExecutionEvents(ctx, "session-1", "", 3)
	if err != nil || len(firstPage) != 3 {
		t.Fatalf("first page = (%d events, %v)", len(firstPage), err)
	}
	rest, err := s.ListSessionExecutionEvents(ctx, "session-1", firstPage[2].ID, 10)
	if err != nil || len(rest) != 1 {
		t.Fatalf("second page = (%d events, %v)", len(rest), err)
	}
	var all []domain.ExecutionEventRecord
	all = append(all, firstPage...)
	all = append(all, rest...)
	for i := 1; i < len(all); i++ {
		if all[i].IngestedAt.Before(all[i-1].IngestedAt) {
			t.Fatalf("events out of ingestion order: %v then %v", all[i-1].IngestedAt, all[i].IngestedAt)
		}
		if all[i].SessionID != "session-1" {
			t.Fatalf("event from another session leaked: %#v", all[i])
		}
	}
	checkpoint := all[0]
	if checkpoint.EventType != "checkpoint" || checkpoint.Transport != domain.ExecutionEventTerminal ||
		checkpoint.LaunchID != "launch-1" || checkpoint.PayloadJSON != `{"summary":"step 0"}` {
		t.Fatalf("checkpoint row = %#v", checkpoint)
	}
	if !checkpoint.ObservedAt.Equal(base) {
		t.Fatalf("checkpoint observedAt = %v", checkpoint.ObservedAt)
	}
}

func TestListSessionExecutionEventsRefusesAnUnknownCursor(t *testing.T) {
	s := newTestStore(t)
	_, err := s.ListSessionExecutionEvents(context.Background(), "session-1", "no-such-event", 10)
	if !errors.Is(err, domain.ErrExecutionEventCursorUnknown) {
		t.Fatalf("unknown cursor error = %v", err)
	}
}
