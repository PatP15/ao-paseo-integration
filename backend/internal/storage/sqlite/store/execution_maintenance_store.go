package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ReplaceExecutionHostSkills replaces one host's cached skills inventory whole.
// The channel always captures the complete list, so a partial merge could only
// preserve rows the host no longer has.
func (s *Store) ReplaceExecutionHostSkills(
	ctx context.Context, hostID domain.ExecutionHostID, skills []domain.ExecutionHostSkill, at time.Time,
) error {
	if hostID == "" {
		return fmt.Errorf("replace host skills: host id is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "replace host skills", func(q *gen.Queries) error {
		if err := q.DeleteExecutionHostSkills(ctx, string(hostID)); err != nil {
			return fmt.Errorf("clear host %s skills: %w", hostID, err)
		}
		for _, skill := range skills {
			if skill.Name == "" {
				return fmt.Errorf("host %s skill has no name", hostID)
			}
			if err := q.InsertExecutionHostSkill(ctx, gen.InsertExecutionHostSkillParams{
				HostID: string(hostID), Name: skill.Name, Description: skill.Description,
				CapturedAt: encodeExecutionTime(at),
			}); err != nil {
				return fmt.Errorf("insert host %s skill %s: %w", hostID, skill.Name, err)
			}
		}
		return nil
	})
}

// ListExecutionHostSkills returns one host's cached inventory, name-sorted.
func (s *Store) ListExecutionHostSkills(ctx context.Context, hostID domain.ExecutionHostID) ([]domain.ExecutionHostSkill, error) {
	rows, err := s.qr.ListExecutionHostSkills(ctx, string(hostID))
	if err != nil {
		return nil, fmt.Errorf("list host %s skills: %w", hostID, err)
	}
	skills := make([]domain.ExecutionHostSkill, 0, len(rows))
	for _, row := range rows {
		capturedAt, err := decodeExecutionTime(row.CapturedAt)
		if err != nil {
			return nil, fmt.Errorf("decode host %s skill captured_at: %w", hostID, err)
		}
		skills = append(skills, domain.ExecutionHostSkill{
			HostID: domain.ExecutionHostID(row.HostID), Name: row.Name,
			Description: row.Description, CapturedAt: capturedAt,
		})
	}
	return skills, nil
}

// UpsertExecutionHostPrefs stores the confirmed preferences copy and hash.
func (s *Store) UpsertExecutionHostPrefs(ctx context.Context, prefs domain.ExecutionHostPrefs) error {
	if prefs.HostID == "" || prefs.SHA256 == "" {
		return fmt.Errorf("upsert host prefs: host id and sha256 are required")
	}
	exists := int64(0)
	if prefs.Exists {
		exists = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.UpsertExecutionHostPrefs(ctx, gen.UpsertExecutionHostPrefsParams{
		HostID: string(prefs.HostID), Content: prefs.Content, Sha256: prefs.SHA256,
		FileExists: exists, ConfirmedAt: encodeExecutionTime(prefs.ConfirmedAt),
	}); err != nil {
		return fmt.Errorf("upsert host %s prefs: %w", prefs.HostID, err)
	}
	return nil
}

// GetExecutionHostPrefs returns the confirmed preferences copy, if any.
func (s *Store) GetExecutionHostPrefs(ctx context.Context, hostID domain.ExecutionHostID) (domain.ExecutionHostPrefs, bool, error) {
	row, err := s.qr.GetExecutionHostPrefs(ctx, string(hostID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExecutionHostPrefs{}, false, nil
	}
	if err != nil {
		return domain.ExecutionHostPrefs{}, false, fmt.Errorf("get host %s prefs: %w", hostID, err)
	}
	confirmedAt, err := decodeExecutionTime(row.ConfirmedAt)
	if err != nil {
		return domain.ExecutionHostPrefs{}, false, fmt.Errorf("decode host %s prefs confirmed_at: %w", hostID, err)
	}
	return domain.ExecutionHostPrefs{
		HostID: domain.ExecutionHostID(row.HostID), Content: row.Content, SHA256: row.Sha256,
		Exists: row.FileExists != 0, ConfirmedAt: confirmedAt,
	}, true, nil
}
