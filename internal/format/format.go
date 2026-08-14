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

// Package format turns CommonMark into Google Chat markup.
//
// Chat's text field is not CommonMark and the differences are not cosmetic.
// Bold is one asterisk, not two. Italic is an underscore. A link is
// <https://url|display text> rather than [display text](https://url). Passing
// ordinary Markdown through renders literal asterisks and tildes to everybody
// in the space, which is a real and common bug in tools that assume otherwise.
//
// Two rules carry this package, and SPEC.md §16 asks for exhaustive tests and
// fuzzing because of them.
//
// Nothing inside a fenced or inline code span is ever transformed. Somebody
// pasting a shell command or a diff into a code block is the case this exists
// for, and it is the most likely bug here: a translator that works by
// substitution cannot know where it is, so this one scans left to right and
// tracks what it is inside of.
//
// Markup is generated, never concatenated. One function builds a link and one
// builds a mention, and both refuse text they cannot represent rather than
// producing a wrapper that somebody else's text can close.
package format

import (
	"strings"
	"unicode/utf8"

	"github.com/kmoneil/spacebar/internal/output"
)

// Validate refuses a message that cannot be sent as it stands.
//
// Only one thing qualifies today, and it matters: text that is not valid
// UTF-8. The alternative to refusing is replacing the bad bytes with U+FFFD,
// which sends a message that is not the one the caller wrote, to people who
// have no way to tell, on behalf of somebody who was told it succeeded. A
// wrong answer that looks like a right one is worse than a failure.
// Written as one scan rather than utf8.ValidString followed by a hunt for the
// offset. The two-step version leaves a branch that no input can reach, and a
// line nothing can execute is a line nothing can check.
func Validate(src string) error {
	for i := 0; i < len(src); {
		r, size := utf8.DecodeRuneInString(src[i:])
		if r == utf8.RuneError && size == 1 {
			return output.Errorf("INVALID_UTF8", output.ExitUsage,
				"the message is not valid UTF-8: the byte at offset %d (0x%02x) does not begin a character.\n"+
					"Nothing was sent. Replacing it would send a message that is not the one you wrote, "+
					"to people who could not tell.", i, src[i])
		}
		i += size
	}
	return nil
}

// Translate converts CommonMark into Chat markup, returning any warnings about
// what could not be carried across.
//
// Without --md a message is sent exactly as it was typed, so this runs only
// when the caller asked for it. Anything this does not understand is left
// alone rather than guessed at.
func Translate(src string) (string, []string, error) {
	if err := Validate(src); err != nil {
		return "", nil, err
	}

	warn := &collector{}
	out, err := translateBlocks(src, warn)
	if err != nil {
		return "", nil, err
	}
	return out, warn.list, nil
}

// collector gathers warnings without repeating them.
//
// A twenty-row table would otherwise produce twenty identical lines about
// tables, and a warning somebody has to scroll past is a warning they stop
// reading.
type collector struct {
	seen map[string]bool
	list []string
}

func (c *collector) add(message string) {
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	if c.seen[message] {
		return
	}
	c.seen[message] = true
	c.list = append(c.list, message)
}

// splitLines splits on newlines and keeps the information about whether the
// text ended with one, so that translating and rejoining is lossless.
func splitLines(src string) (lines []string, trailingNewline bool) {
	if src == "" {
		return nil, false
	}
	trailingNewline = strings.HasSuffix(src, "\n")
	body := strings.TrimSuffix(src, "\n")
	return strings.Split(body, "\n"), trailingNewline
}

func joinLines(lines []string, trailingNewline bool) string {
	out := strings.Join(lines, "\n")
	if trailingNewline {
		out += "\n"
	}
	return out
}
