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
	for _, seed := range append([]string{
		"", "plain", "one\ttwo\nthree",

		// Hidden text, and the two invisible characters real writing needs, so
		// that a change tightening this has to keep them.
		"deploy done\U000e0049\U000e0047",
		"\U0001F468\u200d\U0001F469\u200d\U0001F467",
		"می\u200cروم",
	}, hostile...) {
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
				if r >= tagsFirst && r <= tagsLast {
					t.Fatalf("%s(%q) = %q, which holds the tag character %U: text that is in the "+
						"message and not on the screen", name, in, got, r)
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

// TestHiddenTextIsShownRatherThanObeyedOrDropped.
//
// The Tags block is deprecated, rendered as nothing everywhere, and carries a
// full ASCII alphabet, so a run of it is text that is in the message, absent
// from the screen, and perfectly legible to anything reading codepoints. It is
// the standard carrier for an instruction aimed at a model.
//
// What changed to make it matter is not the block. It is that a message body now
// goes to a model as well as to a terminal, and a model reads codepoints.
func TestHiddenTextIsShownRatherThanObeyedOrDropped(t *testing.T) {
	// "IGNORE" in tag letters, which is what this looks like on the wire.
	var hidden strings.Builder
	for _, r := range "IGNORE" {
		hidden.WriteRune(rune(0xE0000) + r)
	}
	body := "deploy done" + hidden.String()

	got := Sanitize(body)
	if strings.ContainsRune(got, 0xE0049) {
		t.Errorf("a tag character survived into terminal output: %q", got)
	}
	if !strings.Contains(got, `\U000e0049`) {
		t.Errorf("the hidden text is not shown as an escape:\n%q", got)
	}
	if !strings.Contains(got, "deploy done") {
		t.Errorf("the visible text did not survive: %q", got)
	}

	// Shown rather than dropped. The escape is longer than what it replaced, so
	// a reader sees that something is there, which is the whole point: removing
	// it would make the terminal clean and the operator wrong.
	if len(got) <= len(body) {
		t.Errorf("the hidden text was removed rather than shown: %q", got)
	}

	// Cell escapes it too, because a list is where a body is most likely read.
	if cell := Cell(body); !strings.Contains(cell, `\U000e0049`) {
		t.Errorf("Cell did not show the hidden text: %q", cell)
	}
}

// TestTheCharactersRealTextNeedsAreLeftAlone is the other half, and it is the
// half that keeps this from being a defence somebody turns off.
//
// The obvious wider set of invisible characters is a trap. A zero-width joiner
// is what makes a family emoji one glyph, and the non-joiner is required by
// Persian and several Indic scripts. Escaping those would garble ordinary
// messages written by ordinary people.
func TestTheCharactersRealTextNeedsAreLeftAlone(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"a family emoji, which is zero-width joiners", "\U0001F468\u200d\U0001F469\u200d\U0001F467"},
		{"a Persian non-joiner", "\u0645\u06cc\u200c\u0631\u0648\u0645"},
		{"an ordinary sentence", "deploy done"},
		{"accents and other scripts", "café 日本語 Ελληνικά"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.text); got != tc.text {
				t.Errorf("Sanitize(%q) = %q, and it should have been left alone", tc.text, got)
			}
		})
	}
}
