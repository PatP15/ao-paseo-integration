// Package paseoevent implements AO's agent-to-control-plane report channel: the
// immutable run brief that establishes the contract, the on-wire framing, and
// the ingest path that turns frames into durable AO facts.
//
// Everything here is AO-owned vocabulary. Despite the package name there are no
// Paseo types in it — the remote read surfaces arrive as ports interfaces, so
// the protocol is testable without a CLI, a daemon, or a host.
//
// The channel is advisory by construction. A report may set activity state, file
// a question, and record progress evidence. It may never authorize a kill, an
// archive, a cleanup, a merge, a force-push, a permission decision, a host
// reassignment, or a retry-budget change. That is not a policy the ingest path
// checks at runtime, it is a shape: the Store interface it holds has no method
// that can do any of those things. The reason is that the transport is
// forgeable — anything able to write to an agent's transcript can replay another
// agent's reports — so nothing irreversible may rest on one.
package paseoevent

import (
	"encoding/base64"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
)

// Frame geometry.
//
// The spike measured the remote PTY hard-wrapping at exactly 80 columns and
// returning *screen* lines, so an over-wide report arrives split at an arbitrary
// byte offset and the "line is exactly COLS wide, so it continues" heuristic is
// ambiguous — a payload can legitimately end at the column limit. The fix is to
// never emit a line that can wrap: every frame is padded out to at most
// maxLineWidth columns, and the payload is chunked to fit.
//
// PROTOCOL.md §1 specifies 76-character base64 chunks. That number predates the
// measured column width: 76 characters of chunk plus a header is over 80 and
// wraps, which is the exact failure the chunking exists to prevent. The
// invariant that matters is the emitted *line* width, so that is what is fixed
// here at 76 columns, and the chunk is whatever is left after the header.
const (
	tokenPrefix  = "AO_EVENT_"
	nonceLen     = 12
	terminator   = ";"
	maxLineWidth = 76
	// headerWidth is "AO_EVENT_<nonce> kkk/nnn <crc32> ".
	headerWidth = len(tokenPrefix) + nonceLen + 1 + 7 + 1 + 8 + 1
	chunkLen    = maxLineWidth - headerWidth - len(terminator)
	maxChunks   = 999

	// MaxPayloadBytes caps one decoded report. A report is a pointer to work,
	// not the work itself, so the cap is small on purpose: detail belongs in the
	// repository, in a commit, or in a follow-up fetch keyed by event id.
	MaxPayloadBytes = 2048

	// NoncePlaceholder is what a brief's worked example carries in place of a
	// real nonce. It can never match a launch nonce, which is what keeps AO from
	// ingesting the instructions it just sent.
	NoncePlaceholder = "<NONCE>"
)

// EncodeFrames renders one report payload as the lines an emitter writes, in
// order. Callers use it to state the contract in a brief and to drive tests; AO
// itself only ever decodes.
func EncodeFrames(nonce string, payload []byte) ([]string, error) {
	if err := ValidateNonce(nonce); err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("report payload is empty")
	}
	if len(payload) > MaxPayloadBytes {
		return nil, fmt.Errorf("report payload is %d bytes, over the %d byte cap", len(payload), MaxPayloadBytes)
	}
	body := base64.StdEncoding.EncodeToString(payload)
	total := (len(body) + chunkLen - 1) / chunkLen
	if total > maxChunks {
		return nil, fmt.Errorf("report payload needs %d frames, over the %d frame cap", total, maxChunks)
	}
	sum := crc32.ChecksumIEEE([]byte(body))
	frames := make([]string, 0, total)
	for index := 1; index <= total; index++ {
		start := (index - 1) * chunkLen
		end := start + chunkLen
		if end > len(body) {
			end = len(body)
		}
		frames = append(frames, fmt.Sprintf("%s%s %03d/%03d %08x %s%s",
			tokenPrefix, nonce, index, total, sum, body[start:end], terminator))
	}
	return frames, nil
}

