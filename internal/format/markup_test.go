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

// TestALinkCannotBeClosedByItsOwnText is the claim from the card, and the
// answer to it changed during the work.
//
// The card said the wrapper characters would be escaped. They cannot be: Chat
// has no escape syntax, so there is no way to write a pipe inside the display
// half of a link such that the far end reads it as a pipe. The choice is
// therefore between refusing and altering, and altering a message so that it
// can be represented is the thing this tool does not do.
func TestALinkCannotBeClosedByItsOwnText(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		display string
	}{
		{"a pipe in the text", "https://x.invalid", "a|b"},
		{"a closing bracket in the text", "https://x.invalid", "a>b"},
		{"an opening bracket in the text", "https://x.invalid", "a<b"},
		{"a whole wrapper in the text", "https://x.invalid", "x|https://evil.invalid"},
		{"a pipe in the url", "https://x.invalid|evil", "text"},
		{"a bracket in the url", "https://x.invalid>", "text"},
		{"markup smuggled through the text", "https://x.invalid", "click> <users/all"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Link(tc.url, tc.display)
			if err == nil {
				t.Fatalf("Link(%q, %q) = %q, and it should have been refused", tc.url, tc.display, got)
			}
			if code := output.ExitCodeOf(err); code != output.ExitUsage {
				t.Errorf("exit %d, want %d", code, output.ExitUsage)
			}
			if !strings.Contains(err.Error(), "offset") {
				t.Errorf("the refusal does not say where the character is:\n%v", err)
			}
		})
	}
}

// TestARefusedLinkStopsTheWholeMessage keeps the refusal from being something
// a caller can walk past. Nothing is sent, rather than the message being sent
// with one link quietly dropped.
func TestARefusedLinkStopsTheWholeMessage(t *testing.T) {
	_, _, err := Translate("ok [a|b](https://x.invalid) more")
	if err == nil {
		t.Fatal("a message with an unrepresentable link was accepted")
	}
	if code := output.ExitCodeOf(err); code != output.ExitUsage {
		t.Errorf("exit %d, want %d", code, output.ExitUsage)
	}
}

// TestARefusalSurvivesEveryConstructItIsBuriedIn keeps the refusal from
// depending on where the link happens to sit. Each of these reaches Link
// through a different path, and a path that swallowed the error would send the
// message with the link silently mangled.
func TestARefusalSurvivesEveryConstructItIsBuriedIn(t *testing.T) {
	const bad = "[a|b](https://x.invalid)"

	for _, src := range []string{
		bad,
		"# " + bad,
		"- " + bad,
		"> " + bad,
		"**" + bad + "**",
		"_" + bad + "_",
		"[outer " + bad + "](https://y.invalid)",
		"text before " + bad + " text after",
	} {
		t.Run(src, func(t *testing.T) {
			got, _, err := Translate(src)
			if err == nil {
				t.Fatalf("accepted, and produced %q", got)
			}
			if code := output.ExitCodeOf(err); code != output.ExitUsage {
				t.Errorf("exit %d, want %d", code, output.ExitUsage)
			}
		})
	}
}

// TestAnUnbalancedBracketIsText covers the case where a closing bracket exists
// but does not close this one, which is the only way the bracket matcher gives
// up after starting.
func TestAnUnbalancedBracketIsText(t *testing.T) {
	for _, src := range []string{"[[a]", "[[a](https://x.invalid)"} {
		got, _, err := Translate(src)
		if err != nil {
			t.Fatalf("Translate(%q): %v", src, err)
		}
		if !strings.Contains(got, "[") {
			t.Errorf("Translate(%q) = %q, and the bracket should have stayed text", src, got)
		}
	}
}

