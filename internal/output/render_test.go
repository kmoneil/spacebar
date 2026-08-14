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
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type message struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

func render(opts Options) (*Renderer, *bytes.Buffer, *bytes.Buffer) {
	var out, errw bytes.Buffer
	return NewRenderer(&out, &errw, opts), &out, &errw
}

func TestResultAsJSONIsOneObject(t *testing.T) {
	r, out, errw := render(Options{JSON: true})

	if err := r.Result(message{Name: "spaces/AAAA1111/messages/x", Text: "deploy done"}, nil); err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if errw.Len() != 0 {
		t.Errorf("a result wrote to stderr: %q", errw.String())
	}

	var got message
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\ngot: %s", err, out.String())
	}
	if got.Text != "deploy done" {
		t.Errorf("text = %q", got.Text)
	}
}

func TestResultAsTextAlignsItsLabels(t *testing.T) {
	r, out, _ := render(Options{})

	err := r.Result(nil, Fields{
		{Label: "space", Value: "spaces/AAAA1111"},
		{Label: "message", Value: "spaces/AAAA1111/messages/x"},
	})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}

	want := "space    spaces/AAAA1111\nmessage  spaces/AAAA1111/messages/x\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

// TestListIsNDJSONWithNoWrappingArray is the claim from the card. A wrapping
// array cannot be written until the last record is known, so a list that had
// one could not stream, and `tail` could never emit anything at all.
func TestListIsNDJSONWithNoWrappingArray(t *testing.T) {
	r, out, _ := render(Options{JSON: true})

	for _, m := range []message{
		{Name: "spaces/A/messages/1", Text: "first"},
		{Name: "spaces/A/messages/2", Text: "second"},
		{Name: "spaces/A/messages/3", Text: "third"},
	} {
		if err := r.Item(m, m.Name, m.Text); err != nil {
			t.Fatalf("rendering: %v", err)
		}
	}

	body := out.String()
	if strings.HasPrefix(body, "[") {
		t.Error("the list is wrapped in an array, so it cannot stream")
	}

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), body)
	}
	for i, line := range lines {
		var got message
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d is not a complete object on its own: %v\n%s", i+1, err, line)
		}
	}
}

