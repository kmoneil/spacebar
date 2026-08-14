// Copyright 2026 Kevin O'Neil
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package output

import (
	"strings"
	"testing"
)

// The hostile inputs are written as Go escapes rather than as the characters
// themselves. Several of them are invisible, and a table where the difference
// between two rows cannot be seen is a table nobody can review.
func TestSanitize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "ordinary text is untouched",
			in:   "deploy done",
			want: "deploy done",
		},
		{
			// The reason the escaper does not reach for strconv.Quote, which
			// would turn all of this into hex.
			name: "every script and emoji survives",
			in:   "déjà vu ✅ 日本語 Привет \U0001F389",
			want: "déjà vu ✅ 日本語 Привет \U0001F389",
		},
		{
			name: "a tab and a newline survive",
			in:   "one\ttwo\nthree",
			want: "one\ttwo\nthree",
		},
		{
			// Erase in display. A message body carrying this would clear the
			// terminal of whoever read it.
			name: "a CSI sequence is shown, not obeyed",
			in:   "hi\x1b[2Jthere",
			want: `hi\x1b[2Jthere`,
		},
		{
			// Set window title, and on some terminals a way to put text into
			// the input buffer that the operator did not type.
			name: "an OSC sequence is shown, not obeyed",
			in:   "hi\x1b]0;pwned\x07there",
			want: `hi\x1b]0;pwned\x07there`,
		},
		{
			name: "a lone escape byte is shown",
			in:   "\x1b",
			want: `\x1b`,
		},
		{
			// Returns the cursor to the start of the line, so what follows
			// overwrites what this tool printed a moment ago.
			name: "a bare carriage return is escaped",
			in:   "real output\rfake output",
			want: `real output\rfake output`,
		},
		{
			name: "a carriage return before a newline is escaped and the newline is not",
			in:   "one\r\ntwo",
			want: "one\\r\ntwo",
		},
		{
			// The eight-bit form of the control sequence introducer. A
			// terminal in that mode acts on it exactly as on ESC and a
			// bracket, and an escaper that only knew about ESC would pass it.
			name: "the C1 control sequence introducer is escaped",
			in:   "hi\u009b2Jthere",
			want: `hi\u009b2Jthere`,
		},
		{
			name: "NUL and DEL are escaped",
			in:   "a\x00b\x7fc",
			want: `a\x00b\x7fc`,
		},
		{
			// Trojan Source. The bytes say one thing and the terminal paints
			// another, which in a tool that prints space names beside message
			// bodies is a way to make one look like another.
			name: "a right-to-left override is escaped",
			in:   "spaces/AAAA\u202ereal",
			want: `spaces/AAAA\u202ereal`,
		},
		{
			name: "a directional isolate is escaped",
			in:   "a\u2066b\u2069c",
			want: `a\u2066b\u2069c`,
		},
		{
			// Not replaced with U+FFFD: a value altered to make it printable
			// is a wrong answer that looks like a right one.
			name: "an invalid UTF-8 byte is written as the byte it is",
			in:   "ok \xff\xfe bad",
			want: `ok \xff\xfe bad`,
		},
		{
			name: "a real replacement character is left alone",
			in:   "ok � bad",
			want: "ok � bad",
		},
		{
			name: "an empty string stays empty",
			in:   "",
			want: "",
		},
		{
			name: "an escape at the very start is caught",
			in:   "\x1b[31mred",
			want: `\x1b[31mred`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.in); got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCellAlsoEscapesTheSeparators is the difference between text that is safe
// to display and text that is safe to display inside a structure.
func TestCellAlsoEscapesTheSeparators(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"one\ttwo", `one\ttwo`},
		{"one\ntwo", `one\ntwo`},
		{"one\r\ntwo", `one\r\ntwo`},
		{"plain", "plain"},
		{"hi\x1b[2Jthere", `hi\x1b[2Jthere`},
	}

	for _, tc := range cases {
		if got := Cell(tc.in); got != tc.want {
			t.Errorf("Cell(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// hostile is the set both functions have to defuse, used by the property tests
// below and by the fuzz seed corpus.
var hostile = []string{
	"\x1b[2J",
	"\x1b]0;title\x07",
	"\u009b2J",
	"a\rb",
	"\x00\x01\x02\x1f\x7f",
	"\u202ereversed",
	"\xff\xfe",
}

// TestNothingEscapedKeepsAnEscape states the property once over every hostile
// input, rather than checking it case by case where one row can be forgotten.
func TestNothingEscapedKeepsAnEscape(t *testing.T) {
	for _, in := range hostile {
		for name, got := range map[string]string{"Sanitize": Sanitize(in), "Cell": Cell(in)} {
			if strings.ContainsAny(got, "\x1b\u009b\x00\r\x7f") {
				t.Errorf("%s(%q) = %q, which still holds a control character", name, in, got)
			}
		}
	}
}

// FuzzSanitize states the invariant the table cannot: whatever the input,
// nothing a terminal acts on comes out.
func FuzzSanitize(f *testing.F) {
	for _, seed := range append([]string{"", "plain", "one\ttwo\nthree"}, hostile...) {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		for name, got := range map[string]string{"Sanitize": Sanitize(in), "Cell": Cell(in)} {
			for _, r := range got {
				if r == 0x1b || r == 0x9b || r == '\r' || r == 0x7f || (r < 0x20 && r != '\t' && r != '\n') {
					t.Fatalf("%s(%q) = %q, which holds %U", name, in, got, r)
				}
				if bidi[r] {
					t.Fatalf("%s(%q) = %q, which holds the bidi control %U", name, in, got, r)
				}
			}
		}

		// Cell goes further: it may hold neither separator, or a value could
		// forge a column or a whole row.
		if cell := Cell(in); strings.ContainsAny(cell, "\t\n") {
			t.Fatalf("Cell(%q) = %q, which holds a separator", in, cell)
		}
	})
}
