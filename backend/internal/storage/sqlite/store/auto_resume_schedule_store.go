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

// AutoResumeSessionState reads what the watcher must know before it schedules
// another resume for a session: how many it has already spent, whether one is
// still unsent, and when it last acted.
//
// The three reads are one method rather than three store calls so the caller
// cannot accidentally check the cap against one session and the pending row
// against another.
func (s *Store) AutoResumeSessionState(
	ctx context.Context, sessionID domain.SessionID,
) (domain.AutoResumeSessionState, error) {
	if sessionID == "" {
		return domain.AutoResumeSessionState{}, fmt.Errorf("auto-resume state: session id is required")
	}
	attempts, err := s.qr.CountAutoResumes(ctx, string(sessionID))
	if err != nil {
		return domain.AutoResumeSessionState{}, fmt.Errorf("count auto-resumes for %s: %w", sessionID, err)
	}
	state := domain.AutoResumeSessionState{Attempts: int(attempts)}
	if attempts == 0 {
		return state, nil
	}
	detected, err := s.qr.LastAutoResumeDetectedAt(ctx, string(sessionID))
	if err != nil {
		return domain.AutoResumeSessionState{}, fmt.Errorf("last auto-resume for %s: %w", sessionID, err)
	}
	if detected != "" {
		at, err := decodeExecutionTime(detected)
		if err != nil {
			return domain.AutoResumeSessionState{}, fmt.Errorf("decode last auto-resume time for %s: %w", sessionID, err)
		}
		state.LastDetectedAt = at
	}
	row, err := s.qr.GetPendingAutoResume(ctx, string(sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return domain.AutoResumeSessionState{}, fmt.Errorf("pending auto-resume for %s: %w", sessionID, err)
	}
	pending, err := decodeAutoResume(row)
	if err != nil {
		return domain.AutoResumeSessionState{}, err
	}
	state.Pending, state.HasPending = pending, true
	return state, nil
}

// ScheduleAutoResume records one pending resume.
//
// The partial unique index on (session_id) WHERE state = 'pending' is what
// makes this safe to call from a racing tick: the second insert is refused by
// the database rather than nudging the agent twice for one death.
func (s *Store) ScheduleAutoResume(ctx context.Context, row domain.AutoResumeSchedule) error {
	if row.ID == "" || row.SessionID == "" {
		return fmt.Errorf("schedule auto-resume: id and session id are required")
	}
	if row.Attempt < 1 {
		return fmt.Errorf("schedule auto-resume: attempt must be positive")
	}
	exact := int64(0)
	if row.ExactReset {
		exact = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.InsertAutoResume(ctx, gen.InsertAutoResumeParams{
		ID: row.ID, SessionID: string(row.SessionID), LaunchID: row.LaunchID,
		Attempt: int64(row.Attempt), ResumeAt: encodeExecutionTime(row.ResumeAt),
		ExactReset: exact, Notice: row.Notice,
		DetectedAt: encodeExecutionTime(row.DetectedAt), UpdatedAt: encodeExecutionTime(row.UpdatedAt),
	}); err != nil {
		return fmt.Errorf("schedule auto-resume for %s: %w", row.SessionID, err)
	}
	return nil
}

// ListDueAutoResumes returns pending resumes whose time has come, oldest first.
func (s *Store) ListDueAutoResumes(
	ctx context.Context, at time.Time, limit int,
) ([]domain.AutoResumeSchedule, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list due auto-resumes: limit must be positive")
	}
	rows, err := s.qr.ListDueAutoResumes(ctx, gen.ListDueAutoResumesParams{
		ResumeAt: encodeExecutionTime(at), Limit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list due auto-resumes: %w", err)
	}
	return decodeAutoResumes(rows)
}

// ListPendingAutoResumes returns every unsent resume, oldest due first. It is
// the read behind the "waiting for its limit to reset" badge, which needs one
// answer for the whole session list rather than a probe per card.
func (s *Store) ListPendingAutoResumes(ctx context.Context) ([]domain.AutoResumeSchedule, error) {
	rows, err := s.qr.ListPendingAutoResumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pending auto-resumes: %w", err)
	}
	return decodeAutoResumes(rows)
}

// SettleAutoResume closes out one pending resume. It reports whether this call
// was the one that settled it: a row another tick already resolved stays as it
// was rather than taking a second verdict.
func (s *Store) SettleAutoResume(
	ctx context.Context, id string, state domain.AutoResumeState, detail string, at time.Time,
) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("settle auto-resume: id is required")
	}
	if state == domain.AutoResumePending {
		return false, fmt.Errorf("settle auto-resume %s: pending is not a settled state", id)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.SettleAutoResume(ctx, gen.SettleAutoResumeParams{
		State: string(state), Detail: detail, UpdatedAt: encodeExecutionTime(at), ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("settle auto-resume %s: %w", id, err)
	}
	return rows > 0, nil
}

// CancelPendingAutoResume drops a session's unsent resume, returning whether
// there was one. Used when the session revived on its own or was terminated:
// the resume it was waiting to send no longer describes anything real.
func (s *Store) CancelPendingAutoResume(
	ctx context.Context, sessionID domain.SessionID, detail string, at time.Time,
) (bool, error) {
	if sessionID == "" {
		return false, fmt.Errorf("cancel auto-resume: session id is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.CancelPendingAutoResumes(ctx, gen.CancelPendingAutoResumesParams{
		Detail: detail, UpdatedAt: encodeExecutionTime(at), SessionID: string(sessionID),
	})
	if err != nil {
		return false, fmt.Errorf("cancel auto-resume for %s: %w", sessionID, err)
	}
	return rows > 0, nil
}

func decodeAutoResumes(rows []gen.AutoResumeSchedule) ([]domain.AutoResumeSchedule, error) {
	out := make([]domain.AutoResumeSchedule, 0, len(rows))
	for _, row := range rows {
		decoded, err := decodeAutoResume(row)
		if err != nil {
			return nil, err
		}
		out = append(out, decoded)
	}
	return out, nil
}

func decodeAutoResume(row gen.AutoResumeSchedule) (domain.AutoResumeSchedule, error) {
	resumeAt, err := decodeExecutionTime(row.ResumeAt)
	if err != nil {
		return domain.AutoResumeSchedule{}, fmt.Errorf("decode auto-resume %s resume_at: %w", row.ID, err)
	}
	detectedAt, err := decodeExecutionTime(row.DetectedAt)
	if err != nil {
		return domain.AutoResumeSchedule{}, fmt.Errorf("decode auto-resume %s detected_at: %w", row.ID, err)
	}
	updatedAt, err := decodeExecutionTime(row.UpdatedAt)
	if err != nil {
		return domain.AutoResumeSchedule{}, fmt.Errorf("decode auto-resume %s updated_at: %w", row.ID, err)
	}
	return domain.AutoResumeSchedule{
		ID: row.ID, SessionID: domain.SessionID(row.SessionID), LaunchID: row.LaunchID,
		Attempt: int(row.Attempt), State: domain.AutoResumeState(row.State),
		ResumeAt: resumeAt, ExactReset: row.ExactReset != 0, Notice: row.Notice,
		Detail: row.Detail, DetectedAt: detectedAt, UpdatedAt: updatedAt,
	}, nil
}
