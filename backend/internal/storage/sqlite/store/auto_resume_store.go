package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// GetAutoResumeSettings reads the singleton auto-resume policy. The row is
// seeded by the migration, so there is no "not found" case for a caller to
// handle — a missing row is a corrupt database, not a first run.
func (s *Store) GetAutoResumeSettings(ctx context.Context) (domain.AutoResumeSettings, error) {
	row, err := s.qr.GetAutoResumeSettings(ctx)
	if err != nil {
		return domain.AutoResumeSettings{}, fmt.Errorf("get auto-resume settings: %w", err)
	}
	updated, err := decodeExecutionTime(row.UpdatedAt)
	if err != nil {
		return domain.AutoResumeSettings{}, fmt.Errorf("decode auto-resume settings updated time: %w", err)
	}
	return domain.AutoResumeSettings{
		Enabled: row.Enabled != 0, ResumePrompt: row.ResumePrompt, UpdatedAt: updated,
	}, nil
}

// PutAutoResumeSettings replaces the policy.
//
// The prompt is stored exactly as given, including empty: an empty value is the
// durable way to say "use whatever default ships today", so writing the current
// default text in its place would freeze this install on today's wording.
func (s *Store) PutAutoResumeSettings(
	ctx context.Context,
	settings domain.AutoResumeSettings,
	at time.Time,
) (domain.AutoResumeSettings, error) {
	enabled := int64(0)
	if settings.Enabled {
		enabled = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.PutAutoResumeSettings(ctx, gen.PutAutoResumeSettingsParams{
		Enabled: enabled, ResumePrompt: settings.ResumePrompt, UpdatedAt: encodeExecutionTime(at),
	})
	if err != nil {
		return domain.AutoResumeSettings{}, fmt.Errorf("put auto-resume settings: %w", err)
	}
	if rows == 0 {
		return domain.AutoResumeSettings{}, fmt.Errorf("put auto-resume settings: singleton row is missing")
	}
	settings.UpdatedAt = at.UTC()
	return settings, nil
}