func TestListAsTextIsTabSeparated(t *testing.T) {
	r, out, _ := render(Options{})

	if err := r.Item(nil, "spaces/A/messages/1", "deploy done"); err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if want := "spaces/A/messages/1\tdeploy done\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// TestADataColumnCannotDriveTheTerminal is the invariant this package exists
// for, checked at the point a body actually reaches a stream rather than at the
// escaper it goes through.
func TestADataColumnCannotDriveTheTerminal(t *testing.T) {
	hostileBody := "deploy done\x1b[2J\x1b]0;pwned\x07\rall clear"

	t.Run("as a field", func(t *testing.T) {
		r, out, _ := render(Options{})
		if err := r.Result(nil, Fields{{Label: "text", Value: hostileBody}}); err != nil {
			t.Fatalf("rendering: %v", err)
		}
		assertInert(t, out.String())
	})

	t.Run("as a cell", func(t *testing.T) {
		r, out, _ := render(Options{})
		if err := r.Item(nil, "spaces/A/messages/1", hostileBody); err != nil {
			t.Fatalf("rendering: %v", err)
		}
		assertInert(t, out.String())
		if lines := strings.Count(out.String(), "\n"); lines != 1 {
			t.Errorf("a body forged %d rows", lines)
		}
		if fields := strings.Count(out.String(), "\t"); fields != 1 {
			t.Errorf("a body forged a column: %d tabs", fields)
		}
	})

	t.Run("as a warning", func(t *testing.T) {
		r, _, errw := render(Options{})
		r.Warn("%s", hostileBody)
		assertInert(t, errw.String())
	})
}

func assertInert(t *testing.T, got string) {
	t.Helper()
	if strings.ContainsAny(got, "\x1b\r") {
		t.Errorf("the output can drive the terminal: %q", got)
	}
}

// TestColourIsNeverDataDerived holds the other half of the escaping rule. The
// only strings that keep their escapes are the constants in this package, so
// turning colour on cannot turn a message body back into a control sequence.
func TestColourIsNeverDataDerived(t *testing.T) {
	r, out, _ := render(Options{Color: true})

	if err := r.Result(nil, Fields{{Label: "text", Value: "body\x1b[2J"}}); err != nil {
		t.Fatalf("rendering: %v", err)
	}

	body := out.String()
	if !strings.Contains(body, ansiBold) {
		t.Error("colour is on and the label is not painted")
	}
	if strings.Contains(body, "\x1b[2J") {
		t.Error("the body's own escape survived")
	}
	// The label is painted and the value is not, so the only escapes present
	// are the ones this package wrote.
	if want := 2; strings.Count(body, "\x1b") != want {
		t.Errorf("got %d escapes, want %d (the open and the reset)", strings.Count(body, "\x1b"), want)
	}
}

func TestQuietSilencesStderrAndNotStdout(t *testing.T) {
	r, out, errw := render(Options{Quiet: true})

	r.Warn("the keyring is unavailable")
	r.Note("3 spaces")
	if errw.Len() != 0 {
		t.Errorf("--quiet still wrote to stderr: %q", errw.String())
	}

	if err := r.Result(nil, Fields{{Label: "space", Value: "spaces/AAAA1111"}}); err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if out.Len() == 0 {
		t.Error("--quiet suppressed the result, which is data and was asked for")
	}
}

// TestWarningsAreJSONInJSONMode keeps stderr parseable as a stream of
// documents. A consumer that gets a JSON object for an error and a bare line
// for a warning has to guess which it is holding.
func TestWarningsAreJSONInJSONMode(t *testing.T) {
	r, out, errw := render(Options{JSON: true})
	r.Warn("read a credential from %s", "/home/x/.config/spacebar/credentials.json")

	if out.Len() != 0 {
		t.Errorf("a warning reached stdout: %q", out.String())
	}

	var env struct {
		Warning struct {
			Message string `json:"message"`
		} `json:"warning"`
	}
	if err := json.Unmarshal(errw.Bytes(), &env); err != nil {
		t.Fatalf("the warning is not valid JSON: %v\ngot: %s", err, errw.String())
	}
	if !strings.Contains(env.Warning.Message, "credentials.json") {
		t.Errorf("message = %q", env.Warning.Message)
	}
}

// TestChatMarkupSurvivesJSONReadably is why HTML escaping is off. Chat writes a
// link as <url|text> and a mention as <users/all>, so the default encoder would
// fill almost every message body with escapes for no gain: a consumer decodes
// either form to the same string.
func TestChatMarkupSurvivesJSONReadably(t *testing.T) {
	r, out, _ := render(Options{JSON: true})

	body := "see <https://example.invalid|the run> and ask <users/all>"
	if err := r.Result(message{Name: "spaces/A/messages/1", Text: body}, nil); err != nil {
		t.Fatalf("rendering: %v", err)
	}

	// Written with an escaped backslash rather than as the six characters it
	// stands for, so that what this looks for cannot be mistaken for a literal
	// angle bracket by the next person to read it.
	if strings.Contains(out.String(), "\\u003c") {
		t.Errorf("the encoder escaped the markup:\n%s", out.String())
	}

	var got message
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if got.Text != body {
		t.Errorf("the body did not round-trip:\n got %q\nwant %q", got.Text, body)
	}
}

// TestJSONDoesNotAlterAValue is the other side of sanitizing. Text output
// escapes because it goes to a terminal; --json goes to a program, and a value
// rewritten to be printable is a wrong answer that looks like a right one.
func TestJSONDoesNotAlterAValue(t *testing.T) {
	r, out, _ := render(Options{JSON: true})

	body := "deploy done\x1b[2J\ttabbed\nnewline"
	if err := r.Result(message{Text: body}, nil); err != nil {
		t.Fatalf("rendering: %v", err)
	}

	var got message
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if got.Text != body {
		t.Errorf("the value was altered:\n got %q\nwant %q", got.Text, body)
	}

	// Lossless and still inert on the wire: encoding/json writes everything
	// below U+0020 as an escape, so no control sequence is in the bytes.
	if strings.ContainsAny(out.String(), "\x1b\r") {
		t.Errorf("a raw control character reached the JSON output:\n%q", out.String())
	}
}
