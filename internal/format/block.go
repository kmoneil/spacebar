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

import "strings"

// translateBlocks walks the message a line at a time, because the one piece of
// state that matters spans lines: whether this line is inside a fenced code
// block.
//
// A fence is the case the spec singles out. Somebody pastes a shell command, a
// diff, or a stack trace, and every asterisk and underscore in it has to arrive
// exactly as it was pasted. So a fence is tracked here rather than guessed at
// by whatever is looking at one line.
func translateBlocks(src string, warn *collector) (string, error) {
	lines, trailingNewline := splitLines(src)
	out := make([]string, 0, len(lines))

	inFence := false
	for _, line := range lines {
		if isFence(line) {
			// The delimiter itself passes through: Chat writes a code block
			// with three backticks too, so the fence is already correct.
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}

		translated, err := translateLine(line, warn)
		if err != nil {
			return "", err
		}
		out = append(out, translated)
	}

	if inFence {
		// Left open on purpose rather than closed for them. Adding a fence
		// would change what is sent, and the sender may have meant the
		// backticks literally. Chat renders the rest as code, which is what
		// the source says, and the warning says so before it goes.
		warn.add("a fenced code block is never closed, so everything after it will be sent as code")
	}
	return joinLines(out, trailingNewline), nil
}

// isFence reports whether a line opens or closes a fenced code block. Up to
// three leading spaces, then at least three backticks, which is CommonMark's
// rule.
func isFence(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return false
	}
	return strings.HasPrefix(trimmed, "```")
}

// translateLine handles the block constructs that survive into Chat, then
// hands the rest of the line to the inline scanner.
func translateLine(line string, warn *collector) (string, error) {
	indent, body := splitIndent(line)

	if heading, ok := headingText(body); ok {
		// Chat has no headings. A bold line is the closest thing that still
		// reads as a heading rather than as ordinary text.
		text, err := translateInline(heading, warn)
		if err != nil {
			return "", err
		}
		if text == "" {
			return line, nil
		}
		return indent + "*" + text + "*", nil
	}

	if marker, rest, ok := listMarker(body); ok {
		text, err := translateInline(rest, warn)
		if err != nil {
			return "", err
		}
		// Chat takes the same two markers, so the prefix stays as written.
		return indent + marker + text, nil
	}

	if quoted, ok := strings.CutPrefix(body, ">"); ok {
		// Chat quotes with the same character. Only the content is translated,
		// so the marker cannot be mistaken for a link wrapper.
		text, err := translateInline(quoted, warn)
		if err != nil {
			return "", err
		}
		return indent + ">" + text, nil
	}

	if isTableRow(body) {
		warn.add("Chat has no tables, so the rows will arrive as the lines of text they are written as")
	}

	return translateInlineWithIndent(indent, body, warn)
}

func translateInlineWithIndent(indent, body string, warn *collector) (string, error) {
	text, err := translateInline(body, warn)
	if err != nil {
		return "", err
	}
	return indent + text, nil
}

func splitIndent(line string) (indent, body string) {
	trimmed := strings.TrimLeft(line, " \t")
	return line[:len(line)-len(trimmed)], trimmed
}

// headingText returns the text of an ATX heading.
func headingText(body string) (string, bool) {
	hashes := 0
	for hashes < len(body) && body[hashes] == '#' {
		hashes++
	}
	if hashes == 0 || hashes > 6 {
		return "", false
	}
	rest := body[hashes:]
	if rest != "" && rest[0] != ' ' {
		// "#nothing" is a word beginning with a hash, not a heading. A channel
		// name or a ticket number would otherwise become a bold line.
		return "", false
	}
	// A closing run of hashes is decoration in CommonMark and is dropped.
	return strings.TrimRight(strings.TrimSpace(rest), " #"), true
}

// listMarker splits a bullet marker off the front of a line.
//
// The trailing space is what tells a list from emphasis: "* item" is a bullet
// and "*italic*" is not, and the difference is one character.
func listMarker(body string) (marker, rest string, ok bool) {
	for _, prefix := range []string{"* ", "- ", "+ "} {
		if strings.HasPrefix(body, prefix) {
			return prefix, body[len(prefix):], true
		}
	}
	return "", "", false
}

// isTableRow recognises a GitHub-style table row well enough to warn about it.
func isTableRow(body string) bool {
	return strings.HasPrefix(body, "|") && strings.Count(body, "|") >= 2
}
