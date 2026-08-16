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
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Options are what the global flags mean here.
type Options struct {
	JSON  bool
	Quiet bool
	Color bool

	// Interactive is whether there is a person on the other end of the stream
	// this command reads from. A field rather than something Confirm works out
	// for itself, so that the asking path can be tested at all: a test cannot
	// arrange for a real terminal, and a rule nothing exercises is a rule
	// nobody has checked.
	Interactive bool

	// AssumeYes is --yes: every confirmation is answered in advance.
	AssumeYes bool
}

// Renderer writes results to stdout and everything else to stderr.
//
// Both are io.Writer rather than the process's own streams, so that a test can
// read what a command produced without a subprocess, and so that this package
// stays the only one that has to know which stream is which.
type Renderer struct {
	out  io.Writer
	errw io.Writer
	opts Options
}

func NewRenderer(out, errw io.Writer, opts Options) *Renderer {
	return &Renderer{out: out, errw: errw, opts: opts}
}

// Field is one labelled value in a single result.
type Field struct {
	Label string
	Value string
}

// Fields is the text form of a single result: what a person reads when they did
// not ask for --json.
type Fields []Field

// Result writes one result to stdout.
//
// data is the structured form and fields the human one. Both are given because
// they are genuinely different documents: the JSON is a contract with a program
// that will still be parsing it in a year, and the text is for somebody reading
// one line in a terminal. Deriving either from the other produces something bad
// at both jobs.
func (r *Renderer) Result(data any, fields Fields) error {
	if r.opts.JSON {
		return r.encode(r.out, data, true)
	}
	return r.writeFields(fields)
}

// Block writes one result to stdout that is a block of text rather than a set
// of labelled values.
//
// It exists for --dry-run, whose text rendering is an HTTP request: a method
// line, headers, a blank line, a body. That is not label-and-value shaped, and
// forcing it into Fields would produce something that is neither a request
// somebody can compare against the API documentation nor a field list.
//
// stdout, and this is the decision the dry-run card was asked to make. A dry
// run is the result of the command that was asked for: it exits 0, so the rule
// that a failing command writes nothing to stdout is not in play, and in --json
// mode it has to be parseable by whatever the caller pipes it into. Putting it
// on stderr would mean every consumer of a dry run needed 2>&1, mixing it with
// the warnings it is not.
// The text always ends at a line boundary, and one is added when the value does
// not have one. Not an alteration of the value, which is why it is here rather
// than left to each caller: it is the line terminator every line-oriented stream
// ends with, the same one Item writes, and stdout is read by `wc -l`, by a shell
// substitution that strips it anyway, and by a terminal that would otherwise put
// the next prompt in the middle of somebody's message. A dry run already ends
// with one and is unchanged; a message body does not, which is how this was
// found.
func (r *Renderer) Block(data any, text string) error {
	if r.opts.JSON {
		return r.encode(r.out, data, true)
	}

	safe := Sanitize(text)
	if !strings.HasSuffix(safe, "\n") {
		safe += "\n"
	}

	_, err := fmt.Fprint(r.out, safe)
	return err
}

// Item writes one element of a list to stdout.
//
// One object per line in --json mode, with no wrapping array, so that output
// streams: a caller piping into jq sees the first record before the last page
// has been fetched, and `tail` could not emit anything at all if the document
// had to be closed first (SPEC.md §11.2).
//
// In text mode the cells are separated by a single tab and not aligned into
// columns, for the same reason. Alignment requires knowing the widest value,
// which requires holding every row, which is exactly what a streaming list
// cannot do. A tab is also what cut and awk expect.
func (r *Renderer) Item(data any, cells ...string) error {
	if r.opts.JSON {
		return r.encode(r.out, data, false)
	}

	safe := make([]string, len(cells))
	for i, cell := range cells {
		safe[i] = Cell(cell)
	}
	_, err := fmt.Fprintln(r.out, strings.Join(safe, "\t"))
	return err
}

