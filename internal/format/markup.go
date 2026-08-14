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
	"encoding/json"
	"strings"

	"github.com/kmoneil/spacebar/internal/output"
)

// wrapperChars are the three characters that carry structure in Chat markup.
//
// A link is <url|display> and a mention is <users/id>, so an angle bracket
// opens or closes one and a pipe separates the halves. Anywhere else in a
// message they are ordinary text.
const wrapperChars = "<>|"

// MentionAll is the markup that notifies everybody in a space.
const MentionAll = "<users/all>"

// Link builds a hyperlink.
//
// This is the only place one is built, which is what the generated-never-
// concatenated rule means: a link assembled at a call site is a link whose
// display text nobody checked.
//
// Chat has no escape syntax. There is no way to write a pipe inside the
// display half of a link such that the far end reads it as a pipe rather than
// as the separator, so a display text containing one cannot be represented at
// all, and this refuses rather than producing something that renders as
// neither what was asked for nor an error. Refusing is the only option that
// does not silently change what a reader sees.
//
// If a way to escape these does exist, this is the one function that changes.
// See the note in this package's tests about what was not verified.
func Link(url, display string) (string, error) {
	if err := checkWrapper(url, "the URL of a link"); err != nil {
		return "", err
	}
	if err := checkWrapper(display, "the text of a link"); err != nil {
		return "", err
	}

	// A link whose text is its own URL is written bare: Chat turns a URL into
	// a link on its own, and <url|url> shows the address twice.
	if display == "" || display == url {
		return url, nil
	}
	return "<" + url + "|" + display + ">", nil
}

// Mention builds a mention of one user from a resolved user resource name.
//
// The name is checked rather than trusted because it reaches a wrapper: it
// arrives from a directory lookup, which is somewhere else, and a value that
// can close its own wrapper can write markup of its own.
func Mention(name string) (string, error) {
	if !strings.HasPrefix(name, "users/") || !safeResourceName(strings.TrimPrefix(name, "users/")) {
		return "", output.Errorf("MARKUP", output.ExitUsage,
			"%q is not a user resource name, which is users/ followed by an identifier.", name)
	}
	return "<" + name + ">", nil
}

// safeResourceName reports whether s is safe to place inside a wrapper without
// any escaping at all, which is the same standard SPEC.md §15.8 sets for a
// space name reaching a URL path. Escaping is the second layer and never the
// only one.
func safeResourceName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func checkWrapper(s, what string) error {
	i := strings.IndexAny(s, wrapperChars)
	if i < 0 {
		return nil
	}
	return output.Errorf("MARKUP", output.ExitUsage,
		"%s contains %q at offset %d, and Chat markup has no way to escape it there.\n"+
			"A link is written <url|text>, so an angle bracket or a pipe inside one ends it early "+
			"and the rest becomes markup somebody else wrote.\n"+
			"Nothing was sent. Remove the character, or send the message without --md.",
		what, s[i], i)
}

// Cards reads the cardsV2 payload for a message.
//
// The elements are not decoded. A card is a deep tree of widgets with its own
// schema, and a struct written here would be a guess reviewed as though it were
// knowledge, with every field this tool had not heard of silently dropped out
// of somebody's message. What is checked is only the shape the API requires of
// the list itself, which is the part a person gets wrong: cardsV2 is an array
// of CardWithId, each of which has a card.
//
// The most common mistake is pasting a single card where an array belongs, and
// it is worth naming rather than approximating. Wrapping it would be this tool
// deciding what somebody meant, and the whole point of refusing is that a
// message which is not the one they wrote is worse than a failure.
func Cards(raw []byte) ([]json.RawMessage, error) {
	var cards []json.RawMessage
	if err := json.Unmarshal(raw, &cards); err != nil {
		var single map[string]json.RawMessage
		if json.Unmarshal(raw, &single) == nil {
			return nil, cardErr("that file holds one card, and %s is a list of them.\n"+
				"Wrap it in brackets: [{\"cardId\": \"a-name\", \"card\": { ... }}]", "cardsV2")
		}
		return nil, cardErr("that file is not the JSON %s takes: %v", "cardsV2", err)
	}
	if len(cards) == 0 {
		return nil, cardErr("that file holds no cards.")
	}

	for i, one := range cards {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(one, &fields); err != nil {
			return nil, cardErr("card %d is not an object.", i+1)
		}
		if _, ok := fields["card"]; !ok {
			return nil, cardErr("card %d has no %q field.\n"+
				"Each element is {\"cardId\": \"a-name\", \"card\": { ... }}, and the card itself goes inside.",
				i+1, "card")
		}
	}
	return cards, nil
}

// cardErr is exit 2: the file has to be corrected by whoever wrote it, and no
// retry changes that.
func cardErr(format string, a ...any) error {
	return output.Errorf("CARD", output.ExitUsage, format, a...)
}
