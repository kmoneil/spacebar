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
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// ColorFor reports whether ANSI escapes may be written to w.
//
// It asks about w rather than about the process, which is the difference
// between describing the stream a command will actually write to and
// describing the one it usually writes to. Anything that is not a file is not a
// terminal: a buffer, a pipe wrapper, or anything a test substitutes.
func ColorFor(w io.Writer, noColorFlag bool) bool {
	f, ok := w.(*os.File)
	return ok && UseColor(f, noColorFlag)
}

// Interactive reports whether in is something a person is typing into.
//
// This asks about the reader for a reason that cost a test to find. The
// obvious version asks CanPrompt, which describes os.Stdin, and a command
// reads from whatever it was given: in production those are the same file and
// in a test they are not, so a prompt-refusal rule verified against os.Stdin is
// verified against a stream nothing reads.
//
// A caveat that is cheaper to write down than to fix. os.ModeCharDevice, which
// SPEC.md §11.3 specifies and which this uses, is set for /dev/null as much as
// for a terminal, so `command < /dev/null` is treated as interactive. Telling
// them apart needs a termios call and therefore a dependency, and the
// consequence here is bounded: the question is asked, the read returns nothing,
// and nothing is what a refusal is made of. The command exits 7 either way.
func Interactive(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && IsTTY(f)
}

// IsInteractive reports whether there is a person on the other end.
//
// Exposed so that a command can decide whether an instruction is worth
// printing. "Paste it and press Ctrl-D" helps somebody at a keyboard and is
// noise in a log.
func (r *Renderer) IsInteractive() bool { return r.opts.Interactive }

// Confirm asks question and returns nil only if the answer was yes.
//
// The refusal is an error rather than a boolean, because every caller of this
// does the same thing with a no, and a boolean invites one of them to do
// something else. Exit 7, which a script can tell apart from a failure.
//
// Three answers, in order. --yes was given, so there is nothing to ask.
// Nobody is there to ask, which is a refusal rather than a default: SPEC.md
// §11.3 makes that a hard rule, because a CLI that blocks on a prompt inside a
// pipeline hangs whatever is driving it, and a hung agent is strictly worse
// than a failed one. Otherwise the question is asked.
//
// The question goes to stderr, like everything that is not a result, so that a
// caller reading stdout is never handed a prompt in the middle of a document.
func (r *Renderer) Confirm(in io.Reader, format string, a ...any) error {
	question := Sanitize(fmt.Sprintf(format, a...))

	if r.opts.AssumeYes {
		return nil
	}
	if !r.opts.Interactive {
		return &Error{
			Code: "CONFIRMATION_REQUIRED",
			Exit: ExitRefused,
			Message: "not confirmed: stdin is not a terminal, so there was nobody to ask.\n" +
				"The question was: " + question + "\n" +
				"Pass --yes to answer it in advance.",
		}
	}

	_, _ = fmt.Fprintf(r.errw, "%s [y/N] ", r.paint(ansiBold, question))

	// The read error is deliberately not distinguished from a no. A stream that
	// ended without an answer has not agreed to anything, and io.EOF arrives
	// alongside a final line rather than instead of it, so a caller who typed
	// yes and closed the stream in the same breath is still read as a yes.
	answer, _ := bufio.NewReader(in).ReadString('\n')

	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}

	// Anything else is no, including the empty line that Enter produces. The
	// default has to be the one that changes nothing, because Enter is what
	// somebody presses when they have stopped reading.
	return &Error{
		Code:    "CONFIRMATION_REQUIRED",
		Exit:    ExitRefused,
		Message: "not confirmed, so nothing was changed.",
	}
}
