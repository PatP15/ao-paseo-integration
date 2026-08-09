package paseoevent

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	// MaintenanceRepoFile is one instruction file's content hash at the
	// checkout's base branch.
	MaintenanceRepoFile = "repo_file"
	// MaintenanceSkillFile is one chunk of one skill file, worker → AO.
	MaintenanceSkillFile = "skill_file"
	// MaintenancePushFile is one chunk of one skill file, AO → worker, typed
	// into the terminal as input. It is deliberately a DIFFERENT kind from
	// skill_file: the PTY echoes input back into the capture, so the outbound
	// parser must be able to recognize and skip AO's own inbound frames.
	MaintenancePushFile = "push_file"
	// MaintenancePushEnd terminates an inbound push stream.
	MaintenancePushEnd = "push_end"
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
// Home is the worker's home directory, reported by every verb so AO learns
// where to place later maintenance workspaces without a separate bootstrap.
type MaintenanceDonePayload struct {
	Count  int    `json:"count,omitempty"`
	Parts  int    `json:"parts,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Exists bool   `json:"exists,omitempty"`
	Home   string `json:"home,omitempty"`
	// Head is the checkout's HEAD commit for repo operations.
	Head string `json:"head,omitempty"`
}

// MaintenanceErrorPayload names why the worker refused or failed.
type MaintenanceErrorPayload struct {
	Message string `json:"message"`
}

// MaintenanceRepoFilePayload is one instruction file's identity at the
// checkout's base branch: its repo-relative path and the sha256 of its
// committed content there.
type MaintenanceRepoFilePayload struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// MaintenanceFileChunkPayload is one chunk of one named file, used by both
// transfer directions (kinds skill_file and push_file). SHA256 is the digest
// of the COMPLETE file and rides every chunk so a receiver can verify the
// assembled file no matter which chunk arrived last.
type MaintenanceFileChunkPayload struct {
	Path       string `json:"path"`
	Part       int    `json:"part"`
	Parts      int    `json:"parts"`
	ContentB64 string `json:"contentB64"`
	SHA256     string `json:"sha256"`
}

// MaintenancePushEndPayload closes an inbound push stream; Files is the total
// the receiver must have assembled and verified.
type MaintenancePushEndPayload struct {
	Files int `json:"files"`
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
	case MaintenanceSkill, MaintenancePrefs, MaintenanceRepoFile, MaintenanceSkillFile,
		MaintenancePushFile, MaintenancePushEnd, MaintenanceDone, MaintenanceError:
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
	RepoFiles  []MaintenanceRepoFilePayload
	FileChunks []MaintenanceFileChunkPayload
	Done       *MaintenanceDonePayload
	Err        *MaintenanceErrorPayload
}

// ParseMaintenanceRun decodes the frame lines of one maintenance invocation.
// It returns an incomplete result (Done and Err both nil) when the terminating
// event has not arrived yet, so a caller can keep polling the capture.
func ParseMaintenanceRun(nonce string, lines []string) (MaintenanceResult, error) {
	result := MaintenanceResult{PrefsParts: map[int]string{}}
	decoded := Decode(nonce, lines)
	// Dedupe per (kind, seq), never per seq alone: the PTY echoes AO's own
	// inbound push frames into the same capture, and the two directions each
	// number from 1 — a shared-seq dedupe would drop the worker's real output
	// as duplicates of the echoed input (found live).
	seen := map[string]bool{}
	for _, payload := range decoded.Payloads {
		event, err := DecodeMaintenanceEvent(payload.Data)
		if err != nil {
			return MaintenanceResult{}, err
		}
		key := fmt.Sprintf("%s:%d", event.Kind, event.Seq)
		if seen[key] {
			continue
		}
		seen[key] = true
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
		case MaintenanceRepoFile:
			var repoFile MaintenanceRepoFilePayload
			if err := strictUnmarshal(event.Payload, &repoFile); err != nil {
				return MaintenanceResult{}, err
			}
			if repoFile.Path == "" || repoFile.SHA256 == "" {
				return MaintenanceResult{}, fmt.Errorf("maintenance repo_file event is malformed")
			}
			result.RepoFiles = append(result.RepoFiles, repoFile)
		case MaintenanceSkillFile:
			var chunk MaintenanceFileChunkPayload
			if err := strictUnmarshal(event.Payload, &chunk); err != nil {
				return MaintenanceResult{}, err
			}
			if chunk.Path == "" || chunk.Part < 1 || chunk.Parts < chunk.Part || chunk.SHA256 == "" {
				return MaintenanceResult{}, fmt.Errorf("maintenance skill_file chunk is malformed")
			}
			result.FileChunks = append(result.FileChunks, chunk)
		case MaintenancePushFile, MaintenancePushEnd:
			// AO's own inbound frames, echoed back by the PTY. Not output.
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

// MaintenanceFile is one complete, hash-verified transferred file.
type MaintenanceFile struct {
	Path    string
	Content []byte
}

// AssembleFileChunks reorders and concatenates per-file chunks and verifies
// each assembled file against the digest its chunks declared. Order-stable by
// first appearance, so a directory round-trips in a deterministic order.
func AssembleFileChunks(chunks []MaintenanceFileChunkPayload) ([]MaintenanceFile, error) {
	type pending struct {
		parts  map[int]string
		total  int
		sha256 string
	}
	order := []string{}
	byPath := map[string]*pending{}
	for _, chunk := range chunks {
		entry, ok := byPath[chunk.Path]
		if !ok {
			entry = &pending{parts: map[int]string{}, total: chunk.Parts, sha256: chunk.SHA256}
			byPath[chunk.Path] = entry
			order = append(order, chunk.Path)
		}
		if entry.total != chunk.Parts || entry.sha256 != chunk.SHA256 {
			return nil, fmt.Errorf("file %s chunks disagree about their whole", chunk.Path)
		}
		entry.parts[chunk.Part] = chunk.ContentB64
	}
	files := make([]MaintenanceFile, 0, len(order))
	for _, path := range order {
		entry := byPath[path]
		var content []byte
		for part := 1; part <= entry.total; part++ {
			encoded, ok := entry.parts[part]
			if !ok {
				return nil, fmt.Errorf("file %s is missing part %d of %d", path, part, entry.total)
			}
			raw, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, fmt.Errorf("file %s part %d is not valid base64: %w", path, part, err)
			}
			content = append(content, raw...)
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != entry.sha256 {
			return nil, fmt.Errorf("file %s arrived corrupted: content does not match its declared sha256", path)
		}
		files = append(files, MaintenanceFile{Path: path, Content: content})
	}
	return files, nil
}

// EncodeFileChunkEvents frames one file as chunk events of the given kind
// (skill_file outbound, push_file inbound), starting at seq and returning the
// next free seq.
func EncodeFileChunkEvents(nonce, kind, path string, content []byte, seq int) ([]string, int, error) {
	digest := sha256.Sum256(content)
	sum := hex.EncodeToString(digest[:])
	chunks := SplitPrefsChunks(content)
	if len(chunks) == 0 {
		chunks = [][]byte{{}}
	}
	var lines []string
	for index, chunk := range chunks {
		frames, err := EncodeMaintenanceEvent(nonce, seq, kind, MaintenanceFileChunkPayload{
			Path: path, Part: index + 1, Parts: len(chunks),
			ContentB64: base64.StdEncoding.EncodeToString(chunk), SHA256: sum,
		})
		if err != nil {
			return nil, seq, err
		}
		lines = append(lines, frames...)
		seq++
	}
	return lines, seq, nil
}

// ParseMaintenancePush decodes an inbound push stream (kinds push_file and
// push_end) from accumulated terminal-input lines. It returns (nil, false,
// nil) while the end marker has not arrived, so a reader can keep
// accumulating; any malformed or foreign content among this nonce's frames is
// an error, because a partially applied push is worse than a refused one.
func ParseMaintenancePush(nonce string, lines []string) ([]MaintenanceFile, bool, error) {
	decoded := Decode(nonce, lines)
	var chunks []MaintenanceFileChunkPayload
	declared := -1
	seen := map[int]bool{}
	for _, payload := range decoded.Payloads {
		event, err := DecodeMaintenanceEvent(payload.Data)
		if err != nil {
			return nil, false, err
		}
		if seen[event.Seq] {
			continue
		}
		seen[event.Seq] = true
		switch event.Kind {
		case MaintenancePushFile:
			var chunk MaintenanceFileChunkPayload
			if err := strictUnmarshal(event.Payload, &chunk); err != nil {
				return nil, false, err
			}
			chunks = append(chunks, chunk)
		case MaintenancePushEnd:
			var end MaintenancePushEndPayload
			if err := strictUnmarshal(event.Payload, &end); err != nil {
				return nil, false, err
			}
			declared = end.Files
		default:
			return nil, false, fmt.Errorf("unexpected %s event in an inbound push stream", event.Kind)
		}
	}
	if declared < 0 {
		return nil, false, nil
	}
	files, err := AssembleFileChunks(chunks)
	if err != nil {
		return nil, false, err
	}
	if len(files) != declared {
		return nil, false, fmt.Errorf("push declared %d files but carried %d", declared, len(files))
	}
	return files, true, nil
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