// Warn writes to stderr, unless --quiet.
//
// The message is sanitized after formatting rather than before, so that a
// caller cannot pass a value through the format string and have it reach the
// terminal unexamined. Colour is added afterwards, by this package, which is
// why sanitizing does not erase it.
func (r *Renderer) Warn(format string, a ...any) {
	if r.opts.Quiet {
		return
	}
	message := Sanitize(fmt.Sprintf(format, a...))

	if r.opts.JSON {
		// One object per line, matching the error envelope, so that a caller
		// reading stderr in --json mode gets a stream of documents rather than
		// a mixture of JSON and prose it has to guess at.
		_ = r.encode(r.errw, warningEnvelope{Warning: warningBody{Message: message}}, false)
		return
	}

	// Discarded because a caller cannot act on "we could not tell you about a
	// thing that was only a warning", and the command itself has not failed.
	_, _ = fmt.Fprintf(r.errw, "%s%s\n", r.paint(ansiYellow, "warning: "), message)
}

// Warnings writes each of them. internal/auth returns its warnings rather than
// printing them, because this is the package that prints.
func (r *Renderer) Warnings(warnings []string) {
	for _, warning := range warnings {
		r.Warn("%s", warning)
	}
}

// Logf writes one diagnostic line to stderr: a request, a response, a retry.
//
// This is the sink for --verbose. Whether anything reaches it is the caller's
// decision, not this method's: internal/chat takes a logger and does nothing
// when it is nil, so a run without --verbose costs nothing rather than
// formatting lines that get thrown away.
//
// --quiet wins when both are given. The two flags contradict each other, one of
// them has to lose, and the one asking for less output is the safer loser: a
// pipeline that set --quiet did so because something downstream is reading.
//
// Sanitized like every other line here, because a log line carries values from
// the far end. A response header is chosen by whoever runs the server, and a
// terminal is a program that interprets bytes.
func (r *Renderer) Logf(format string, a ...any) {
	if r.opts.Quiet {
		return
	}
	message := Sanitize(fmt.Sprintf(format, a...))

	if r.opts.JSON {
		_ = r.encode(r.errw, logEnvelope{Log: logBody{Message: message}}, false)
		return
	}

	// Discarded for the reason Warn discards it: a caller cannot act on being
	// unable to read a diagnostic, and the command itself has not failed.
	_, _ = fmt.Fprintln(r.errw, r.paint(ansiDim, message))
}

type warningEnvelope struct {
	Warning warningBody `json:"warning"`
}

type warningBody struct {
	Message string `json:"message"`
}

type logEnvelope struct {
	Log logBody `json:"log"`
}

type logBody struct {
	Message string `json:"message"`
}

// encode writes v as JSON.
//
// HTML escaping is off. Go's encoder turns <, > and & into < and friends
// by default, which is lossless and unreadable for this tool in particular:
// Chat markup writes a link as <https://url|text> and a mention as
// <users/all>, so almost every message body would arrive full of escapes. A
// consumer decodes either form to the same string.
//
// Nothing else about a value is altered. In particular the sanitizing that text
// output does is deliberately not applied here: encoding/json already writes
// every character below U+0020 as an escape, so no control sequence survives
// into a JSON string, and rewriting anything else would break the rule that a
// value is never silently altered to make it representable. --json is read by
// programs, and it hands them what was actually there.
func (r *Renderer) encode(w io.Writer, v any, indent bool) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

func (r *Renderer) writeFields(fields Fields) error {
	width := 0
	for _, field := range fields {
		width = max(width, len(field.Label))
	}

	for _, field := range fields {
		// Padded on the unpainted label, because a colour code is bytes that
		// take no width on screen and would push every value out of line.
		padding := strings.Repeat(" ", width-len(field.Label))
		_, err := fmt.Fprintf(r.out, "%s%s  %s\n",
			r.paint(ansiBold, field.Label), padding, Sanitize(field.Value))
		if err != nil {
			return err
		}
	}
	return nil
}

// Note writes a secondary line to stderr: a hint, or a count, or anything a
// person might want and a script never parses. Suppressed by --quiet.
func (r *Renderer) Note(format string, a ...any) {
	if r.opts.Quiet || r.opts.JSON {
		return
	}
	_, _ = fmt.Fprintln(r.errw, r.paint(ansiDim, Sanitize(fmt.Sprintf(format, a...))))
}