// ValidateNonce enforces the launch-nonce shape: exactly nonceLen lowercase hex
// characters, so a nonce cannot contain a space, a frame separator, or the
// placeholder.
func ValidateNonce(nonce string) error {
	if len(nonce) != nonceLen {
		return fmt.Errorf("report nonce must be %d characters, got %d", nonceLen, len(nonce))
	}
	for _, char := range nonce {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return fmt.Errorf("report nonce must be lowercase hex")
		}
	}
	return nil
}

// DecodedPayload is one complete, checksum-verified report body together with
// the exact lines that carried it.
type DecodedPayload struct {
	Data []byte
	// Raw is the frame lines as they arrived, newline joined. It is stored
	// before the report is applied, so what AO acted on stays auditable even if
	// the decode was wrong.
	Raw string
}

// DecodeResult is the outcome of decoding one window of remote lines. Every
// counter is a fact worth surfacing: a rising Malformed count means the emitter
// or the terminal is misbehaving, and a rising Foreign count on a quiet session
// means something is replaying another launch's reports.
type DecodeResult struct {
	Payloads  []DecodedPayload
	Malformed int
	Foreign   int
	// Incomplete counts frame groups still missing chunks when the window ended.
	Incomplete int
	// FirstIncompleteLine is the window-relative index of the earliest line
	// belonging to an incomplete group, or -1 when every group completed. A
	// cursored caller must not advance past it, or the rest of that report is
	// never read.
	FirstIncompleteLine int
}

type frameGroup struct {
	total     int
	chunks    map[int]string
	raw       []string
	firstLine int
	broken    bool
}

// Decode extracts every complete report in lines that carries nonce.
//
// It is deliberately forgiving about what surrounds a frame and strict about the
// frame itself. Lines with no token at all are ordinary output and are ignored,
// not counted: a transcript is mostly prose. A frame under another nonce is
// counted and dropped — that is the path AO's own brief example takes. A frame
// under this nonce that does not parse, does not carry its terminator, or fails
// its checksum is counted as malformed and dropped whole; a partially applied
// report is worse than a missing one.
func Decode(nonce string, lines []string) DecodeResult {
	result := DecodeResult{FirstIncompleteLine: -1}
	if ValidateNonce(nonce) != nil {
		// Without a valid launch nonce nothing can be authenticated, so every
		// frame present is foreign by definition.
		for _, line := range lines {
			if strings.Contains(line, tokenPrefix) {
				result.Foreign++
			}
		}
		return result
	}
	token := tokenPrefix + nonce + " "
	groups := make(map[string]*frameGroup)
	completed := make(map[string]struct{})

	for index, line := range lines {
		offset := strings.Index(line, tokenPrefix)
		if offset < 0 {
			continue
		}
		frame := line[offset:]
		if !strings.HasPrefix(frame, token) {
			// Either another launch's nonce or the brief's <NONCE> placeholder
			// echoed back. Both are somebody else's bytes.
			result.Foreign++
			continue
		}
		chunk, err := parseFrame(strings.TrimSpace(frame[len(token):]))
		if err != nil {
			result.Malformed++
			continue
		}
		key := chunk.key()
		if _, done := completed[key]; done {
			// The spike found each emission appearing twice, once as the shell's
			// echo of the command and once as its output. The second copy is
			// dropped here; a report that still slips through twice is caught by
			// the emitter-minted event id at the store.
			continue
		}
		group, known := groups[key]
		if !known {
			group = &frameGroup{total: chunk.total, chunks: make(map[int]string, chunk.total), firstLine: index}
			groups[key] = group
		}
		if existing, seen := group.chunks[chunk.index]; seen {
			if existing != chunk.body {
				// Two different bodies claiming the same position: the group can
				// no longer be trusted as a whole.
				group.broken = true
			}
			continue
		}
		group.chunks[chunk.index] = chunk.body
		group.raw = append(group.raw, frame)
		if len(group.chunks) < group.total {
			continue
		}
		completed[key] = struct{}{}
		delete(groups, key)
		if group.broken {
			result.Malformed++
			continue
		}
		payload, err := group.assemble(chunk.checksum)
		if err != nil {
			result.Malformed++
			continue
		}
		result.Payloads = append(result.Payloads, DecodedPayload{Data: payload, Raw: strings.Join(group.raw, "\n")})
	}

	for _, group := range groups {
		if group.broken {
			result.Malformed++
			continue
		}
		result.Incomplete++
		if result.FirstIncompleteLine < 0 || group.firstLine < result.FirstIncompleteLine {
			result.FirstIncompleteLine = group.firstLine
		}
	}
	return result
}

