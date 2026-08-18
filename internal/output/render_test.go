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
	"io"
	"runtime"
	"strings"
	"sync"
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

// TestALogLineGoesToStderrAndIsEscaped.
//
// This is the sink for --verbose, and what reaches it comes from the far end: a
// response status, a header value, a reason somebody else's server chose. stdout
// carries data and nothing else, so a diagnostic that landed there would corrupt
// the output of whatever is parsing it, and a terminal is a program that
// interprets bytes, so the line is escaped like every other one here.
func TestALogLineGoesToStderrAndIsEscaped(t *testing.T) {
	r, out, errw := render(Options{})

	r.Logf("< %s in %s", "429 Too Many\x1b[2JMuch", "12ms")

	if out.Len() != 0 {
		t.Errorf("a log line reached stdout: %q", out.String())
	}
	if strings.Contains(errw.String(), "\x1b[2J") {
		t.Errorf("a log line carried an escape sequence to the terminal: %q", errw.String())
	}
	if !strings.Contains(errw.String(), "429 Too Many") {
		t.Errorf("the log line lost its content: %q", errw.String())
	}
}

// TestALogLineIsJSONInJSONMode, matching the error and warning envelopes, so
// that stderr in --json mode is a stream of documents rather than a mixture a
// consumer has to guess at.
func TestALogLineIsJSONInJSONMode(t *testing.T) {
	r, _, errw := render(Options{JSON: true})

	r.Logf("> POST %s", "https://chat.example/v1/spaces/AAAA1111/messages?key=REDACTED")

	var got struct {
		Log struct {
			Message string `json:"message"`
		} `json:"log"`
	}
	if err := json.Unmarshal(errw.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\ngot: %s", err, errw.String())
	}
	if !strings.HasPrefix(got.Log.Message, "> POST https://chat.example/") {
		t.Errorf("log message = %q", got.Log.Message)
	}
}

// TestQuietSilencesTheLog. The two flags contradict each other, one of them has
// to lose, and the one asking for less output is the safer loser: a pipeline
// that set --quiet did so because something downstream is reading.
func TestQuietSilencesTheLog(t *testing.T) {
	for _, opts := range []Options{{Quiet: true}, {Quiet: true, JSON: true}} {
		r, out, errw := render(opts)
		r.Logf("a diagnostic")

		if errw.Len() != 0 || out.Len() != 0 {
			t.Errorf("--quiet wrote a log line: stdout %q stderr %q", out.String(), errw.String())
		}
	}
}

// TestABlockEndsAtALineBoundary.
//
// `messages get` writes a message body through Block, and a body does not end
// with a newline. Without one the shell prompt lands in the middle of somebody's
// message, `wc -l` is short by one, and a redirect produces a file whose last
// line is not a line. Block was written for --dry-run, whose text already ends
// with one, so nothing noticed until a real message came back from the API.
func TestABlockEndsAtALineBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"a message body", "deploy done", "deploy done\n"},
		{"a body of several lines", "one\ntwo", "one\ntwo\n"},

		// Already terminated, as a dry run is. Exactly one newline, because a
		// second would be a blank line in output somebody is comparing against
		// the API documentation.
		{"a dry run", "GET /v1/spaces\nAuthorization: REDACTED\n", "GET /v1/spaces\nAuthorization: REDACTED\n"},

		// A message can be empty: a card-only message has no text at all. A
		// blank line is the right answer, because zero bytes on stdout is what
		// a failure looks like.
		{"an empty body", "", "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, out, _ := render(Options{})
			if err := r.Block(nil, tc.text); err != nil {
				t.Fatalf("Block: %v", err)
			}
			if out.String() != tc.want {
				t.Errorf("Block wrote %q, want %q", out.String(), tc.want)
			}
		})
	}
}

