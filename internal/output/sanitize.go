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
	"fmt"
	"strings"
	"unicode/utf8"
)

// Sanitize makes text safe to write to a terminal.
//
// A terminal is a program that interprets bytes, and a message body is written
// by whoever is in the space: an external guest, a compromised account, an app
// posting on somebody's behalf. A body containing a CSI sequence can move the
// cursor or clear the screen; an OSC sequence can set the window title, and on
// some terminals prime the input buffer with a command the operator did not
// type. None of that is a rendering quirk. It is somebody else's program
// running on the reader's screen.
//
// So a control character is shown rather than obeyed, in the same notation Go
// uses for one, and everything else survives untouched: emoji, accents, and
// every script a colleague might write in are data, not danger.
//
// Tab and newline survive, because a message legitimately contains both and
// escaping them would make ordinary multi-line text unreadable. Carriage return
// does not: on its own it returns the cursor to the start of the line, so text
// after it overwrites text before it, and a body can hide what this tool
// printed a moment ago.
func Sanitize(s string) string { return escape(s, false) }

// Cell makes text safe to write as one column of one row.
//
// Everything Sanitize does, and tab and newline as well. A list is written one
// record per line with tab between the columns, so a value containing either
// can forge a column or a whole row: a message body reading "done\tspaces/AAAA"
// would otherwise arrive at whatever is parsing the output as two fields.
//
// This is the difference between text that is merely safe to display and text
// that is safe to display inside a structure.
func Cell(s string) string { return escape(s, true) }

// bidi are the characters that reorder text without being control characters.
//
// U+202E RIGHT-TO-LEFT OVERRIDE and its relatives change the order glyphs are
// painted in, so a string can be made to display as something other than what
// it contains. It is the trick behind the Trojan Source papers, and in a tool
// that prints message bodies next to space names it is a way to make one look
// like another.
var bidi = map[rune]bool{
	0x200E: true, // LEFT-TO-RIGHT MARK.
	0x200F: true, // RIGHT-TO-LEFT MARK.
	0x202A: true, // LEFT-TO-RIGHT EMBEDDING.
	0x202B: true, // RIGHT-TO-LEFT EMBEDDING.
	0x202C: true, // POP DIRECTIONAL FORMATTING.
	0x202D: true, // LEFT-TO-RIGHT OVERRIDE.
	0x202E: true, // RIGHT-TO-LEFT OVERRIDE.
	0x2066: true, // LEFT-TO-RIGHT ISOLATE.
	0x2067: true, // RIGHT-TO-LEFT ISOLATE.
	0x2068: true, // FIRST STRONG ISOLATE.
	0x2069: true, // POP DIRECTIONAL ISOLATE.
}

func escape(s string, strict bool) string {
	var b strings.Builder

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])

		// A one-byte RuneError is a byte that is not valid UTF-8, rather than a
		// real U+FFFD. Written as the byte it is: replacing it with the
		// replacement character would be this tool altering a value to make it
		// printable, and a message that is not what was sent is a wrong answer
		// that looks like a right one.
		if r == utf8.RuneError && size == 1 {
			grow(&b, s, i)
			writeHexByte(&b, s[i])
			i += size
			continue
		}

		if replacement, ok := escapeRune(r, strict); ok {
			grow(&b, s, i)
			b.WriteString(replacement)
			i += size
			continue
		}

		if b.Len() > 0 {
			b.WriteRune(r)
		}
		i += size
	}

	// Nothing needed escaping, so the original string is returned rather than a
	// copy of it. Most text is ordinary and this is on the path of every
	// message this tool ever prints.
	if b.Len() == 0 {
		return s
	}
	return b.String()
}

