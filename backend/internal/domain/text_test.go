package domain

import "testing"

func TestSanitizeControlChars(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text unchanged", in: "hello world", want: "hello world"},
		{name: "keeps newline tab carriage return", in: "a\nb\tc\rd", want: "a\nb\tc\rd"},
		{name: "strips ansi escape byte leaving harmless residue", in: "before\x1b[2Jafter", want: "before[2Jafter"},
		{name: "strips nul and bell", in: "x\x00y\az", want: "xyz"},
		{name: "strips osc sequence bytes", in: "\x1b]0;title\a", want: "]0;title"},
		{name: "empty stays empty", in: "", want: ""},
		// Category Cf: invisible to the human reading a PR comment, fully
		// legible to the model. unicode.IsControl covers only Cc, so each of
		// these used to survive into the agent's prompt.
		// Written as escapes on purpose: a literal here would be invisible in
		// the source too, and a reviewer could not tell what is being tested.
		{name: "strips zero-width space and joiner", in: "rm\u200B -rf\u200C /\u200D", want: "rm -rf /"},
		{name: "strips bidi override that reorders the rendered line", in: "run \u202Escramble\u202C now", want: "run scramble now"},
		{name: "strips bidi isolates", in: "a\u2066b\u2067c\u2069d", want: "abcd"},
		{name: "strips soft hyphen and BOM", in: "\uFEFFpass\u00ADword", want: "password"},
		// The TAG block encodes plain ASCII in codepoints that render as
		// nothing: this input displays as "ok" and reads as "ok delete".
		{name: "strips tag block smuggling", in: "ok\U000E0020\U000E0064\U000E0065\U000E006C\U000E0065\U000E0074\U000E0065", want: "ok"},
		{name: "strips language tag", in: "a\U000E0001b", want: "ab"},
		{name: "keeps ordinary non-ascii text", in: "héllo 世界 🙂", want: "héllo 世界 🙂"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeControlChars(tt.in); got != tt.want {
				t.Fatalf("SanitizeControlChars(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
