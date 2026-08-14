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

package cli

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/output"
)

// The golden files under testdata/golden are the public output contract
// (SPEC.md §16). An agent parsing --json is a consumer we will never hear from
// until we break it, so a diff here is meant to be loud: it is not a test that
// needs updating, it is a change every caller sees.
//
// `make golden` rewrites them. `make contract` rewrites them and fails if
// anything moved, which is what CI runs.
var update = flag.Bool("update", false, "rewrite the golden files")

// volatile matches the values in `version` output that belong to the machine
// rather than to the contract. The field names stay under test; a toolchain
// bump must not.
var volatile = []*regexp.Regexp{
	regexp.MustCompile(`("go":\s*")[^"]*(")`),
	regexp.MustCompile(`("os":\s*")[^"]*(")`),
	regexp.MustCompile(`("arch":\s*")[^"]*(")`),
	regexp.MustCompile(`(?m)^(go      ).*$`),
	regexp.MustCompile(`(?m)^(os/arch ).*$`),
}

func normalize(s string) string {
	for _, re := range volatile {
		s = re.ReplaceAllString(s, "${1}ELIDED${2}")
	}
	return s
}

type result struct {
	stdout string
	stderr string
	exit   output.ExitCode
}

// runCLI drives the whole command tree the way main does, but against buffers,
// so a test can assert on the streams and the exit code as values. Nothing here
// spawns a subprocess: a test that shells out tests the shell as much as the
// code.
func runCLI(args ...string) result {
	return runWith(strings.NewReader(""), args...)
}

// runCLIIn is the same with something on stdin.
//
// A buffer is not a terminal, which is the point rather than a limitation: it
// is the pipeline case, and the pipeline case is where the rules about
// prompting and about blocking actually apply. t is taken because a caller
// here is invariably also setting an environment variable for the same run, and
// asking for it makes that the obvious thing to do.
func runCLIIn(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	return runWith(strings.NewReader(stdin), args...)
}

func runWith(stdin io.Reader, args ...string) result {
	opts := &Options{}
	root := New(opts)

	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(stdin)
	root.SetArgs(args)

	err := root.Execute()
	exit := output.Report(&errBuf, err, opts.JSON)

	return result{stdout: out.String(), stderr: errBuf.String(), exit: exit}
}

func golden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating the golden directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden at %s: %v\nrun: make golden", path, err)
	}
	if got != string(want) {
		t.Errorf("the output contract changed.\n\n--- want (%s)\n%s\n--- got\n%s\n\n"+
			"If this was deliberate, run 'make golden' and record it in the same commit.",
			path, want, got)
	}
}