// escapeRune reports how r should be written, and whether it needs writing at
// all.
func escapeRune(r rune, strict bool) (string, bool) {
	switch r {
	case '\t':
		if strict {
			return `\t`, true
		}
		return "", false
	case '\n':
		if strict {
			return `\n`, true
		}
		return "", false
	case '\r':
		return `\r`, true
	case 0x7f:
		return `\x7f`, true
	}

	switch {
	case r < 0x20:
		return fmt.Sprintf(`\x%02x`, r), true
	// The C1 range holds a second encoding of the control sequence introducer
	// at U+009B, which a terminal in eight-bit mode acts on exactly as it acts
	// on ESC followed by an open bracket.
	case r >= 0x80 && r <= 0x9f:
		return fmt.Sprintf(`\u%04x`, r), true
	case bidi[r]:
		return fmt.Sprintf(`\u%04x`, r), true
	// The Tags block, which is where hidden text goes. See tagsBlock.
	case r >= tagsFirst && r <= tagsLast:
		return fmt.Sprintf(`\U%08x`, r), true
	}
	return "", false
}

// The Unicode Tags block, U+E0000 to U+E007F.
//
// Deprecated since Unicode 5.1 and rendered as nothing by every terminal and
// every font, and it carries a complete ASCII alphabet: U+E0041 is a tag
// letter A. So a run of them is text that is present in a message, absent from
// the screen, and perfectly legible to anything reading codepoints. It is the
// standard carrier for an instruction meant for a model rather than a person.
//
// This matters here because of what Milestone 5 changed. A message body used to
// go to a terminal, where an invisible character is a curiosity: a terminal does
// not act on one, and the escaping rule above is about a terminal being a
// program that interprets bytes. A body now also goes to a model, through
// list_messages, get_message and search_messages, and a model reads codepoints
// rather than glyphs. Every one of those tools already ends its description with
// a sentence admitting the risk; this is that risk with the operator's ability
// to notice it removed.
//
// Escaped rather than removed, like everything else here, and escaped visibly:
// what a reader sees is \U000e0041 where the hidden text is, which is the
// signal. Removing it would be altering a value to make it printable, which is
// the one thing this package will not do.
//
// **Only this block.** The obvious wider set is a trap. U+200D and U+200C are
// invisible and are required by real text: a family emoji is zero-width joiners
// and Persian and several Indic scripts need the non-joiner, so escaping them
// would mangle ordinary messages, and a defence that garbles what people
// actually write is one somebody turns off. The Tags block has no legitimate use
// in a message at all, which is what makes it the one that can be refused
// without a judgement call.
//
// --json and an MCP tool result are deliberately untouched, for the reason they
// are already exempt from the rest of this file: they hand a program what was
// actually there. So this makes the terminal honest and changes nothing about
// what a model receives, which SECURITY.md says out loud rather than leaving to
// be inferred from a passing escape.
const (
	tagsFirst = 0xE0000
	tagsLast  = 0xE007F
)

// grow copies the untouched prefix into b the first time something has to be
// escaped, so that the common case of text needing nothing never allocates.
func grow(b *strings.Builder, s string, upTo int) {
	if b.Len() == 0 {
		b.Grow(len(s) + 8)
		b.WriteString(s[:upTo])
	}
}

// writeHexByte writes c as \xNN, and exists so that escape's Builder can stay
// on the stack.
//
// It was fmt.Fprintf(&b, `\x%02x`, c) until 2026-08-20, which converts &b to
// an io.Writer, and a Builder that reaches an interface escapes to the heap.
// That is 32 bytes and one allocation on every call to escape, including the
// calls that escape nothing, on the path of every cell this tool prints. The
// branch it sits in runs only for a byte that is not valid UTF-8, so the cost
// was paid by all the callers who never reached it, and grow's comment said
// the common case never allocates while it always did.
//
// strings.Builder's own methods are written to keep the receiver on the stack,
// so WriteString and WriteByte do not have this effect. BenchmarkEscape is
// what shows the difference, and `go build -gcflags=-m` names the cause.
func writeHexByte(b *strings.Builder, c byte) {
	const hexDigits = "0123456789abcdef"

	b.WriteString(`\x`)
	b.WriteByte(hexDigits[c>>4])
	b.WriteByte(hexDigits[c&0x0f])
}
