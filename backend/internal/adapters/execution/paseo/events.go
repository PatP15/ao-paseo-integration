package paseo

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.ExecutionTerminalReader = (*Backend)(nil)
var _ ports.ExecutionTranscriptReader = (*Backend)(nil)

// CaptureTerminal reads a bounded, cursored range of one host terminal.
//
// This is the only cursored surface the Paseo CLI has, and the only one that
// keeps a model out of the byte path. It is a PTY, so the lines it returns are
// screen lines hard-wrapped at the terminal's column width, and its scrollback
// is finite: a caller that falls far enough behind loses lines permanently,
// which is why the reader's own sequence numbers are what detect a loss.
//
// Like the observer's reads this skips the desktop-managed daemon guard and
// checks only host registration. The guard costs an extra invocation, and at
// ~0.9 s per invocation a per-read guard would consume the polling budget; the
// caller has already established the host's identity for this tick.
func (b *Backend) CaptureTerminal(
	ctx context.Context,
	hostID domain.ExecutionHostID,
	terminalID string,
	start, end int64,
) (domain.ExecutionEventWindow, error) {
	if _, err := b.registeredHost(ctx, hostID); err != nil {
		return domain.ExecutionEventWindow{}, err
	}
	if terminalID == "" {
		return domain.ExecutionEventWindow{}, fmt.Errorf("terminal capture requires a terminal id")
	}
	if start < 0 || end <= start {
		return domain.ExecutionEventWindow{}, fmt.Errorf("terminal capture range %d..%d is empty", start, end)
	}
	capture, err := b.client.CaptureTerminal(ctx, terminalID, int(start), int(end))
	if err != nil {
		return domain.ExecutionEventWindow{}, fmt.Errorf("capture terminal %s on host %s: %w", terminalID, hostID, err)
	}
	if capture.TerminalID != terminalID {
		// A capture for another terminal is another session's bytes. Refusing is
		// the only safe response: the caller would otherwise advance this
		// session's cursor over lines it never owned.
		return domain.ExecutionEventWindow{}, fmt.Errorf(
			"paseo capture returned terminal %q, not %s", capture.TerminalID, terminalID)
	}
	return domain.ExecutionEventWindow{
		TerminalID: capture.TerminalID,
		Lines:      append([]string(nil), capture.Lines...),
		TotalLines: int64(capture.TotalLines),
	}, nil
}

// Transcript returns the agent's whole rendered transcript.
//
// The full read is deliberate. Paseo 0.2.5 offers no cursor here — `--since` is
// declared and never referenced — and every narrowing flag it does offer loses
// data: a filter renumbers entries so that previously separate messages get
// spliced together, a tail drops the oldest entries rather than the newest, and
// follow mode discards all history when its two-second history fetch times out.
// Full replay every call is what makes ingest at-least-once instead of lossy,
// so the absence of a cursor is the safe property here rather than the problem.
func (b *Backend) Transcript(
	ctx context.Context,
	hostID domain.ExecutionHostID,
	agentID domain.ExecutionAgentID,
) (string, error) {
	if _, err := b.registeredHost(ctx, hostID); err != nil {
		return "", err
	}
	if agentID == "" {
		return "", fmt.Errorf("transcript read requires an agent id")
	}
	transcript, err := b.client.Logs(ctx, string(agentID))
	if err != nil {
		return "", fmt.Errorf("read transcript of Paseo agent %s: %w", agentID, err)
	}
	return transcript, nil
}
