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

// translateInline scans one line left to right.
//
// Left to right, and not by substitution, because the state that decides
// whether a character means anything is the state of what came before it. A
// pass that replaced "**" everywhere would rewrite the inside of a code span,
// which is the bug SPEC.md §9 names as the most likely one in this package.
//
// The scan works in bytes rather than runes on purpose. Every delimiter here
// is ASCII, and UTF-8 never encodes a multi-byte character using an ASCII
// byte, so a byte scan cannot land in the middle of one or mistake part of an
// emoji for an asterisk.
func translateInline(src string, warn *collector) (string, error) {
	var b strings.Builder
	b.Grow(len(src))

	for i := 0; i < len(src); {
		consumed, err := scanOne(src, i, &b, warn)
		if err != nil {
			return "", err
		}
		i += consumed
	}
	return b.String(), nil
}

// scanOne handles whatever begins at i and reports how many bytes it took.
func scanOne(src string, i int, b *strings.Builder, warn *collector) (int, error) {
	switch src[i] {
	case '`':
		return scanCode(src, i, b), nil
	case '!', '[':
		if n, err := scanLink(src, i, b, warn); n > 0 || err != nil {
			return n, err
		}
	case '*', '_', '~':
		if n, err := scanEmphasis(src, i, b, warn); n > 0 || err != nil {
			return n, err
		}
	case '<':
		noteChatMarkup(src, i, warn)
	}

	b.WriteByte(src[i])
	return 1, nil
}

// scanCode copies a code span through untouched, delimiters and all.
//
// Chat writes inline code with backticks exactly as CommonMark does, so there
// is nothing to translate, and everything to leave alone: the contents are
// somebody's shell command and every asterisk in it is theirs.
//
// Within one line, and that limit was undocumented until 2026-08-19 when
// FuzzNothingInsideACodeSpanIsTransformed found it in under a second. block.go
// splits the document before anything here runs, so a backtick on one line and
// a backtick on the next are two unterminated runs rather than a span, and each
// is literal text. CommonMark would join them into one span whose newline
// becomes a space.
//
// Deliberately not followed. Joining lines would mean a stray backtick in a
// paragraph swallowing everything down to the next one, which is the failure the
// unterminated rule below exists to avoid, made worse by spanning lines. What it
// costs is that a shell command pasted across two lines is not one span, and
// somebody who wants that has a fenced block.
func scanCode(src string, i int, b *strings.Builder) int {
	ticks := runLength(src, i, '`')
	end := findRun(src, i+ticks, '`', ticks)
	if end < 0 {
		// Unterminated. CommonMark says a run with no partner is literal text,
		// and this agrees: the alternative is treating the rest of the line as
		// code because of one stray backtick.
		b.WriteString(src[i : i+ticks])
		return ticks
	}
	b.WriteString(src[i : end+ticks])
	return end + ticks - i
}

// scanLink translates [text](url) and ![alt](url), returning 0 when what is at
// i is not one.
func scanLink(src string, i int, b *strings.Builder, warn *collector) (int, error) {
	start := i
	image := src[i] == '!'
	if image {
		i++
	}
	if i >= len(src) || src[i] != '[' {
		return 0, nil
	}
	// Nothing can close the bracket, so there is no point looking for the one
	// that does. Without this, a line of nothing but open brackets makes every
	// one of them scan to the end of the line.
	if strings.IndexByte(src[i:], ']') < 0 {
		return 0, nil
	}

	textEnd := matchBracket(src, i)
	if textEnd < 0 || textEnd+1 >= len(src) || src[textEnd+1] != '(' {
		return 0, nil
	}
	urlEnd := strings.IndexByte(src[textEnd+1:], ')')
	if urlEnd < 0 {
		return 0, nil
	}
	urlEnd += textEnd + 1

	text := src[i+1 : textEnd]
	url := src[textEnd+2 : urlEnd]

	// The display half is translated first: a link may hold emphasis, and it
	// may also hold a code span whose contents must survive.
	display, err := translateInline(text, warn)
	if err != nil {
		return 0, err
	}
	if image {
		// Chat cannot show an image inline. A link to it keeps the address,
		// which is the part that cannot be reconstructed from the rest.
		warn.add("Chat cannot show an image in a message, so each one was sent as a link to it")
	}

	link, err := Link(url, display)
	if err != nil {
		return 0, err
	}
	b.WriteString(link)
	return urlEnd + 1 - start, nil
}