type frameChunk struct {
	index    int
	total    int
	checksum uint32
	body     string
}

func (c frameChunk) key() string {
	return fmt.Sprintf("%08x/%03d", c.checksum, c.total)
}

func parseFrame(rest string) (frameChunk, error) {
	if !strings.HasSuffix(rest, terminator) {
		// The terminator is how a truncated line is told from a short final
		// chunk. Without it the frame may have been cut by a wrap or a
		// scrollback boundary.
		return frameChunk{}, fmt.Errorf("frame is missing its terminator")
	}
	fields := strings.Fields(strings.TrimSuffix(rest, terminator))
	if len(fields) != 3 {
		return frameChunk{}, fmt.Errorf("frame has %d fields, want 3", len(fields))
	}
	position, checksum, body := fields[0], fields[1], fields[2]
	indexText, totalText, found := strings.Cut(position, "/")
	if !found {
		return frameChunk{}, fmt.Errorf("frame position %q is not k/n", position)
	}
	index, err := strconv.Atoi(indexText)
	if err != nil {
		return frameChunk{}, fmt.Errorf("frame index %q is not a number", indexText)
	}
	total, err := strconv.Atoi(totalText)
	if err != nil {
		return frameChunk{}, fmt.Errorf("frame total %q is not a number", totalText)
	}
	if total < 1 || total > maxChunks || index < 1 || index > total {
		return frameChunk{}, fmt.Errorf("frame position %d/%d is out of range", index, total)
	}
	if len(checksum) != 8 {
		return frameChunk{}, fmt.Errorf("frame checksum %q is not 8 hex characters", checksum)
	}
	sum, err := strconv.ParseUint(checksum, 16, 32)
	if err != nil {
		return frameChunk{}, fmt.Errorf("frame checksum %q is not hex", checksum)
	}
	if len(body) > chunkLen {
		return frameChunk{}, fmt.Errorf("frame body is %d characters, over %d", len(body), chunkLen)
	}
	if index < total && len(body) != chunkLen {
		// Only the last chunk may be short. A short interior chunk means the
		// line lost characters in transit.
		return frameChunk{}, fmt.Errorf("interior frame body is %d characters, want %d", len(body), chunkLen)
	}
	return frameChunk{index: index, total: total, checksum: uint32(sum), body: body}, nil
}

func (g *frameGroup) assemble(checksum uint32) ([]byte, error) {
	var body strings.Builder
	for index := 1; index <= g.total; index++ {
		chunk, ok := g.chunks[index]
		if !ok {
			return nil, fmt.Errorf("frame group is missing chunk %d", index)
		}
		body.WriteString(chunk)
	}
	encoded := body.String()
	if crc32.ChecksumIEEE([]byte(encoded)) != checksum {
		return nil, fmt.Errorf("frame group failed its checksum")
	}
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("frame group is not base64: %w", err)
	}
	if len(payload) > MaxPayloadBytes {
		return nil, fmt.Errorf("report payload is %d bytes, over the %d byte cap", len(payload), MaxPayloadBytes)
	}
	return payload, nil
}