func TestGoldenOutputContract(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want output.ExitCode

		// stdin is what the invocation reads. isolated gives it an empty
		// configuration directory and a keyring of its own, which every profile
		// case needs: a golden that depended on the machine's configuration
		// would record that machine.
		stdin    string
		isolated bool
	}{
		{"version.txt", []string{"version"}, output.ExitOK, "", false},
		{"version.json", []string{"version", "--json"}, output.ExitOK, "", false},

		// An unknown command has to be a usage failure and not a help screen.
		// Cobra's own default for a root command with no Run is to print help
		// and exit 0, which tells a script that asked for something that does
		// not exist that it succeeded.
		{"unknown-command.txt", []string{"bogus"}, output.ExitUsage, "", false},
		{"unknown-command.json", []string{"--json", "bogus"}, output.ExitUsage, "", false},

		{"unknown-flag.txt", []string{"--nope"}, output.ExitUsage, "", false},
		{"unknown-flag.json", []string{"--json", "--nope"}, output.ExitUsage, "", false},

		// The profile group. These are the first commands with output worth
		// freezing: an empty list has to be exit 0 with an empty stdout, and
		// the no-URL failure is the message a first-time user meets.
		{"profile-list-empty.txt", []string{"profile", "list"}, output.ExitOK, "", true},
		{"profile-list-empty.json", []string{"--json", "profile", "list"}, output.ExitOK, "", true},

		{"profile-set-webhook.txt", []string{"profile", "set-webhook", "alerts"}, output.ExitOK, testWebhook, true},
		{"profile-set-webhook.json", []string{"--json", "profile", "set-webhook", "alerts"}, output.ExitOK, testWebhook, true},

		{"profile-list-two.txt", []string{"profile", "list"}, output.ExitOK, "", true},
		{"profile-list-two.json", []string{"--json", "profile", "list"}, output.ExitOK, "", true},

		{"profile-set-webhook-no-url.txt", []string{"profile", "set-webhook", "alerts"}, output.ExitUsage, "", true},
		{"profile-set-webhook-no-url.json", []string{"--json", "profile", "set-webhook", "alerts"}, output.ExitUsage, "", true},

		// A removal in a pipeline, where there is nobody to answer the
		// question. Exit 7, and nothing removed.
		{"profile-rm-unconfirmed.txt", []string{"profile", "rm", "alerts"}, output.ExitRefused, "y\n", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.isolated {
				isolate(t)
			}
			if strings.HasPrefix(tc.name, "profile-list-two.") {
				// Two profiles, so that the not-default column is rendered by
				// something. One row can only ever show the true side of every
				// marker.
				for _, name := range []string{"alerts", "releases"} {
					if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", name); setup.exit != output.ExitOK {
						t.Fatalf("setup: exit %d\n%s", setup.exit, setup.stderr)
					}
				}
			}
			if tc.name == "profile-rm-unconfirmed.txt" {
				// Needs something to remove. Not part of the recorded output.
				if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
					t.Fatalf("setup: exit %d\n%s", setup.exit, setup.stderr)
				}
			}

			got := runCLIIn(t, tc.stdin, tc.args...)
			if got.exit != tc.want {
				t.Errorf("exit code = %d, want %d", got.exit, tc.want)
			}

			// Both streams are part of the contract, and which one a thing
			// lands on is the more important half: SPEC.md §11.2 puts results
			// on stdout and everything else on stderr, so that a caller
			// piping stdout into jq is never handed an error object.
			var b bytes.Buffer
			fmt.Fprintf(&b, "exit %d\n", got.exit)
			fmt.Fprintf(&b, "--- stdout\n%s", normalize(got.stdout))
			fmt.Fprintf(&b, "--- stderr\n%s", normalize(got.stderr))
			golden(t, tc.name, b.String())
		})
	}
}

// TestFailureWritesNothingToStdout holds the rule that makes --json safe to
// pipe. A partially written document followed by an error is worse than no
// document at all, because the first one parses.
func TestFailureWritesNothingToStdout(t *testing.T) {
	for _, args := range [][]string{
		{"bogus"},
		{"--json", "bogus"},
		{"--nope"},
		{"--json", "--nope"},
	} {
		got := runCLI(args...)
		if got.exit == output.ExitOK {
			t.Errorf("%v: expected a failure", args)
		}
		if got.stdout != "" {
			t.Errorf("%v: wrote %q to stdout; a failing command writes nothing there", args, got.stdout)
		}
	}
}

// TestLicensesComesFromTheBinary is the whole of SPEC.md §2.4: the notices
// have to be reproducible from a binary somebody was handed on its own, with
// no checkout and no network.
func TestLicensesComesFromTheBinary(t *testing.T) {
	got := runCLI("licenses")
	if got.exit != output.ExitOK {
		t.Fatalf("exit = %d, stderr = %q", got.exit, got.stderr)
	}
	for _, want := range []string{
		"github.com/spf13/cobra",
		"github.com/spf13/pflag",
		// Reached through cobra behind //go:build windows. It is here because
		// it was once absent: a licence file generated on the machine that
		// happened to run the generator is right for that machine only, and
		// the Windows archive would have shipped without this notice.
		"github.com/inconshreveable/mousetrap",
		"Apache License",
	} {
		if !bytes.Contains([]byte(got.stdout), []byte(want)) {
			t.Errorf("`licenses` output does not mention %q; run: make licenses", want)
		}
	}
}
