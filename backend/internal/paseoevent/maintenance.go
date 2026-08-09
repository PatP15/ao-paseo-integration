package paseoevent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// The maintenance channel is AO asking a host's AO-owned worker binary about
// AO-owned files: the skills inventory and the orchestration preferences. It
// rides the exact same frame layer as reports (crc32, 76-col, base64, nonce),
// so the terminal transport needs no second parser — but it is a DIFFERENT
// schema on purpose. A maintenance event can never decode as a report Event
// and a report can never decode as a maintenance event, so neither ingest path
// can be confused by the other's traffic even before the nonce check.
//
// No LLM is anywhere in this byte path (G4's rule): the worker binary emits
// machine-generated frames and AO decodes them strictly.

// MaintenanceSchema identifies the maintenance wire format.
const MaintenanceSchema = "ao.maintenance.v1"

// Maintenance event kinds.
const (
	// MaintenanceSkill is one installed skill's identity.
	MaintenanceSkill = "skill"
	// MaintenancePrefs is one chunk of the preferences file content.
	MaintenancePrefs = "prefs"
	// MaintenanceDone terminates a successful run and carries the totals the
	// receiver verifies against what it assembled.
	MaintenanceDone = "done"
	// MaintenanceError terminates a failed run. Its presence means the worker
	// refused or could not complete the operation; partial output before it
	// must be discarded.
	MaintenanceError = "error"
)

// maxPrefsChunkBytes keeps one prefs chunk event under MaxPayloadBytes after
// JSON and base64 overhead.
const maxPrefsChunkBytes = 1024

// MaxPrefsFileBytes bounds the preferences file the channel will carry. The
// file is a small JSON config by design; anything larger is a mistake worth
// surfacing rather than transporting.
const MaxPrefsFileBytes = 64 * 1024

// MaintenanceEvent is one maintenance fact on the wire.
type MaintenanceEvent struct {
	Schema  string          `json:"schema"`
	Seq     int             `json:"seq"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// MaintenanceSkillPayload is one skill from ~/.claude/skills.
type MaintenanceSkillPayload struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// MaintenancePrefsPayload is one base64 chunk of the preferences file.
type MaintenancePrefsPayload struct {
	Part       int    `json:"part"`
	ContentB64 string `json:"contentB64"`
}

// MaintenanceDonePayload closes a run. For prefs operations SHA256 is the hex
// digest of the complete file content (empty file and missing file both hash
// the empty string; Exists distinguishes them) and Parts is how many prefs
// chunks were emitted. For inventory runs Count is the number of skills.
type MaintenanceDonePayload struct {
	Count  int    `json:"count,omitempty"`
	Parts  int    `json:"parts,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Exists bool   `json:"exists,omitempty"`
}

// MaintenanceErrorPayload names why the worker refused or failed.
type MaintenanceErrorPayload struct {
	Message string `json:"message"`
}

// EncodeMaintenanceEvent frames one maintenance event for the terminal.
func EncodeMaintenanceEvent(nonce string, seq int, kind string, payload any) ([]string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal maintenance payload: %w", err)
	}
	body, err := json.Marshal(MaintenanceEvent{Schema: MaintenanceSchema, Seq: seq, Kind: kind, Payload: raw})
	if err != nil {
		return nil, fmt.Errorf("marshal maintenance event: %w", err)
	}
	return EncodeFrames(nonce, body)
}

// WriteMaintenanceEvent frames and writes one maintenance event.
func WriteMaintenanceEvent(out io.Writer, nonce string, seq int, kind string, payload any) error {
	frames, err := EncodeMaintenanceEvent(nonce, seq, kind, payload)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if _, err := fmt.Fprintln(out, frame); err != nil {
			return fmt.Errorf("write maintenance frame: %w", err)
		}
	}
	return nil
}

