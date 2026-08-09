package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDispatchRefusesSettingsWithoutAValidator(t *testing.T) {
	now := time.Date(2026, time.August, 9, 2, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	req := testDispatchRequest()
	req.ThinkingOptionID = "high"
	_, err := New(store).Dispatch(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "never forwarded") {
		t.Fatalf("Dispatch error = %v, want the unvalidated-settings refusal", err)
	}
	sessions, listErr := store.ListSessions(context.Background(), "project")
	if listErr != nil || len(sessions) != 0 {
		t.Fatalf("sessions after refused dispatch = (%v, %v), want none committed", sessions, listErr)
	}
}

func TestDispatchValidatesSettingsAgainstTheSelectedHost(t *testing.T) {
	now := time.Date(2026, time.August, 9, 3, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	service := New(store)

	var seen struct {
		hostID                          domain.ExecutionHostID
		provider, model, thinkingOption string
	}
	refusal := errors.New("thinking option not discovered")
	validatorErr := refusal
	service.SetSettingsValidator(func(_ context.Context, hostID domain.ExecutionHostID, provider, model, thinkingOptionID string) error {
		seen.hostID, seen.provider, seen.model, seen.thinkingOption = hostID, provider, model, thinkingOptionID
		return validatorErr
	})

	req := testDispatchRequest()
	req.ThinkingOptionID = "high"
	if _, err := service.Dispatch(context.Background(), req); !errors.Is(err, refusal) {
		t.Fatalf("Dispatch error = %v, want the validator refusal", err)
	}
	if seen.hostID != "host" || seen.provider != "codex" || seen.model != "gpt-test" || seen.thinkingOption != "high" {
		t.Fatalf("validator saw %#v", seen)
	}

	validatorErr = nil
	dispatched, err := service.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := decodeStartPayload(dispatched.Command.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ThinkingOptionID != "high" {
		t.Fatalf("payload thinking option = %q, want the validated id committed", payload.ThinkingOptionID)
	}
}

func TestDispatchWithoutSettingsNeverConsultsTheValidator(t *testing.T) {
	now := time.Date(2026, time.August, 9, 4, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	service := New(store)
	service.SetSettingsValidator(func(context.Context, domain.ExecutionHostID, string, string, string) error {
		t.Fatal("validator consulted for a request without settings")
		return nil
	})
	if _, err := service.Dispatch(context.Background(), testDispatchRequest()); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchRefusesProviderFeatures(t *testing.T) {
	now := time.Date(2026, time.August, 9, 5, 0, 0, 0, time.UTC)
	store := newDispatchTestStore(t, now)
	req := testDispatchRequest()
	req.Features = map[string]bool{"fast_mode": true}
	_, err := New(store).Dispatch(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "features") {
		t.Fatalf("Dispatch error = %v, want the features refusal", err)
	}
}
