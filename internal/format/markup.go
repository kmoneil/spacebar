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