// DecodeMaintenanceEvent strictly parses one assembled payload. Anything that
// is not exactly a maintenance event — a report, trailing JSON, an unknown
// kind — is rejected whole.
func DecodeMaintenanceEvent(payload []byte) (MaintenanceEvent, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var event MaintenanceEvent
	if err := decoder.Decode(&event); err != nil {
		return MaintenanceEvent{}, fmt.Errorf("decode maintenance event: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return MaintenanceEvent{}, fmt.Errorf("decode maintenance event: trailing JSON")
	}
	if event.Schema != MaintenanceSchema {
		return MaintenanceEvent{}, fmt.Errorf("maintenance event schema %q is not %s", event.Schema, MaintenanceSchema)
	}
	switch event.Kind {
	case MaintenanceSkill, MaintenancePrefs, MaintenanceDone, MaintenanceError:
	default:
		return MaintenanceEvent{}, fmt.Errorf("unknown maintenance kind %q", event.Kind)
	}
	if event.Seq < 1 {
		return MaintenanceEvent{}, fmt.Errorf("maintenance seq must be positive")
	}
	return event, nil
}

// SplitPrefsChunks slices file content into chunk payloads that each fit one
// frame-safe event.
func SplitPrefsChunks(content []byte) [][]byte {
	if len(content) == 0 {
		return nil
	}
	var chunks [][]byte
	for start := 0; start < len(content); start += maxPrefsChunkBytes {
		end := start + maxPrefsChunkBytes
		if end > len(content) {
			end = len(content)
		}
		chunks = append(chunks, content[start:end])
	}
	return chunks
}

// MaintenanceResult is a fully decoded, seq-verified maintenance run.
type MaintenanceResult struct {
	Skills     []MaintenanceSkillPayload
	PrefsParts map[int]string
	Done       *MaintenanceDonePayload
	Err        *MaintenanceErrorPayload
}

// ParseMaintenanceRun decodes the frame lines of one maintenance invocation.
// It returns an incomplete result (Done and Err both nil) when the terminating
// event has not arrived yet, so a caller can keep polling the capture.
func ParseMaintenanceRun(nonce string, lines []string) (MaintenanceResult, error) {
	result := MaintenanceResult{PrefsParts: map[int]string{}}
	decoded := Decode(nonce, lines)
	seen := map[int]bool{}
	for _, payload := range decoded.Payloads {
		event, err := DecodeMaintenanceEvent(payload.Data)
		if err != nil {
			return MaintenanceResult{}, err
		}
		if seen[event.Seq] {
			continue
		}
		seen[event.Seq] = true
		switch event.Kind {
		case MaintenanceSkill:
			var skill MaintenanceSkillPayload
			if err := strictUnmarshal(event.Payload, &skill); err != nil {
				return MaintenanceResult{}, err
			}
			if strings.TrimSpace(skill.Name) == "" {
				return MaintenanceResult{}, fmt.Errorf("maintenance skill event carries no name")
			}
			result.Skills = append(result.Skills, skill)
		case MaintenancePrefs:
			var chunk MaintenancePrefsPayload
			if err := strictUnmarshal(event.Payload, &chunk); err != nil {
				return MaintenanceResult{}, err
			}
			if chunk.Part < 1 || chunk.ContentB64 == "" {
				return MaintenanceResult{}, fmt.Errorf("maintenance prefs chunk is malformed")
			}
			result.PrefsParts[chunk.Part] = chunk.ContentB64
		case MaintenanceDone:
			var done MaintenanceDonePayload
			if err := strictUnmarshal(event.Payload, &done); err != nil {
				return MaintenanceResult{}, err
			}
			result.Done = &done
		case MaintenanceError:
			var failure MaintenanceErrorPayload
			if err := strictUnmarshal(event.Payload, &failure); err != nil {
				return MaintenanceResult{}, err
			}
			result.Err = &failure
		}
	}
	return result, nil
}

func strictUnmarshal(raw json.RawMessage, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("decode maintenance payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode maintenance payload: trailing JSON")
	}
	return nil
}
