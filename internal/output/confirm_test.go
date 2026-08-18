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
	"errors"
	"os"
	"strings"
	"testing"
)

// TestConfirmDefaultsToNo.
//
// Enter is what somebody presses when they have stopped reading, so it has to
// mean the thing that changes nothing. Anything that is not an explicit yes is
// a no, including a typo: a confirmation that accepted "yez" as agreement would
// be a confirmation in name only.
func TestConfirmDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		answer string
		agreed bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"  yes  \n", true},
		{"YES\n", true},

		{"\n", false},
		{"n\n", false},
		{"no\n", false},
		{"yez\n", false},
		{"", false},
		{"anything else\n", false},

		// Typed without a trailing newline, then the stream closed. The answer
		// arrives alongside io.EOF rather than instead of it, and somebody who
		// typed yes said yes.
		{"y", true},
	} {
		t.Run(strings.TrimSpace(tc.answer), func(t *testing.T) {
			r, out, errw := render(Options{Interactive: true})

			err := r.Confirm(strings.NewReader(tc.answer), "Remove profile %q?", "alerts")
			if tc.agreed && err != nil {
				t.Fatalf("answer %q was read as a refusal: %v", tc.answer, err)
			}
			if !tc.agreed {
				if err == nil {
					t.Fatalf("answer %q was read as agreement", tc.answer)
				}
				if got := ExitCodeOf(err); got != ExitRefused {
					t.Errorf("exit code = %d, want %d", got, ExitRefused)
				}
			}

			// The question is not a result. A caller piping stdout into jq must
			// never be handed a prompt in the middle of a document.
			if out.Len() != 0 {
				t.Errorf("the question reached stdout: %q", out.String())
			}
			if !strings.Contains(errw.String(), "Remove profile \"alerts\"?") {
				t.Errorf("the question was not asked: %q", errw.String())
			}
		})
	}
}

// TestConfirmRefusesWhenThereIsNobodyToAsk holds the rule from SPEC.md §11.3.
//
// A CLI that blocks on a prompt inside a pipeline hangs whatever is driving it,
// and a hung agent is strictly worse than a failed one: a failure gets
// reported, a hang gets a timeout somebody has to go and find. So the answer is
// exit 7 rather than a default, and stdin is not read at all, which is what
// stops the block.
func TestConfirmRefusesWhenThereIsNobodyToAsk(t *testing.T) {
	r, out, errw := render(Options{Interactive: false})

	// A reader that fails the test if it is touched. Reading it at all is the
	// bug: even a reader with an answer in it would be a pipeline this command
	// consumed a line of.
	err := r.Confirm(readerThatMustNotBeUsed{t}, "Remove profile %q?", "alerts")
	if err == nil {
		t.Fatal("a confirmation that could not be asked for was treated as agreement")
	}
	if got := ExitCodeOf(err); got != ExitRefused {
		t.Errorf("exit code = %d, want %d", got, ExitRefused)
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("the refusal does not say how to answer in advance:\n%v", err)
	}
	if out.Len() != 0 || errw.Len() != 0 {
		t.Errorf("a question was asked with nobody there: stdout %q stderr %q", out.String(), errw.String())
	}
}

// TestYesAnswersInAdvance, and does so without reading stdin, because a command
// given --yes has no question left and a pipeline it read from would be a
// pipeline it stole a line from.
func TestYesAnswersInAdvance(t *testing.T) {
	for _, opts := range []Options{
		{AssumeYes: true, Interactive: true},
		{AssumeYes: true, Interactive: false},
	} {
		r, out, errw := render(opts)

		if err := r.Confirm(readerThatMustNotBeUsed{t}, "Remove profile %q?", "alerts"); err != nil {
			t.Errorf("--yes did not answer the question: %v", err)
		}
		if out.Len() != 0 || errw.Len() != 0 {
			t.Errorf("--yes still asked: stdout %q stderr %q", out.String(), errw.String())
		}
	}
}

// TestTheQuestionIsEscaped. A profile name is chosen by whoever ran the
// command, but the same path will carry a space's display name before long, and
// that comes from people the operator may not know. A terminal is a program
// that interprets bytes.
func TestTheQuestionIsEscaped(t *testing.T) {
	r, _, errw := render(Options{Interactive: true})

	_ = r.Confirm(strings.NewReader("n\n"), "Remove %q?", "alerts\x1b[2Jgone")
	if strings.Contains(errw.String(), "\x1b[2J") {
		t.Errorf("an escape sequence reached the terminal through a question: %q", errw.String())
	}
}

type readerThatMustNotBeUsed struct{ t *testing.T }

func (r readerThatMustNotBeUsed) Read([]byte) (int, error) {
	r.t.Error("stdin was read when there was no question to ask")
	return 0, errors.New("stdin must not be read here")
}

// TestInteractiveAsksAboutTheStreamRatherThanTheProcess.
//
// This is the function whose absence was a bug. The version that asked about
// os.Stdin described a stream nothing reads: every command reads from whatever
// it was handed, so the prompt-refusal rule was being verified against the
// wrong file. Anything that is not a file is not a terminal, which covers a
// buffer, a pipe wrapper, and everything a test substitutes.
func TestInteractiveAsksAboutTheStreamRatherThanTheProcess(t *testing.T) {
	if Interactive(strings.NewReader("y\n")) {
		t.Error("a buffer was treated as a terminal")
	}
	if Interactive(nil) {
		t.Error("a nil reader was treated as a terminal")
	}

	// A real file that is not a character device: the closest a test can get to
	// "stdin redirected from a file", which is the case that must not prompt.
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("creating a file: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if Interactive(f) {
		t.Error("a regular file was treated as a terminal")
	}
}

// TestColorForAsksAboutTheStreamToo, and refuses anything that is not a file
// for the same reason: a buffer has no terminal behind it, so escapes written
// to one are bytes somebody stored.
func TestColorForAsksAboutTheStreamToo(t *testing.T) {
	var buf strings.Builder
	if ColorFor(&buf, false) {
		t.Error("colour was enabled for a buffer")
	}
	if ColorFor(os.Stdout, true) {
		t.Error("--no-color was ignored")
	}
}

// TestIsInteractiveReportsWhatWasConfigured, which is what a command asks
// before printing an instruction only a person can act on.
func TestIsInteractiveReportsWhatWasConfigured(t *testing.T) {
	for _, want := range []bool{true, false} {
		r, _, _ := render(Options{Interactive: want})
		if r.IsInteractive() != want {
			t.Errorf("IsInteractive = %v, want %v", r.IsInteractive(), want)
		}
	}
}
