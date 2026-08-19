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
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/output"
)

// TestALinkCannotBeClosedByItsOwnText is the claim from the card, and the
// answer to it changed twice.
//
// The card said the wrapper characters would be escaped. They cannot be: Chat
// has no escape syntax, which was later checked against a real space rather
// than assumed, and a backslash renders as a backslash. So the choice is
// between refusing and altering, and altering a message so that it can be
// represented is the thing this tool does not do.
//
// The second change is what is refused. The same live check measured the
// parser: the URL is everything to the first pipe and the display is everything
// from there to the first closing bracket. Only that closing bracket cannot
// appear in the display, and this package refused all three for two milestones,
// which failed a message that would have worked. See the sibling test.
func TestALinkCannotBeClosedByItsOwnText(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		display string
	}{
		{"a closing bracket in the text", "https://x.invalid", "a>b"},
		{"a pipe in the url", "https://x.invalid|evil", "text"},
		{"a bracket in the url", "https://x.invalid>", "text"},
		{"an opening bracket in the url", "https://x.invalid/a<evil", "text"},

		// The display ends at the first closing bracket, so this one is refused
		// for the bracket rather than for the mention: what would arrive is
		// "click" as a link and the rest as ordinary text, which is not the
		// message that was written.
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

// TestAPipeOrAnOpeningBracketInLinkTextIsRepresentable.
//
// Measured against a real space, which is the only instrument there is: the
// API returns no formattedText on a webhook send, so it cannot report its own
// interpretation. `<url|a | b>` renders as a link labelled `a | b`, because the
// split is on the first pipe, and `<url|a < b>` renders as `a < b`, because an
// opening bracket starts nothing.
//
// This package refused both for two milestones on the assumption that it could
// not know, which is the right way to be wrong, and the cost was that
// `[the a|b docs](url)` failed at exit 2 on something that works.
func TestAPipeOrAnOpeningBracketInLinkTextIsRepresentable(t *testing.T) {
	for _, tc := range []struct{ name, display, want string }{
		{"a pipe", "a | b", "<https://x.invalid|a | b>"},
		{"two pipes", "a || b", "<https://x.invalid|a || b>"},
		{"an opening bracket", "a < b", "<https://x.invalid|a < b>"},
		{"what looks like a mention but has no close", "a <users/all", "<https://x.invalid|a <users/all>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Link("https://x.invalid", tc.display)
			if err != nil {
				t.Fatalf("Link refused a display Chat renders: %v", err)
			}
			if got != tc.want {
				t.Errorf("Link = %q, want %q", got, tc.want)
			}
		})
	}

	// And through the translator, which is how anybody actually reaches it.
	got, _, err := Translate("see [the a|b docs](https://x.invalid)")
	if err != nil {
		t.Fatalf("Translate refused a link Chat renders: %v", err)
	}
	if got != "see <https://x.invalid|the a|b docs>" {
		t.Errorf("Translate = %q", got)
	}
}