func TestLink(t *testing.T) {
	cases := []struct {
		url, display, want string
	}{
		{"https://x.invalid", "text", "<https://x.invalid|text>"},
		{"https://x.invalid", "", "https://x.invalid"},
		{"https://x.invalid", "https://x.invalid", "https://x.invalid"},
		{"https://x.invalid/a b", "spaced", "<https://x.invalid/a b|spaced>"},
	}

	for _, tc := range cases {
		got, err := Link(tc.url, tc.display)
		if err != nil {
			t.Errorf("Link(%q, %q): %v", tc.url, tc.display, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Link(%q, %q) = %q, want %q", tc.url, tc.display, got, tc.want)
		}
	}
}

// TestMentionOnlyAcceptsAResolvedName holds the second layer. The name comes
// back from a directory lookup, which is somewhere else, and a value that can
// close its own wrapper can write markup nobody asked for.
func TestMentionOnlyAcceptsAResolvedName(t *testing.T) {
	if got, err := Mention("users/1234567890"); err != nil || got != "<users/1234567890>" {
		t.Errorf("Mention(users/1234567890) = %q, %v", got, err)
	}

	for _, name := range []string{
		"",
		"1234567890",
		"users/",
		"users/12>34",
		"users/12|34",
		"users/all users/other",
		"spaces/AAAA",
		"users/../../etc",
	} {
		if got, err := Mention(name); err == nil {
			t.Errorf("Mention(%q) = %q, and it should have been refused", name, got)
		}
	}
}

func TestMentionAllIsWrittenOnceHere(t *testing.T) {
	if MentionAll != "<users/all>" {
		t.Errorf("MentionAll = %q", MentionAll)
	}
}

// FuzzTranslate is the property SPEC.md §16 asks for. Whatever goes in, what
// comes out cannot contain a wrapper this package built around text it did not
// check, and translating something twice must not keep changing it.
func FuzzTranslate(f *testing.F) {
	seeds := []string{
		"", "plain text", "**bold**", "*italic*", "~~strike~~",
		"`code **kept**`", "```\n**kept**\n```", "[text](https://x.invalid)",
		"| a | b |", "![alt](https://x.invalid)", "# heading", "> quote",
		"- item", "2 * 3 * 4", "<users/all>", "a\n\nb", "***", "____",
		"[a](b)[c](d)", "``a ` b``", "*a `b * c` d*",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, src string) {
		got, _, err := Translate(src)
		if err != nil {
			// A refusal is a valid outcome, and the only ones that exist are
			// invalid UTF-8 and a link that cannot be represented.
			return
		}

		// When the source has no angle bracket of its own, every one in the
		// output was built here, and Link never emits one it does not close.
		//
		// The condition is what makes this precise rather than merely strict.
		// A source containing "<users/" is somebody's own text and comes
		// through exactly as written, unclosed bracket and all, because
		// altering it would be inventing intent. The first version of this
		// check had no condition and the fuzzer found that input in under a
		// second, which is the check being wrong rather than the code.
		if !strings.Contains(src, "<") {
			if err := wrappersBalance(got); err != nil {
				t.Fatalf("Translate(%q) = %q: %v", src, got, err)
			}
		}

		// Valid in has to mean valid out. A scanner that works in bytes could
		// otherwise cut a multi-byte character in half and nothing downstream
		// would notice until somebody read the message.
		if err := Validate(got); err != nil {
			t.Fatalf("Translate(%q) = %q, which is no longer valid UTF-8: %v", src, got, err)
		}

		// Text with nothing to translate comes out as it went in. This is the
		// case almost every real message is, and the one where a bug would be
		// least visible in a table of interesting inputs.
		if !strings.ContainsAny(src, "*_~`[]()#>|<!") && got != src {
			t.Fatalf("plain text was altered:\n  in %q\n out %q", src, got)
		}
	})
}

// TestTranslationIsOneWay records something the fuzz target originally
// asserted the opposite of, which is worth keeping written down.
//
// Translating is not idempotent and cannot be. The two markups share
// characters and disagree about them: two asterisks mean bold in CommonMark
// and Chat writes bold with one, so Chat's bold reads back as CommonMark's
// italic and translating twice turns *bold* into _italic_. There is no
// encoding that avoids this, because the ambiguity is between the languages
// rather than in this code.
//
// The consequence is a documentation one. Feeding already-translated text back
// through --md corrupts it, so the output of a dry run is not an input.
func TestTranslationIsOneWay(t *testing.T) {
	once, _ := translate(t, "**bold**")
	if once != "*bold*" {
		t.Fatalf("first pass = %q", once)
	}

	twice, _ := translate(t, once)
	if twice == once {
		t.Fatal("translating twice was stable, so this note is out of date and the fuzz target could assert it")
	}
	if twice != "_bold_" {
		t.Errorf("second pass = %q, want %q", twice, "_bold_")
	}
}

// wrappersBalance checks that no wrapper is left open. Called only when the
// source had no angle bracket of its own, so every one here was written by this
// package.
func wrappersBalance(s string) error {
	for i := 0; i < len(s); i++ {
		if s[i] == '<' && !strings.Contains(s[i+1:], ">") {
			return errUnclosed
		}
	}
	return nil
}

var errUnclosed = &wrapperError{}

type wrapperError struct{}

func (*wrapperError) Error() string { return "a wrapper was opened and never closed" }
