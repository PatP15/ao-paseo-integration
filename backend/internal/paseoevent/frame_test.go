package paseoevent

import (
	"strings"
	"testing"
)

const testNonce = "a1b2c3d4e5f6"

func TestEncodeFramesNeverEmitsALineThatCanWrap(t *testing.T) {
	// The spike measured the remote PTY hard-wrapping at exactly 80 columns and
	// returning screen lines, so a frame wider than that arrives split at an
	// arbitrary offset and cannot be told from a frame that legitimately ended
	// at the column limit. Staying under the limit is the whole mitigation.
	frames, err := EncodeFrames(testNonce, []byte(strings.Repeat(`{"pad":"x"}`, 60)))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(frames) < 3 {
		t.Fatalf("want a multi-frame payload, got %d frames", len(frames))
	}
	for index, frame := range frames {
		if len(frame) > maxLineWidth {
			t.Fatalf("frame %d is %d columns, over %d: %q", index, len(frame), maxLineWidth, frame)
		}
		if !strings.HasPrefix(frame, tokenPrefix+testNonce+" ") {
			t.Fatalf("frame %d has no token: %q", index, frame)
		}
		if !strings.HasSuffix(frame, terminator) {
			t.Fatalf("frame %d has no terminator: %q", index, frame)
		}
	}
}

