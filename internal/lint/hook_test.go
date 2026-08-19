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

package lint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The commit-msg hook is a gate and had nothing holding it, which is how it
// spent four milestones unable to see the thing it exists to refuse.
//
// It read `head -1` for the subject. git reads everything up to the first blank
// line and joins it with spaces, so a subject wrapped across two lines is one
// long subject to every reader there will ever be, and this hook measured only
// the first of them. An eighty-character subject landed on a branch that way,
// through a gate written to refuse one, and nobody had to make a mistake to do
// it: an editor that soft-wraps does it for them.
//
// Run as a subprocess rather than reimplemented here. A second copy of the
// rules in Go would be a second thing to disagree with the shell, and what has
// to be true is that the file in .githooks refuses these, because that file is
// what runs on somebody's machine.

// commitMsg runs the hook against a message and reports whether it was allowed.
func commitMsg(t *testing.T, message string) (bool, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	if err := os.WriteFile(path, []byte(message), 0o600); err != nil {
		t.Fatalf("writing the message: %v", err)
	}

	hook := filepath.Join(repoRoot(t), ".githooks", "commit-msg")
	out, err := exec.Command("sh", hook, path).CombinedOutput()
	return err == nil, string(out)
}

// TestTheCommitMessageHookReadsTheSubjectGitWillRecord.
//
// Every refusal here is a shape that reached a commit or could have. The
// wrapped-subject case is the one that actually did, on 2026-08-19, and it is
// written out in full rather than generated so that it reads as the thing that
// happened.
func TestTheCommitMessageHookReadsTheSubjectGitWillRecord(t *testing.T) {
	long := "fix(auth): " + strings.Repeat("a", 62) // 73, one over.
	fits := "fix(auth): " + strings.Repeat("a", 61) // 72, exactly.

	for _, tc := range []struct {
		name    string
		message string
		allowed bool
	}{
		// What a good message looks like, in the shapes it really comes in.
		{
			name:    "a subject and a body",
			message: "fix(chat): name the space in a 404 rather than the URL\n\nA body that explains.\n",
			allowed: true,
		},
		{
			name:    "a subject on its own",
			message: "docs(readme): describe the webhook path\n",
			allowed: true,
		},
		{
			name: "git's own comment lines, which the hook sees and git strips",
			message: "fix(auth): redact a fragment\n\nbody\n" +
				"# Please enter the commit message for your changes.\n# On branch main\n",
			allowed: true,
		},
		{
			name:    "a comment before the subject, which is where an editor puts one",
			message: "# a comment first\nfix(auth): redact a fragment\n\nbody\n",
			allowed: true,
		},
		{
			name:    "a breaking change",
			message: "feat(send)!: change the golden layout\n\nBREAKING CHANGE: a golden moved.\n",
			allowed: true,
		},
		{
			name:    "exactly at the limit",
			message: fits + "\n\nbody\n",
			allowed: true,
		},

		// The defect this test exists for.
		{
			name: "a subject wrapped over two lines, which is what landed",
			message: "feat(auth): finish a login by pasting the callback the browser could not\n" +
				"deliver\n\nbody\n",
		},
		{
			name:    "a body with no blank line before it, which git reads as more subject",
			message: "fix(auth): short subject\nthis is the body with no blank line\n",
		},

		// The rules that were already held, kept here so that a rewrite of the
		// subject extraction cannot quietly drop one.
		{name: "one character over the limit", message: long + "\n\nbody\n"},
		{name: "not a conventional commit", message: "made some changes\n\nbody\n"},
		{name: "a trailing period", message: "fix(auth): redact a fragment.\n\nbody\n"},
		{name: "a markdown heading in the body", message: "fix(auth): redact it\n\n## What changed\n\nbody\n"},
		{name: "a fenced block in the body", message: "fix(auth): redact it\n\n```go\nx := 1\n```\n"},
		{name: "a markdown table in the body", message: "fix(auth): redact it\n\n| a | b |\n"},
		{name: "markdown emphasis in the body", message: "fix(auth): redact it\n\nthis is **bold** text\n"},
		{name: "a markdown link in the body", message: "fix(auth): redact it\n\nsee [the docs](https://x.invalid)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, out := commitMsg(t, tc.message)
			if allowed != tc.allowed {
				verb := "refused"
				if allowed {
					verb = "allowed"
				}
				t.Fatalf("the hook %s this message:\n%s\nit said:\n%s", verb, tc.message, out)
			}
		})
	}
}

// TestTheHookQuotesTheSubjectGitWouldHaveRecorded.
//
// The refusal has to show the joined subject and not the first line of the
// file, because the first line is the thing that looked fine and the joined one
// is the thing that is wrong. A message that refuses without showing what it
// objected to sends somebody looking at the wrong line.
func TestTheHookQuotesTheSubjectGitWouldHaveRecorded(t *testing.T) {
	allowed, out := commitMsg(t,
		"feat(auth): finish a login by pasting the callback the browser could not\ndeliver\n\nbody\n")
	if allowed {
		t.Fatal("a wrapped subject was allowed")
	}

	joined := "feat(auth): finish a login by pasting the callback the browser could not deliver"
	if !strings.Contains(out, joined) {
		t.Errorf("the refusal does not show the subject git would record:\n%s", out)
	}
}
