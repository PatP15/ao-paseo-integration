package paseo

import (
	"context"
	"testing"
	"time"
)

func TestSchedulesMapsRowsAndRefusesUnregisteredHosts(t *testing.T) {
	next := time.Date(2027, 1, 1, 3, 0, 0, 0, time.UTC)
	client := newFakeExecutionClient(nil)
	client.schedules = []Schedule{{
		ID: "1ce3c290", Name: "nightly", Cadence: "cron:0 3 1 1 *",
		Target: "new-agent:claude", Status: "active", NextRunAt: &next,
	}}
	backend := newBackend(client, newMemoryExecutionStore(nil), func() time.Time { return backendTestNow })

	schedules, err := backend.Schedules(context.Background(), "host-1")
	if err != nil || len(schedules) != 1 {
		t.Fatalf("Schedules = (%#v, %v)", schedules, err)
	}
	row := schedules[0]
	if row.ID != "1ce3c290" || row.Cadence != "cron:0 3 1 1 *" || !row.NextRunAt.Equal(next) || !row.LastRunAt.IsZero() {
		t.Fatalf("row = %#v", row)
	}
	if _, err := backend.Schedules(context.Background(), "ghost"); err == nil {
		t.Fatal("unregistered host accepted")
	}
}

func TestDeleteScheduleConfirmsTheDeletedID(t *testing.T) {
	client := newFakeExecutionClient(nil)
	backend := newBackend(client, newMemoryExecutionStore(nil), func() time.Time { return backendTestNow })
	if err := backend.DeleteSchedule(context.Background(), "host-1", "1ce3c290"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range client.calls {
		if call == "delete-schedule:1ce3c290" {
			found = true
		}
	}
	if !found {
		t.Fatalf("delete never reached the client: %v", client.calls)
	}
}

func TestScheduleDeleteArgsRejectFlagShapedIDs(t *testing.T) {
	for _, id := range []string{"", "-1ce3", "1ce3 c290"} {
		if _, err := scheduleDeleteArgs("worker:6767", id); err == nil {
			t.Fatalf("schedule id %q was accepted", id)
		}
	}
}