// TestARefusedLinkStopsTheWholeMessage keeps the refusal from being something
// a caller can walk past. Nothing is sent, rather than the message being sent
// with one link quietly dropped.
func TestARefusedLinkStopsTheWholeMessage(t *testing.T) {
	_, _, err := Translate("ok [a>b](https://x.invalid) more")
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
	const bad = "[a>b](https://x.invalid)"

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

// TestCardsChecksOnlyTheShapeTheAPIRequires.
//
// A card is a deep tree of widgets with its own schema, and a struct written
// here would be a guess reviewed as though it were knowledge, with every field
// this tool had not heard of silently dropped out of somebody's message. So the
// elements are not decoded, and what is checked is the part a person gets
// wrong: cardsV2 is a list of objects that each have a card.
//
// This test exists because the invariant says this package is at 100% statement
// coverage and it was not: Cards arrived with the send command, was covered
// through internal/cli, and left two functions here untouched. The milestone
// exit sweep did not catch it, which is worth knowing about the sweep.
func TestCardsChecksOnlyTheShapeTheAPIRequires(t *testing.T) {
	good := `[{"cardId":"deploy","card":{"header":{"title":"Deployed"}}},{"cardId":"b","card":{}}]`

	cards, err := Cards([]byte(good))
	if err != nil {
		t.Fatalf("Cards refused a valid list: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(cards))
	}

	// Carried through byte for byte, because the caller owns the shape.
	if !strings.Contains(string(cards[0]), `"header"`) {
		t.Errorf("a field this package does not model was dropped: %s", cards[0])
	}

	for _, tc := range []struct{ name, body, says string }{
		// The common mistake, and it is named rather than fixed: wrapping it
		// would be this package deciding what somebody meant.
		{"one card where a list belongs", `{"cardId":"a","card":{}}`, "holds one card"},

		{"an element with no card", `[{"cardId":"a"}]`, `no "card" field`},
		{"an element that is not an object", `["a"]`, "not an object"},
		{"an empty list", `[]`, "holds no cards"},
		{"not JSON at all", `{`, "not the JSON"},
		{"nothing", ``, "not the JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Cards([]byte(tc.body))
			if err == nil {
				t.Fatalf("Cards accepted %q and returned %d cards", tc.body, len(got))
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q:\n%v", tc.says, err)
			}
			if code := output.ExitCodeOf(err); code != output.ExitUsage {
				t.Errorf("exit %d, want %d: the file has to be corrected by whoever wrote it", code, output.ExitUsage)
			}
		})
	}
}

// TestAMentionAcceptsAnAddressBecauseChatResolvesOne.
//
// SPEC.md §10.3 deferred `--mention` on the belief that an email had to become
// a users/NNN first. It does not: `<users/someone@example.com>` was sent to a
// real space on 2026-08-17 and came back with a USER_MENTION annotation naming
// the numeric id, so Chat resolves the address itself.
//
// That belief cost three milestones, which is the argument for measuring a
// premise rather than reasoning from it.
func TestAMentionAcceptsAnAddressBecauseChatResolvesOne(t *testing.T) {
	for _, name := range []string{
		"users/100000000000000000001",
		"users/someone@example.test",
		"users/first.last@sub.example.test",
		"users/all",
	} {
		got, err := Mention(name)
		if err != nil {
			t.Errorf("Mention(%q): %v", name, err)
			continue
		}
		if got != "<"+name+">" {
			t.Errorf("Mention(%q) = %q", name, got)
		}
	}
}

// TestAMentionStillRefusesWhatWouldCloseItsOwnWrapper.
//
// Widening the identifier to take an address must not widen it to take a
// wrapper character. Chat has no escape syntax, so a name carrying one cannot
// be represented and is refused rather than altered, which is the same rule the
// link half follows.
func TestAMentionStillRefusesWhatWouldCloseItsOwnWrapper(t *testing.T) {
	for _, name := range []string{
		"users/a>b@example.test",
		"users/a<b@example.test",
		"users/a|b@example.test",
		"users/a b@example.test",
		"users/a\nb@example.test",
		"users/",
		"users",
		"100000000000000000001",
		"spaces/AAA",
	} {
		if got, err := Mention(name); err == nil {
			t.Errorf("Mention(%q) built %q instead of refusing it", name, got)
		}
	}
}

// FuzzCardsAreCheckedWithoutBeingRewritten states the design decision in
// Cards' own comment as a property: "The elements are not decoded."
//
// That sentence is the whole argument for the function's shape. A struct
// written here for a card would be a guess reviewed as though it were
// knowledge, and every widget field this tool had not heard of would drop
// silently out of somebody's message. So what has to hold is not that a card
// is valid, which this cannot know, but that a card which is accepted arrives
// on the wire as the one that was written.
//
// It is fuzzed rather than tabled because this is a JSON parser over a file
// somebody passed on the command line, which is the shape of input a table
// samples worst. The seeds are the mistakes: a single card where a list
// belongs, a list of things that are not objects, an element with no card
// inside it.
func FuzzCardsAreCheckedWithoutBeingRewritten(f *testing.F) {
	for _, seed := range []string{
		`[{"cardId":"a","card":{"header":{"title":"Deploy"}}}]`,
		`[{"cardId":"a","card":{}},{"cardId":"b","card":{}}]`,
		`{"cardId":"a","card":{}}`,
		`[]`, `[{}]`, `[1]`, `["a"]`, `[null]`, `[[]]`,
		`[{"card":{}}]`, `[{"cardId":"a"}]`,
		`null`, `0`, `""`, `{`, ``, `   `,
		`[{"cardId":"a","card":{"sections":[{"widgets":[{"textParagraph":{"text":"<b>hi</b>"}}]}]}}]`,
		"[{\"cardId\":\"a\",\"card\":{\"x\":\"\\u0000\"}}]",
		`[{"cardId":"a","card":{"n":1e400}}]`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		cards, err := Cards(raw)
		if err != nil {
			// A refusal is a valid outcome and the common one. What matters is
			// that it is a refusal rather than a panic, and that it never
			// half-succeeds.
			if cards != nil {
				t.Fatalf("Cards(%q) returned both %d cards and the error %v", raw, len(cards), err)
			}
			return
		}

		if len(cards) == 0 {
			t.Fatalf("Cards(%q) succeeded with no cards, which send would put on the wire as an empty list", raw)
		}

		// Every element is still an object with a card in it, which is the
		// only shape claim this function makes.
		for i, one := range cards {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(one, &fields); err != nil {
				t.Fatalf("Cards(%q) returned element %d that is not an object: %v", raw, i, err)
			}
			if _, ok := fields["card"]; !ok {
				t.Fatalf("Cards(%q) returned element %d with no card in it: %s", raw, i, one)
			}
		}

		// And nothing was dropped or rewritten on the way through. Compared
		// after decoding rather than byte for byte, because whitespace and key
		// order are not what the claim is about: what was written has to be
		// what is sent.
		out, err := json.Marshal(cards)
		if err != nil {
			t.Fatalf("the cards Cards returned cannot be marshalled back: %v", err)
		}
		wrote, err := decodeJSON(raw)
		if err != nil {
			t.Fatalf("Cards accepted %q, which is not JSON: %v", raw, err)
		}
		sending, err := decodeJSON(out)
		if err != nil {
			t.Fatalf("the cards Cards returned do not re-parse: %v", err)
		}
		if !reflect.DeepEqual(wrote, sending) {
			t.Fatalf("a card was altered on the way through:\n  in %q\n out %q", raw, out)
		}
	})
}

// decodeJSON reads a document into comparable Go values, keeping every number
// as the text it was written as.
//
// UseNumber rather than the default, and the fuzz target found the reason in
// its own seeds within a second of being written. Decoding into `any` turns
// every number into a float64, and 1e400 does not fit in one, so the
// comparison failed on a card the code had passed through perfectly correctly.
// That was the assertion being wrong rather than Cards: json.RawMessage carries
// the bytes, so a number too large for a float64 reaches the wire intact and
// should. A test that cannot represent what the code preserves is a test that
// reports a false failure, which is the kind that gets a real one waved through.
func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
