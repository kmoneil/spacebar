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

package format

import (
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/output"
)

// What has not been verified, stated once here rather than implied by absence.
//
// SPEC.md §9 describes the markup Chat renders. Every expectation below is
// taken from it and from the shapes it gives, and none of it has been checked
// against a live space, because that needs a webhook credential and a real
// space and the tests here never touch the network. Two answers in particular
// are assumed rather than known: what Chat does with an asterisk that opens
// nothing, and whether any escape mechanism exists inside a link wrapper. This
// package assumes there is none and refuses what it cannot represent, which is
// the conservative reading. If a live check shows otherwise, Link is the one
// function that changes.

func translate(t *testing.T, src string) (string, []string) {
	t.Helper()
	got, warnings, err := Translate(src)
	if err != nil {
		t.Fatalf("Translate(%q): %v", src, err)
	}
	return got, warnings
}

// TestNothingInsideACodeSpanIsTransformed is the test this package exists for.
//
// SPEC.md §9 names it as the most likely bug here, and it is: every other rule
// in this file is a substitution, and a substitution that does not know where
// it is will happily rewrite the inside of somebody's shell command.
func TestNothingInsideACodeSpanIsTransformed(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "an inline span keeps its asterisks",
			src:  "see `**not bold**` here",
			want: "see `**not bold**` here",
		},
		{
			name: "an inline span keeps its underscores and tildes",
			src:  "run `__init__` and `~~x~~`",
			want: "run `__init__` and `~~x~~`",
		},
		{
			name: "an inline span keeps something that looks like a link",
			src:  "write `[text](url)` like that",
			want: "write `[text](url)` like that",
		},
		{
			name: "a fenced block keeps everything",
			src:  "```\n**bold** and [link](https://x.invalid)\n```",
			want: "```\n**bold** and [link](https://x.invalid)\n```",
		},
		{
			name: "a fenced block with a language keeps everything",
			src:  "```go\nvar x = *p // **not bold**\n```",
			want: "```go\nvar x = *p // **not bold**\n```",
		},
		{
			name: "translation resumes after a fence closes",
			src:  "```\n**kept**\n```\n**translated**",
			want: "```\n**kept**\n```\n*translated*",
		},
		{
			name: "a double backtick span may contain a single backtick",
			src:  "``a ` b **c**`` and **d**",
			want: "``a ` b **c**`` and *d*",
		},
		{
			name: "a span inside emphasis survives",
			src:  "**bold with `*stars*` inside**",
			want: "*bold with `*stars*` inside*",
		},
		{
			name: "a code span stops emphasis from closing across it",
			src:  "*italic `a * b` still italic*",
			want: "_italic `a * b` still italic_",
		},
		{
			name: "an unterminated backtick is literal",
			src:  "a ` stray **bold**",
			want: "a ` stray *bold*",
		},
		{
			name: "an unterminated fence keeps the rest verbatim",
			src:  "```\n**kept**\nstill kept",
			want: "```\n**kept**\nstill kept",
		},
		{
			name: "an indented fence still counts",
			src:  "   ```\n**kept**\n   ```",
			want: "   ```\n**kept**\n   ```",
		},
		{
			name: "a span inside a heading survives",
			src:  "## the `**flag**`",
			want: "*the `**flag**`*",
		},
		{
			name: "a span inside a list item survives",
			src:  "- run `make **all**`",
			want: "- run `make **all**`",
		},
		{
			name: "a span inside a link text survives",
			src:  "[the `**flag**`](https://x.invalid)",
			want: "<https://x.invalid|the `**flag**`>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := translate(t, tc.src)
			if got != tc.want {
				t.Errorf("Translate(%q)\n got %q\nwant %q", tc.src, got, tc.want)
			}
		})
	}
}