// scanEmphasis translates a delimiter run, returning 0 when it does not open
// anything.
//
// This is where CommonMark and Chat disagree most sharply. A single asterisk
// means italic on the way in and bold on the way out, so passing one through
// unchanged inverts what the sender meant.
func scanEmphasis(src string, i int, b *strings.Builder, warn *collector) (int, error) {
	delim := src[i]
	n := runLength(src, i, delim)
	if n > 2 {
		// A run longer than two is not a delimiter this translates, and it is
		// written out whole rather than one byte at a time. Returning zero here
		// would put the scanner back at the second character of the same run,
		// where it would measure the run again, which is how twenty thousand
		// asterisks became a fifth of a second of quadratic work.
		b.WriteString(src[i : i+n])
		return n, nil
	}
	// A single tilde is not emphasis in CommonMark, and it is already
	// strikethrough in Chat, so leaving it alone is both correct readings.
	if delim == '~' && n == 1 {
		return 0, nil
	}
	if !opensEmphasis(src, i+n) {
		return 0, nil
	}

	end := findCloser(src, i+n, delim, n)
	if end < 0 {
		return 0, nil
	}

	inner, err := translateInline(src[i+n:end], warn)
	if err != nil {
		return 0, err
	}

	b.WriteByte(chatDelimiter(delim, n))
	b.WriteString(inner)
	b.WriteByte(chatDelimiter(delim, n))
	return end + n - i, nil
}

// chatDelimiter maps a CommonMark delimiter run onto the Chat one.
//
// The whole table in one function: two asterisks or two underscores mean bold,
// which Chat writes with one asterisk; one of either means italic, which Chat
// writes with an underscore; two tildes mean strikethrough, which Chat writes
// with one.
func chatDelimiter(delim byte, n int) byte {
	switch {
	case delim == '~':
		return '~'
	case n == 2:
		return '*'
	default:
		return '_'
	}
}

// opensEmphasis reports whether a delimiter run ending at i can open one.
//
// CommonMark's flanking rules in the only form that matters here: a run
// followed by a space or by nothing opens nothing. Without this, "2 * 3 * 4"
// becomes italic arithmetic.
func opensEmphasis(src string, i int) bool {
	return i < len(src) && !isSpace(src[i])
}

// findCloser finds the delimiter run that closes one, skipping any code span
// on the way.
//
// Skipping the code spans is what keeps "`a * b`" from closing an emphasis
// opened before it, which would put Chat markup inside somebody's code.
func findCloser(src string, from int, delim byte, n int) int {
	for i := from; i < len(src); {
		if src[i] == '`' {
			ticks := runLength(src, i, '`')
			if end := findRun(src, i+ticks, '`', ticks); end >= 0 {
				i = end + ticks
				continue
			}
		}
		if src[i] == delim && runLength(src, i, delim) == n && closesEmphasis(src, i) {
			return i
		}
		i++
	}
	return -1
}

// closesEmphasis reports whether a run beginning at i can close one: not at
// the very start, and not preceded by a space.
func closesEmphasis(src string, i int) bool {
	return i > 0 && !isSpace(src[i-1])
}

// noteChatMarkup warns when the source already contains Chat markup.
//
// Somebody who typed <users/all> into a message asking for translation almost
// certainly did not mean to notify a whole space, and Chat will read it that
// way whether this tool translates it or not. It is left exactly as written,
// because altering it would be inventing intent, and the warning says what
// will happen before it does.
func noteChatMarkup(src string, i int, warn *collector) {
	rest := src[i:]
	if strings.HasPrefix(rest, "<users/") {
		warn.add("the message already contains Chat mention markup, which will notify people when it is sent")
	}
}

// matchBracket returns the index of the ] that closes the [ at i, respecting
// nesting so that "[a [b] c](url)" finds the right one.
func matchBracket(src string, i int) int {
	depth := 0
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '`':
			ticks := runLength(src, j, '`')
			if end := findRun(src, j+ticks, '`', ticks); end >= 0 {
				j = end + ticks - 1
			}
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// runLength counts how many of c begin at i.
func runLength(src string, i int, c byte) int {
	n := 0
	for i+n < len(src) && src[i+n] == c {
		n++
	}
	return n
}

// findRun finds a run of exactly n of c at or after from.
func findRun(src string, from int, c byte, n int) int {
	for i := from; i < len(src); i++ {
		if src[i] != c {
			continue
		}
		if runLength(src, i, c) == n {
			return i
		}
		i += runLength(src, i, c) - 1
	}
	return -1
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t'
}