// TestBlockAddsNoSecondNewlineInJSONMode.
//
// The encoder terminates its own document, so the line-boundary rule above must
// not reach this branch and append another. A blank line after a JSON document
// is legal and harmless to a parser, which is exactly why it would go unnoticed
// while quietly making every golden file disagree with what a program sees.
func TestBlockAddsNoSecondNewlineInJSONMode(t *testing.T) {
	r, out, _ := render(Options{JSON: true})
	if err := r.Block(message{Name: "spaces/AAA/messages/BBB", Text: "deploy done"}, "deploy done"); err != nil {
		t.Fatalf("Block: %v", err)
	}

	if !json.Valid(bytes.TrimSpace(out.Bytes())) {
		t.Errorf("a JSON block is not valid JSON:\n%q", out.String())
	}
	if !strings.HasSuffix(out.String(), "}\n") {
		t.Errorf("a JSON block does not end at its closing brace and one newline:\n%q", out.String())
	}

	// The text half is ignored in JSON mode rather than appended to the
	// document, which is the other way this could go wrong.
	if strings.Count(out.String(), "deploy done") != 1 {
		t.Errorf("the body appears more than once, so the text half leaked into JSON mode:\n%q", out.String())
	}
}

// TestConcurrentWritersProduceWholeLines.
//
// SPEC.md §14.2 promises the MCP audit trail is one JSON object per line, and
// calls it a security control rather than a courtesy. The MCP server serves
// tool calls concurrently, so that promise is made by two goroutines sharing
// one Renderer: the audit line for one call and the --verbose log of another.
//
// This was already safe in production, and the reason is why it is worth a
// test. Renderer takes an io.Writer, the command hands it os.Stderr, and
// internal/poll holds a per-descriptor lock across a whole write. Concurrent
// writes to a *os.File therefore cannot interleave, and trying to make one
// interleave, with 60KB lines and a reader slow enough to fill the pipe, does
// not work.
//
// None of which is a property of this type. It is a property of the writer it
// happens to be given, undocumented where it was relied on, and gone the moment
// somebody wraps stderr. So the writer here is a bytes.Buffer, which has no
// lock of its own: this fails under -race without the mutex, and asserts the
// promise rather than the accident.
func TestConcurrentWritersProduceWholeLines(t *testing.T) {
	var errw safeBuffer
	r := NewRenderer(io.Discard, &errw, Options{})

	// Long enough to be split by any writer that can be, which is the shape
	// that corrupts a neighbouring line rather than merely reordering it.
	long := strings.Repeat("x", 6000)

	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Logf("< %s", long)
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			line, err := json.Marshal(map[string]any{"tool": "send_message", "ok": true, "n": i})
			if err != nil {
				t.Errorf("Marshal: %v", err)
				return
			}
			r.Audit(string(line))
		}()
	}
	wg.Wait()

	audit := 0
	for _, line := range strings.Split(strings.TrimRight(errw.String(), "\n"), "\n") {
		if !strings.Contains(line, `"tool"`) {
			continue
		}
		audit++

		// The claim: an audit line is one whole JSON object, with nothing
		// spliced into it and nothing of it spliced into anything else.
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("an audit line is not one JSON object (%d bytes):\n%.200s", len(line), line)
		}
	}
	if audit != 40 {
		t.Errorf("found %d audit lines, want 40", audit)
	}
}

// safeBuffer is a bytes.Buffer with a lock of its own, so that the test asserts
// the Renderer's synchronisation rather than crashing on the buffer's absence
// of any. Without it the race detector reports the buffer rather than the thing
// under test, which is a true report about the wrong subject.
type safeBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	// Written in two halves, with the lock released between them. A writer is
	// allowed to do this and several do: a bufio.Writer flushing mid-line, a
	// network writer, anything wrapping stderr. The gap is the whole point,
	// because it is where another goroutine's line lands when nothing above is
	// holding it off. Each half is locked on its own, so the buffer itself is
	// race-free and the race detector reports the subject rather than the prop.
	if len(p) > 1 {
		half := len(p) / 2
		b.append(p[:half])
		runtime.Gosched()
		b.append(p[half:])
		return len(p), nil
	}
	b.append(p)
	return len(p), nil
}

func (b *safeBuffer) append(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, _ = b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