// TestEveryConversion is SPEC.md §9's table, row by row.
func TestEveryConversion(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		// The one that inverts. A single asterisk is italic on the way in and
		// bold on the way out, so a tool that passes it through says the
		// opposite of what was written.
		{"double asterisk is bold", "**bold**", "*bold*"},
		{"double underscore is bold", "__bold__", "*bold*"},
		{"single asterisk is italic", "*italic*", "_italic_"},
		{"single underscore is italic", "_italic_", "_italic_"},
		{"double tilde is strikethrough", "~~gone~~", "~gone~"},
		{"single tilde is left alone", "~gone~", "~gone~"},

		{"inline code is unchanged", "`code`", "`code`"},
		{"a link becomes a wrapper", "[text](https://x.invalid)", "<https://x.invalid|text>"},
		{"a link whose text is its url is written bare", "[https://x.invalid](https://x.invalid)", "https://x.invalid"},
		{"an empty link text is written bare", "[](https://x.invalid)", "https://x.invalid"},

		{"a heading becomes a bold line", "# Deploy", "*Deploy*"},
		{"a deeper heading becomes a bold line too", "### Deploy", "*Deploy*"},
		{"a closing hash run is decoration", "## Deploy ##", "*Deploy*"},
		{"six hashes is still a heading", "###### Deploy", "*Deploy*"},
		{"seven hashes is not", "####### Deploy", "####### Deploy"},
		{"a hash with no space is not a heading", "#deploys", "#deploys"},
		{"an empty heading is left alone", "#", "#"},

		{"a bullet keeps its marker", "- item", "- item"},
		{"an asterisk bullet keeps its marker", "* item", "* item"},
		{"a plus bullet keeps its marker", "+ item", "+ item"},
		{"a bullet's content is translated", "- **item**", "- *item*"},
		{"a quote keeps its marker", "> quoted", "> quoted"},
		{"a quote's content is translated", "> **quoted**", "> *quoted*"},

		{"emphasis inside a word", "a**b**c", "a*b*c"},
		{"emphasis spanning a link", "**see [it](https://x.invalid)**", "*see <https://x.invalid|it>*"},
		{"nested emphasis", "**bold with _italic_ inside**", "*bold with _italic_ inside*"},
		{"two runs on one line", "**a** and **b**", "*a* and *b*"},

		// Flanking. Without these an ordinary sentence turns into markup.
		{"multiplication is not italic", "2 * 3 * 4", "2 * 3 * 4"},
		{"a trailing asterisk opens nothing", "ends with *", "ends with *"},
		{"an unclosed run is literal", "*unclosed", "*unclosed"},
		{"a space before the closer does not close", "*a *", "*a *"},
		// CommonMark forbids intraword emphasis with underscores and allows it
		// with asterisks, and here the distinction cannot be observed: Chat
		// writes italic with the same character CommonMark does, so treating
		// snake_case as emphasis and leaving it alone produce the same bytes.
		{"snake case comes out as it went in", "my_var_name", "my_var_name"},
		{"an identifier with two runs is unchanged", "a_b_c_d", "a_b_c_d"},

		// Things that look like markup and are not. Each one is a place the
		// scanner has to decide to leave text alone, and leaving text alone is
		// most of what this package does.
		{"a bracket with no link is text", "[text] and more", "[text] and more"},
		{"a bracket with no closing paren is text", "[text](unclosed", "[text](unclosed"},
		{"a bracket that never closes is text", "[text and more", "[text and more"},
		{"an exclamation mark alone is text", "! not an image", "! not an image"},
		{"an exclamation mark before text is text", "![alt] no target", "![alt] no target"},
		{"a code span inside link text is respected", "[a `]` b](https://x.invalid)", "<https://x.invalid|a `]` b>"},
		{"nested brackets in link text", "[a [b] c](https://x.invalid)", "<https://x.invalid|a [b] c>"},
		{"four leading spaces is not a fence", "    ```\n**bold**", "    ```\n*bold*"},
		{"a run of three asterisks is left alone", "***a***", "***a***"},
		{"a run of four underscores is left alone", "____", "____"},
		{"a heading of only hashes and spaces is left alone", "##   ", "##   "},

		{"an empty message stays empty", "", ""},
		{"a trailing newline survives", "**a**\n", "*a*\n"},
		{"blank lines survive", "a\n\n**b**", "a\n\n*b*"},
		{"plain text is untouched", "deploy done", "deploy done"},
		{"emoji and other scripts are untouched", "done 🎉 日本語", "done 🎉 日本語"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := translate(t, tc.src)
			if got != tc.want {
				t.Errorf("Translate(%q)\n got %q\nwant %q", tc.src, got, tc.want)
			}
		})
	}
}

// TestUnsupportedConstructsWarnAboutWhatTheyAre holds the half of §9 that says
// to degrade and warn. The warning has to name what happened: "some formatting
// was lost" tells somebody nothing they can act on.
func TestUnsupportedConstructsWarnAboutWhatTheyAre(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"a table warns about tables", "| a | b |\n| - | - |", "table"},
		{"an image warns about images", "![alt](https://x.invalid/a.png)", "image"},
		{"an unclosed fence warns", "```\ncode", "never closed"},
		{"existing mention markup warns", "ping <users/all> now", "mention"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, warnings := translate(t, tc.src)
			joined := strings.Join(warnings, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("Translate(%q) warned %q, which does not mention %q", tc.src, joined, tc.want)
			}
		})
	}
}

func TestAnImageBecomesALinkRatherThanNothing(t *testing.T) {
	got, _ := translate(t, "![the graph](https://x.invalid/a.png)")
	if want := "<https://x.invalid/a.png|the graph>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestWarningsDoNotRepeat keeps a twenty-row table from producing twenty
// identical lines that somebody learns to scroll past.
func TestWarningsDoNotRepeat(t *testing.T) {
	_, warnings := translate(t, "| a | b |\n| c | d |\n| e | f |")
	if len(warnings) != 1 {
		t.Errorf("got %d warnings for three table rows: %v", len(warnings), warnings)
	}
}

// TestInvalidUTF8IsRefused is the never-silently-altered rule. The alternative
// is sending a message that is not the one the caller wrote, to people who
// cannot tell, on behalf of somebody who was told it worked.
func TestInvalidUTF8IsRefused(t *testing.T) {
	for _, src := range []string{"ok \xff\xfe bad", "\xff", "good text then \x80"} {
		t.Run(src, func(t *testing.T) {
			err := Validate(src)
			if err == nil {
				t.Fatal("invalid UTF-8 was accepted")
			}
			if got := output.ExitCodeOf(err); got != output.ExitUsage {
				t.Errorf("exit %d, want %d", got, output.ExitUsage)
			}
			if !strings.Contains(err.Error(), "offset") {
				t.Errorf("the message does not say where the bad byte is:\n%v", err)
			}

			if _, _, err := Translate(src); err == nil {
				t.Error("Translate accepted invalid UTF-8")
			}
		})
	}
}

func TestValidUTF8IsAccepted(t *testing.T) {
	for _, src := range []string{"", "plain", "🎉 日本語 Привет", "tab\tand\nnewline"} {
		if err := Validate(src); err != nil {
			t.Errorf("Validate(%q): %v", src, err)
		}
	}
}