func TestDecodeRoundTripsAMultiFramePayload(t *testing.T) {
	payload := []byte(`{"schema":"ao.agent-event.v1","seq":1,"note":"` + strings.Repeat("y", 300) + `"}`)
	frames, err := EncodeFrames(testNonce, payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result := Decode(testNonce, frames)
	if len(result.Payloads) != 1 {
		t.Fatalf("payloads = %d, want 1 (%#v)", len(result.Payloads), result)
	}
	if got := string(result.Payloads[0].Data); got != string(payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
	if result.Payloads[0].Raw != strings.Join(frames, "\n") {
		t.Fatalf("raw = %q, want the frames as they arrived", result.Payloads[0].Raw)
	}
	if result.Malformed != 0 || result.Foreign != 0 || result.Incomplete != 0 {
		t.Fatalf("clean decode reported problems: %#v", result)
	}
}

func TestDecodeSurvivesPTYNoiseAndTheDoubledEmission(t *testing.T) {
	// Shaped after spike/fixtures/s1f-terminal-capture.json: a shell banner, an
	// 80-column-wrapped prompt line, and every emission appearing twice — once
	// as the shell's echo of the command and once as its output.
	frames, err := EncodeFrames(testNonce, []byte(`{"seq":1,"summary":"ok"}`))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	lines := []string{
		"",
		"The default interactive shell is now zsh.",
		"(base) worker:/var/folders/4q/T//ao-paseo/repo$ printf '" + frames[0],
		strings.Repeat("x", 80),
		frames[0],
		"(base) worker:/var/folders/4q/T//ao-paseo/repo$",
	}
	result := Decode(testNonce, lines)
	if len(result.Payloads) != 1 {
		t.Fatalf("payloads = %d, want 1 despite the doubled emission (%#v)", len(result.Payloads), result)
	}
	if result.Malformed != 0 {
		t.Fatalf("malformed = %d, want 0: surrounding output is not a frame", result.Malformed)
	}
}

func TestDecodeRefusesAnotherLaunchsFrames(t *testing.T) {
	frames, err := EncodeFrames("0f0f0f0f0f0f", []byte(`{"seq":1,"summary":"other launch"}`))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result := Decode(testNonce, frames)
	if len(result.Payloads) != 0 {
		t.Fatalf("payloads = %d, want 0: another launch's nonce is not this launch's report", len(result.Payloads))
	}
	if result.Foreign != len(frames) {
		t.Fatalf("foreign = %d, want %d", result.Foreign, len(frames))
	}
	if result.Malformed != 0 {
		t.Fatalf("malformed = %d: a well-formed foreign frame is not malformed", result.Malformed)
	}
}

func TestDecodeDropsTruncatedAndCorruptedFrames(t *testing.T) {
	frames, err := EncodeFrames(testNonce, []byte(`{"seq":1,"summary":"ok"}`))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	single := frames[0]

	// Field offsets are fixed by the frame geometry: token, nonce, space,
	// kkk/nnn, space, checksum, space, body.
	const checksumStart = len(tokenPrefix) + nonceLen + 1 + 7 + 1
	for name, line := range map[string]string{
		"terminator lost to a wrap": strings.TrimSuffix(single, terminator),
		"body cut mid-line":         single[:len(single)-6] + terminator,
		"checksum rewritten":        single[:checksumStart] + "deadbeef" + single[checksumStart+8:],
		"position out of range":     strings.Replace(single, " 001/001 ", " 002/001 ", 1),
		"header truncated":          tokenPrefix + testNonce + " 001/001" + terminator,
	} {
		result := Decode(testNonce, []string{line})
		if len(result.Payloads) != 0 {
			t.Fatalf("%s: payloads = %d, want 0", name, len(result.Payloads))
		}
		if result.Malformed != 1 {
			t.Fatalf("%s: malformed = %d, want 1 (%q)", name, result.Malformed, line)
		}
	}
}

func TestDecodeDropsAGroupWhoseChunkIsContradicted(t *testing.T) {
	frames, err := EncodeFrames(testNonce, []byte(strings.Repeat("z", 120)))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(frames) < 3 {
		t.Fatalf("want at least 3 frames, got %d", len(frames))
	}
	// Same position, different body: the group can no longer be trusted whole,
	// and half a report must never be applied.
	const bodyStart = len(tokenPrefix) + nonceLen + 1 + 7 + 1 + 8 + 1
	substitute := "AAAA"
	if frames[0][bodyStart:bodyStart+4] == substitute {
		substitute = "BBBB"
	}
	forged := frames[0][:bodyStart] + substitute + frames[0][bodyStart+4:]
	lines := append([]string{frames[0], forged}, frames[1:]...)

	result := Decode(testNonce, lines)
	if len(result.Payloads) != 0 {
		t.Fatalf("payloads = %d, want 0", len(result.Payloads))
	}
	if result.Malformed != 1 {
		t.Fatalf("malformed = %d, want 1 (%#v)", result.Malformed, result)
	}
}

func TestDecodeReportsWhereAnIncompleteGroupStarted(t *testing.T) {
	// A report split across a capture window must be re-read whole on the next
	// pass, so the cursor may not advance past its first line.
	frames, err := EncodeFrames(testNonce, []byte(strings.Repeat("w", 200)))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	lines := append([]string{"noise", "more noise"}, frames[:2]...)

	result := Decode(testNonce, lines)
	if len(result.Payloads) != 0 || result.Incomplete != 1 {
		t.Fatalf("result = %#v, want one incomplete group", result)
	}
	if result.FirstIncompleteLine != 2 {
		t.Fatalf("first incomplete line = %d, want 2", result.FirstIncompleteLine)
	}

	// The next window starts at that line and completes the report.
	completed := Decode(testNonce, frames)
	if len(completed.Payloads) != 1 || completed.FirstIncompleteLine != -1 {
		t.Fatalf("second pass = %#v, want the whole report", completed)
	}
}

func TestDecodeIgnoresOrdinaryOutput(t *testing.T) {
	result := Decode(testNonce, []string{
		"", "running tests...", "ok  	github.com/example/pkg	0.31s", "AO_EVENT is mentioned in prose",
	})
	if len(result.Payloads) != 0 || result.Malformed != 0 || result.Foreign != 0 {
		t.Fatalf("result = %#v, want a silent pass over prose", result)
	}
}

func TestEncodeFramesRefusesAnUnusablePayloadOrNonce(t *testing.T) {
	if _, err := EncodeFrames("short", []byte("{}")); err == nil {
		t.Fatal("want an error for a malformed nonce")
	}
	if _, err := EncodeFrames(testNonce, nil); err == nil {
		t.Fatal("want an error for an empty payload")
	}
	if _, err := EncodeFrames(testNonce, make([]byte, MaxPayloadBytes+1)); err == nil {
		t.Fatal("want an error for an oversized payload")
	}
}

func TestDecodeWithoutAValidNonceReadsNothing(t *testing.T) {
	frames, err := EncodeFrames(testNonce, []byte(`{"seq":1}`))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result := Decode("", frames)
	if len(result.Payloads) != 0 {
		t.Fatalf("payloads = %d, want 0: without a contract nothing can be authenticated", len(result.Payloads))
	}
	if result.Foreign != len(frames) {
		t.Fatalf("foreign = %d, want %d", result.Foreign, len(frames))
	}
}
