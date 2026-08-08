// Package paseoreporter implements the worker-side half of AO's deterministic
// report transport.
//
// Agents append one validated ao.agent-event.v1 JSON object with Emit. A
// long-running Serve process tails that launch's spool and writes checksummed,
// 76-column frames to stdout. AO starts Serve in a Paseo terminal, making the
// terminal's monotonic line cursor the transport while keeping the model out of
// framing, base64, and checksum generation.
package paseoreporter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
)

const (
	pollInterval = 100 * time.Millisecond
	maxReadBytes = 64 * 1024
)

// DefaultDataDir resolves the worker-side AO state directory. Reporter spools
// are app state, not repository content, so they live under ~/.ao/data (or an
// explicit AO_DATA_DIR) and can never dirty the agent's worktree.
func DefaultDataDir() (string, error) {
	if dataDir := strings.TrimSpace(os.Getenv("AO_DATA_DIR")); dataDir != "" {
		absolute, err := filepath.Abs(dataDir)
		if err != nil {
			return "", fmt.Errorf("resolve AO_DATA_DIR: %w", err)
		}
		return absolute, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".ao", "data"), nil
}

// Emit validates one complete event and appends it atomically to the launch's
// spool. The event supplies its own launch id, which is hashed before becoming
// a filename so an agent-controlled identifier cannot escape the state dir.
func Emit(dataDir string, payload []byte) error {
	payload = bytes.TrimSpace(payload)
	event, err := paseoevent.DecodeEvent(payload)
	if err != nil {
		return fmt.Errorf("validate report: %w", err)
	}
	path, err := spoolPath(dataDir, event.LaunchID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create reporter state directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open report spool: %w", err)
	}

	line := append(append([]byte(nil), payload...), '\n')
	if _, err := file.Write(line); err != nil {
		_ = file.Close()
		return fmt.Errorf("append report: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close report spool: %w", err)
	}
	return nil
}

// Serve tails one launch spool until ctx is canceled and writes terminal-safe
// frames to out. Existing rows are replayed on restart; AO deduplicates them by
// eventId, so replay is safer than maintaining another cursor that can be lost.
func Serve(ctx context.Context, dataDir, sessionID, launchID, nonce string, out io.Writer) error {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(launchID) == "" {
		return fmt.Errorf("serve requires session and launch ids")
	}
	if err := paseoevent.ValidateNonce(nonce); err != nil {
		return err
	}
	path, err := spoolPath(dataDir, launchID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create reporter state directory: %w", err)
	}
	if file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
		return fmt.Errorf("create report spool: %w", err)
	} else if err := file.Close(); err != nil {
		return fmt.Errorf("close report spool: %w", err)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var offset int64
	var pending []byte
	for {
		var readErr error
		offset, pending, readErr = readAvailable(path, offset, pending, func(line []byte) error {
			frames, encodeErr := encodeEvent(line, sessionID, launchID, nonce)
			if encodeErr != nil {
				// Emit validates before append, so this means a partial write or
				// out-of-band spool modification. Drop only that line: one bad
				// advisory report must not darken the channel for every later one.
				_, writeErr := fmt.Fprintln(out, "AO_REPORTER_DROPPED invalid event")
				return writeErr
			}
			return writeFrames(frames, out)
		})
		if readErr != nil {
			return readErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func readAvailable(path string, offset int64, pending []byte, apply func([]byte) error) (int64, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return offset, pending, fmt.Errorf("open report spool: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return offset, pending, fmt.Errorf("stat report spool: %w", err)
	}
	if info.Size() < offset {
		// A truncated spool is treated as a fresh replay. Emit itself only appends,
		// but recovering here keeps an operator repair from wedging the reporter.
		offset, pending = 0, nil
	}
	if info.Size() == offset {
		return offset, pending, nil
	}
	available := info.Size() - offset
	if available > maxReadBytes {
		available = maxReadBytes
	}
	chunk := make([]byte, available)
	read, err := file.ReadAt(chunk, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return offset, pending, fmt.Errorf("read report spool: %w", err)
	}
	offset += int64(read)
	pending = append(pending, chunk[:read]...)
	for {
		newline := bytes.IndexByte(pending, '\n')
		if newline < 0 {
			break
		}
		line := bytes.TrimSpace(pending[:newline])
		pending = pending[newline+1:]
		if len(line) == 0 {
			continue
		}
		if err := apply(line); err != nil {
			return offset, pending, err
		}
	}
	return offset, pending, nil
}

func writeEvent(payload []byte, sessionID, launchID, nonce string, out io.Writer) error {
	frames, err := encodeEvent(payload, sessionID, launchID, nonce)
	if err != nil {
		return err
	}
	return writeFrames(frames, out)
}

func encodeEvent(payload []byte, sessionID, launchID, nonce string) ([]string, error) {
	event, err := paseoevent.DecodeEvent(payload)
	if err != nil {
		return nil, fmt.Errorf("decode spooled report: %w", err)
	}
	if event.SessionID != sessionID || event.LaunchID != launchID {
		return nil, fmt.Errorf("spooled report belongs to another session or launch")
	}
	frames, err := paseoevent.EncodeFrames(nonce, payload)
	if err != nil {
		return nil, fmt.Errorf("encode report frames: %w", err)
	}
	return frames, nil
}

func writeFrames(frames []string, out io.Writer) error {
	for _, frame := range frames {
		if _, err := fmt.Fprintln(out, frame); err != nil {
			return fmt.Errorf("write report frame: %w", err)
		}
	}
	return nil
}

func spoolPath(dataDir, launchID string) (string, error) {
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(launchID) == "" {
		return "", fmt.Errorf("report spool requires data directory and launch id")
	}
	digest := sha256.Sum256([]byte(launchID))
	return filepath.Join(dataDir, "paseo-reporter", hex.EncodeToString(digest[:])+".ndjson"), nil
}
