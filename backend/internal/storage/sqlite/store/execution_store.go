package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// UpsertExecutionHost writes a host and replaces its routable capability set
// atomically.
func (s *Store) UpsertExecutionHost(ctx context.Context, host domain.ExecutionHost, capabilities []string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "upsert execution host", func(q *gen.Queries) error {
		if err := q.UpsertExecutionHost(ctx, executionHostParams(host)); err != nil {
			return err
		}
		if err := q.DeleteExecutionHostCapabilities(ctx, string(host.ID)); err != nil {
			return err
		}
		for _, capability := range capabilities {
			if err := q.InsertExecutionHostCapability(ctx, gen.InsertExecutionHostCapabilityParams{
				HostID: string(host.ID), Capability: capability,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetExecutionHost returns a registered execution host and its capabilities.
func (s *Store) GetExecutionHost(ctx context.Context, id domain.ExecutionHostID) (domain.ExecutionHost, []string, bool, error) {
	row, err := s.qr.GetExecutionHost(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExecutionHost{}, nil, false, nil
	}
	if err != nil {
		return domain.ExecutionHost{}, nil, false, fmt.Errorf("get execution host %s: %w", id, err)
	}
	capabilities, err := s.qr.ListExecutionHostCapabilities(ctx, string(id))
	if err != nil {
		return domain.ExecutionHost{}, nil, false, fmt.Errorf("list execution host %s capabilities: %w", id, err)
	}
	host, err := executionHostFromGen(row)
	if err != nil {
		return domain.ExecutionHost{}, nil, false, err
	}
	return host, capabilities, true, nil
}

// UpsertProjectHostBinding writes the machine-specific project path for a host.
func (s *Store) UpsertProjectHostBinding(ctx context.Context, binding domain.ProjectHostBinding) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.UpsertProjectHostBinding(ctx, gen.UpsertProjectHostBindingParams{
		ProjectID: string(binding.ProjectID), HostID: string(binding.HostID),
		HostRepoPath: binding.HostRepoPath, BaseBranch: binding.BaseBranch,
		Priority: int64(binding.Priority), Enabled: executionBoolInt(binding.Enabled),
		SetupProfile: binding.SetupProfile, CreatedAt: encodeExecutionTime(binding.CreatedAt),
		UpdatedAt: encodeExecutionTime(binding.UpdatedAt),
	})
}

// UpsertSessionExecutionBinding persists remote identifiers before callers use
// them for later lifecycle operations.
func (s *Store) UpsertSessionExecutionBinding(ctx context.Context, binding domain.SessionExecutionBinding) error {
	if binding.LabelsWritten == nil {
		binding.LabelsWritten = map[string]string{}
	}
	labels, err := json.Marshal(binding.LabelsWritten)
	if err != nil {
		return fmt.Errorf("marshal execution labels: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.UpsertSessionExecutionBinding(ctx, gen.UpsertSessionExecutionBindingParams{
		SessionID: string(binding.SessionID), WorkItemID: nullableString(binding.WorkItemID),
		BackendType: string(binding.BackendType), HostID: string(binding.HostID),
		ExternalWorkspaceID: string(binding.ExternalWorkspaceID), ExternalAgentID: string(binding.ExternalAgentID),
		ExternalParentAgentID: string(binding.ExternalParentAgentID), BoundServerID: binding.BoundServerID,
		WorkspaceTitle: binding.WorkspaceTitle, IntentID: string(binding.IntentID), Attempt: int64(binding.Attempt),
		LabelsWrittenJson: string(labels), BranchName: binding.BranchName, HostWorkspacePath: binding.HostWorkspacePath,
		Provider: binding.Provider, Model: binding.Model, Mode: binding.Mode,
		DispatchGeneration: int64(binding.DispatchGeneration), LaunchID: binding.LaunchID,
		TranscriptBytes: binding.TranscriptBytes, TranscriptPrefixSha256: binding.TranscriptPrefixSHA256,
		TerminalID: binding.TerminalID, TerminalLinesConsumed: binding.TerminalLinesConsumed,
		LastObservedAt: encodeExecutionTime(binding.LastObservedAt), CreatedAt: encodeExecutionTime(binding.CreatedAt),
		ArchivedAt: encodeExecutionTime(binding.ArchivedAt),
	})
}

// GetSessionExecutionBinding returns the durable execution binding for a session.
func (s *Store) GetSessionExecutionBinding(ctx context.Context, sessionID domain.SessionID) (domain.SessionExecutionBinding, bool, error) {
	row, err := s.qr.GetSessionExecutionBinding(ctx, string(sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionExecutionBinding{}, false, nil
	}
	if err != nil {
		return domain.SessionExecutionBinding{}, false, fmt.Errorf("get session execution binding %s: %w", sessionID, err)
	}
	binding, err := sessionExecutionBindingFromGen(row)
	if err != nil {
		return domain.SessionExecutionBinding{}, false, err
	}
	return binding, true, nil
}

func executionHostParams(host domain.ExecutionHost) gen.UpsertExecutionHostParams {
	return gen.UpsertExecutionHostParams{
		ID: string(host.ID), Name: host.Name, BackendType: string(host.BackendType),
		Transport: string(host.Transport), Endpoint: host.Endpoint, EndpointSecretRef: host.EndpointSecretRef,
		TrustZone: string(host.TrustZone), Enabled: executionBoolInt(host.Enabled), MaxConcurrentSessions: int64(host.MaxConcurrentSessions),
		ServerID: host.ServerID, PaseoVersion: host.PaseoVersion, RequiresNoMcp: executionBoolInt(host.RequiresNoMCP),
		RequiresNoRelay: executionBoolInt(host.RequiresNoRelay), LastSuccessfulProbeAt: encodeExecutionTime(host.LastSuccessfulProbeAt),
		LastFailedProbeAt: encodeExecutionTime(host.LastFailedProbeAt), LastProbeError: host.LastProbeError,
		CreatedAt: encodeExecutionTime(host.CreatedAt), UpdatedAt: encodeExecutionTime(host.UpdatedAt),
	}
}

func executionHostFromGen(row gen.ExecutionHost) (domain.ExecutionHost, error) {
	lastSuccess, err := decodeExecutionTime(row.LastSuccessfulProbeAt)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("decode execution host %s successful probe: %w", row.ID, err)
	}
	lastFailure, err := decodeExecutionTime(row.LastFailedProbeAt)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("decode execution host %s failed probe: %w", row.ID, err)
	}
	created, err := decodeExecutionTime(row.CreatedAt)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("decode execution host %s created time: %w", row.ID, err)
	}
	updated, err := decodeExecutionTime(row.UpdatedAt)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("decode execution host %s updated time: %w", row.ID, err)
	}
	return domain.ExecutionHost{
		ID: domain.ExecutionHostID(row.ID), Name: row.Name, BackendType: domain.ExecutionBackendType(row.BackendType),
		Transport: domain.ExecutionHostTransport(row.Transport), Endpoint: row.Endpoint, EndpointSecretRef: row.EndpointSecretRef,
		TrustZone: domain.ExecutionTrustZone(row.TrustZone), Enabled: row.Enabled != 0,
		MaxConcurrentSessions: int(row.MaxConcurrentSessions), ServerID: row.ServerID, PaseoVersion: row.PaseoVersion,
		RequiresNoMCP: row.RequiresNoMcp != 0, RequiresNoRelay: row.RequiresNoRelay != 0,
		LastSuccessfulProbeAt: lastSuccess, LastFailedProbeAt: lastFailure, LastProbeError: row.LastProbeError,
		CreatedAt: created, UpdatedAt: updated,
	}, nil
}

func sessionExecutionBindingFromGen(row gen.SessionExecutionBinding) (domain.SessionExecutionBinding, error) {
	var labels map[string]string
	if err := json.Unmarshal([]byte(row.LabelsWrittenJson), &labels); err != nil {
		return domain.SessionExecutionBinding{}, fmt.Errorf("decode session %s execution labels: %w", row.SessionID, err)
	}
	lastObserved, err := decodeExecutionTime(row.LastObservedAt)
	if err != nil {
		return domain.SessionExecutionBinding{}, err
	}
	created, err := decodeExecutionTime(row.CreatedAt)
	if err != nil {
		return domain.SessionExecutionBinding{}, err
	}
	archived, err := decodeExecutionTime(row.ArchivedAt)
	if err != nil {
		return domain.SessionExecutionBinding{}, err
	}
	return domain.SessionExecutionBinding{
		SessionID: domain.SessionID(row.SessionID), WorkItemID: row.WorkItemID.String,
		BackendType: domain.ExecutionBackendType(row.BackendType), HostID: domain.ExecutionHostID(row.HostID),
		ExternalWorkspaceID: domain.ExecutionWorkspaceID(row.ExternalWorkspaceID), ExternalAgentID: domain.ExecutionAgentID(row.ExternalAgentID),
		ExternalParentAgentID: domain.ExecutionAgentID(row.ExternalParentAgentID), BoundServerID: row.BoundServerID,
		WorkspaceTitle: row.WorkspaceTitle, IntentID: domain.ExecutionIntentID(row.IntentID), Attempt: int(row.Attempt),
		LabelsWritten: labels, BranchName: row.BranchName, HostWorkspacePath: row.HostWorkspacePath,
		Provider: row.Provider, Model: row.Model, Mode: row.Mode, DispatchGeneration: int(row.DispatchGeneration),
		LaunchID: row.LaunchID, TranscriptBytes: row.TranscriptBytes, TranscriptPrefixSHA256: row.TranscriptPrefixSha256,
		TerminalID: row.TerminalID, TerminalLinesConsumed: row.TerminalLinesConsumed,
		LastObservedAt: lastObserved, CreatedAt: created, ArchivedAt: archived,
	}, nil
}

func executionBoolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func encodeExecutionTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func decodeExecutionTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}
