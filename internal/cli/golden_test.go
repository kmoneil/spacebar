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
	"net/http"
	"net/http/httptest"
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

	// A dry run prints the URL it would have posted to, and under test that is
	// a loopback server on whatever port the kernel handed out. The port is the
	// machine; the rest of the line, including the redaction, is the contract.
	regexp.MustCompile(`(http://127\.0\.0\.1:)\d+(/)`),

	// The path to the configuration file, which several refusals name so that
	// somebody can go and look at it. Under test it is a t.TempDir, so it is a
	// different absolute path on every run and on every machine.
	//
	// Added after a golden recorded one and would have failed `make contract`
	// on the very next invocation. That is the failure mode worth guarding: an
	// unstable golden does not announce itself as unstable, it announces itself
	// as the output contract having changed, which is the sentence somebody
	// acts on.
	regexp.MustCompile(`(default in )[^\n]*(spacebar/config\.json)`),
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

		// serve stands up a Chat-shaped server and feeds its webhook URL in on
		// stdin. The port is random and the recorded output does not contain
		// it, which is what makes this deterministic: what is frozen is the
		// shape of a verified setup, not the address it reached.
		serve bool
	}{
		{"version.txt", []string{"version"}, output.ExitOK, "", false, false},
		{"version.json", []string{"version", "--json"}, output.ExitOK, "", false, false},

		// An unknown command has to be a usage failure and not a help screen.
		// Cobra's own default for a root command with no Run is to print help
		// and exit 0, which tells a script that asked for something that does
		// not exist that it succeeded.
		{"unknown-command.txt", []string{"bogus"}, output.ExitUsage, "", false, false},
		{"unknown-command.json", []string{"--json", "bogus"}, output.ExitUsage, "", false, false},

		{"unknown-flag.txt", []string{"--nope"}, output.ExitUsage, "", false, false},
		{"unknown-flag.json", []string{"--json", "--nope"}, output.ExitUsage, "", false, false},

		// The profile group. These are the first commands with output worth
		// freezing: an empty list has to be exit 0 with an empty stdout, and
		// the no-URL failure is the message a first-time user meets.
		{"profile-list-empty.txt", []string{"profile", "list"}, output.ExitOK, "", true, false},
		{"profile-list-empty.json", []string{"--json", "profile", "list"}, output.ExitOK, "", true, false},

		{"profile-set-webhook.txt", []string{"profile", "set-webhook", "alerts"}, output.ExitOK, testWebhook, true, false},
		{"profile-set-webhook.json", []string{"--json", "profile", "set-webhook", "alerts"}, output.ExitOK, testWebhook, true, false},

		{"profile-list-two.txt", []string{"profile", "list"}, output.ExitOK, "", true, false},
		{"profile-list-two.json", []string{"--json", "profile", "list"}, output.ExitOK, "", true, false},

		{"profile-set-webhook-no-url.txt", []string{"profile", "set-webhook", "alerts"}, output.ExitUsage, "", true, false},
		{"profile-set-webhook-no-url.json", []string{"--json", "profile", "set-webhook", "alerts"}, output.ExitUsage, "", true, false},

		// A removal in a pipeline, where there is nobody to answer the
		// question. Exit 7, and nothing removed.
		{"profile-rm-unconfirmed.txt", []string{"profile", "rm", "alerts"}, output.ExitRefused, "y\n", true, false},

		{"profile-set-webhook-verified.txt", []string{"profile", "set-webhook", "alerts", "--verify"}, output.ExitOK, "", true, true},
		{"profile-set-webhook-verified.json", []string{"--json", "profile", "set-webhook", "alerts", "--verify"}, output.ExitOK, "", true, true},

		// send. The output shape here is the one an agent parses, so a diff in
		// any of these is a change every caller sees.
		{"send.txt", []string{"send", "deploy done"}, output.ExitOK, "", true, true},
		{"send.json", []string{"--json", "send", "deploy done"}, output.ExitOK, "", true, true},

		{"send-dry-run.txt", []string{"send", "--dry-run", "deploy done"}, output.ExitOK, "", true, true},
		{"send-dry-run.json", []string{"--json", "send", "--dry-run", "deploy done"}, output.ExitOK, "", true, true},

		// The first golden of the warning envelope, which is why --md and a
		// table: Chat has no tables, so the rows arrive as lines of text and
		// somebody has to be told.
		{"send-warning.txt", []string{"send", "--md", "deploy **done**\n\n| a | b |\n| - | - |\n| 1 | 2 |"}, output.ExitOK, "", true, true},
		{"send-warning.json", []string{"--json", "send", "--md", "deploy **done**\n\n| a | b |\n| - | - |\n| 1 | 2 |"}, output.ExitOK, "", true, true},

		{"send-no-arguments.txt", []string{"send"}, output.ExitUsage, "", true, true},
		{"send-no-arguments.json", []string{"--json", "send"}, output.ExitUsage, "", true, true},

		{"send-flag-conflict.txt", []string{"send", "--message-id", "client-a", "--idempotent", "hi"}, output.ExitUsage, "", true, true},

		{"send-unsupported.txt", []string{"send", "--file", "report.pdf", "hi"}, output.ExitUnsupported, "", true, true},
		{"send-unsupported.json", []string{"--json", "send", "--file", "report.pdf", "hi"}, output.ExitUnsupported, "", true, true},

		{"send-wrong-space.txt", []string{"send", "spaces/BBBBSomewhereElse", "hi"}, output.ExitUsage, "", true, true},

		// A dry run of the setup command, which stores nothing and sends
		// nothing. The second half of that is the interesting one: --dry-run on
		// a command that writes to disk has to mean the whole command, not the
		// network half of it.
		{"profile-set-webhook-dry-run.txt", []string{"--dry-run", "profile", "set-webhook", "alerts"}, output.ExitOK, "", true, true},
		{"profile-set-webhook-verify-dry-run.txt", []string{"--dry-run", "profile", "set-webhook", "alerts", "--verify"}, output.ExitOK, "", true, true},

		// The read commands on a webhook profile. This is the shape m3-04's
		// first claim is about: a read on a write-only transport is exit 5, and
		// what is frozen here is that the message names the profile, says what a
		// webhook is, and points at the transport that can.
		//
		// It is the failure most users of this tool will actually meet, because
		// the population the project is built for is the one an incoming webhook
		// is all they get.
		{"spaces-list-unsupported.txt", []string{"spaces", "list"}, output.ExitUnsupported, "", true, true},
		{"spaces-list-unsupported.json", []string{"--json", "spaces", "list"}, output.ExitUnsupported, "", true, true},

		{"messages-list-unsupported.txt", []string{"messages", "list", "spaces/AAAATestSpace"}, output.ExitUnsupported, "", true, true},
		{"messages-list-unsupported.json", []string{"--json", "messages", "list", "spaces/AAAATestSpace"}, output.ExitUnsupported, "", true, true},

		{"spaces-members-unsupported.txt", []string{"spaces", "members", "spaces/AAAATestSpace"}, output.ExitUnsupported, "", true, true},
		{"messages-get-unsupported.txt", []string{"messages", "get", "spaces/AAAATestSpace/messages/BBB"}, output.ExitUnsupported, "", true, true},

		// Usage failures. A profile is configured for these the way it is for
		// send-no-arguments, so that what the golden records is the usage failure
		// rather than the missing-profile one that would come first.
		{"messages-list-no-arguments.txt", []string{"messages", "list"}, output.ExitUsage, "", true, true},
		{"messages-list-no-arguments.json", []string{"--json", "messages", "list"}, output.ExitUsage, "", true, true},

		{"messages-list-bad-order.txt", []string{"messages", "list", "spaces/AAAATestSpace", "--order", "sideways"}, output.ExitUsage, "", true, true},

		{"spaces-get-no-arguments.txt", []string{"spaces", "get"}, output.ExitUsage, "", true, true},

		{"alias-set-no-arguments.txt", []string{"alias", "set"}, output.ExitUsage, "", true, true},
		{"alias-set-looks-like-a-space.txt", []string{"alias", "set", "spaces/AAAA", "spaces/BBBB"}, output.ExitUsage, "", true, true},
		{"alias-set-looks-like-an-address.txt", []string{"alias", "set", "bob@example.test", "spaces/BBBB"}, output.ExitUsage, "", true, true},
		{"alias-rm-no-such-alias.txt", []string{"alias", "rm", "nothing"}, output.ExitUsage, "", true, true},
		{"messages-list-bad-time.txt", []string{"messages", "list", "spaces/AAAATestSpace", "--since", "yesterday"}, output.ExitUsage, "", true, true},

		{"messages-edit-no-arguments.txt", []string{"messages", "edit"}, output.ExitUsage, "", true, true},
		{"messages-edit-unsupported.txt", []string{"messages", "edit", "spaces/AAAATestSpace/messages/BBBB", "text"}, output.ExitUnsupported, "", true, true},
		{"messages-delete-no-arguments.txt", []string{"messages", "delete"}, output.ExitUsage, "", true, true},
		{"messages-delete-unsupported.txt", []string{"messages", "delete", "spaces/AAAATestSpace/messages/BBBB", "--yes"}, output.ExitUnsupported, "", true, true},
		{"messages-download-no-arguments.txt", []string{"messages", "download"}, output.ExitUsage, "", true, true},
		{"messages-download-unsupported.txt", []string{"messages", "download", "spaces/AAAATestSpace/messages/BBBB"}, output.ExitUnsupported, "", true, true},
		{"send-file-unsupported.txt", []string{"send", "hello", "--file", "/etc/hosts"}, output.ExitUnsupported, "", true, true},

		// The confirmation is deliberately not recorded here. On the only
		// profile these goldens can configure, a webhook, the capability gate
		// fires before the question is asked, so a golden named for the
		// confirmation would record a refusal instead. profile-rm-unconfirmed
		// covers the prompt itself, and it can, because removing a profile is
		// local and has no capability to check first.

		{"react-no-arguments.txt", []string{"react"}, output.ExitUsage, "", true, true},
		{"react-unsupported.txt", []string{"react", "spaces/AAAATestSpace/messages/BBBB", "\U0001F44D"}, output.ExitUnsupported, "", true, true},

		// A shortcode is refused before the profile is loaded, so this one
		// records the message rather than the capability gate in front of it.
		{"react-shortcode.txt", []string{"react", "spaces/AAAATestSpace/messages/BBBB", ":thumbsup:"}, output.ExitUsage, "", true, true},

		{"tail-no-arguments.txt", []string{"tail"}, output.ExitUsage, "", true, true},
		{"tail-interval-below-floor.txt", []string{"tail", "spaces/AAAATestSpace", "--interval", "100ms"}, output.ExitUsage, "", true, true},
		// An absolute time rather than "1h", because the refusal echoes what the
		// window resolved to and a duration resolves against the clock. A golden
		// holding a value that changes every run does not announce itself as
		// unstable; it announces itself as the output contract having changed.
		{"tail-since-and-backfill.txt", []string{"tail", "spaces/AAAATestSpace", "--since", "2026-08-16T09:00:00Z", "--backfill", "5"}, output.ExitUsage, "", true, true},

		{"watch-no-arguments.txt", []string{"watch"}, output.ExitUsage, "", true, true},
		{"watch-bad-events.txt", []string{"watch", "spaces/AAAATestSpace", "--events", "everything"}, output.ExitUsage, "", true, true},
		{"watch-interval-below-floor.txt", []string{"watch", "spaces/AAAATestSpace", "--interval", "100ms"}, output.ExitUsage, "", true, true},
		{"watch-unsupported.txt", []string{"watch", "spaces/AAAATestSpace"}, output.ExitUnsupported, "", true, true},

		// --all is the same capability as watch, so a webhook meets the same
		// refusal, and the conflict between --all and a named space is refused
		// before a profile is even loaded.
		{"watch-all-unsupported.txt", []string{"watch", "--all"}, output.ExitUnsupported, "", true, true},
		{"watch-all-and-a-space.txt", []string{"watch", "--all", "spaces/AAAATestSpace"}, output.ExitUsage, "", true, true},

		// sync and search. sync is a read, so a webhook meets the capability
		// refusal; search touches no network at all, so what it refuses is an
		// empty index, which is the state every machine starts in and the first
		// thing a new user will see.
		{"sync-no-arguments.txt", []string{"sync"}, output.ExitUsage, "", true, true},
		{"sync-all-and-a-space.txt", []string{"sync", "--all", "spaces/AAAATestSpace"}, output.ExitUsage, "", true, true},
		{"sync-unsupported.txt", []string{"sync", "spaces/AAAATestSpace"}, output.ExitUnsupported, "", true, true},
		{"search-no-arguments.txt", []string{"search"}, output.ExitUsage, "", true, true},
		{"search-empty-index.txt", []string{"search", "deploy"}, output.ExitUsage, "", true, true},

		// The MCP server's refusals. Both are reached before a session starts,
		// so they are deterministic: one is a profile that can serve nothing,
		// and the other is an allowlist entry that is not a space name.
		{"mcp-nothing-to-serve.txt", []string{"mcp"}, output.ExitUnsupported, "", true, true},
		{"mcp-allow-space-not-a-name.txt", []string{"mcp", "--allow-write", "--allow-space", "eng-alerts"}, output.ExitUsage, "", true, true},
		{"tail-unsupported.txt", []string{"tail", "spaces/AAAATestSpace"}, output.ExitUnsupported, "", true, true},
		{"alias-list-empty.txt", []string{"alias", "list"}, output.ExitOK, "", true, true},

		// A malformed space name is deliberately not recorded here. On the only
		// profile these goldens can configure, a webhook, the capability gate
		// fires first and the output is byte-identical to the case above, so a
		// golden for it would freeze the wrong claim under a name that suggests
		// otherwise. chat.CheckSpaceName is held where it can actually be
		// observed, in TestAReadRefusesABadSpaceNameWithoutAskingTheAPI.
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
			stdin := tc.stdin
			if tc.serve {
				stdin = chatShapedServer(t)
			}
			if strings.HasPrefix(tc.name, "profile-set-webhook-") && strings.Contains(tc.name, "dry-run") {
				// Reads the URL from stdin like any other set-webhook, and
				// deliberately has no profile configured first: a dry run of
				// setup has to work before there is anything set up.
			} else if needsAProfile(tc.name) {
				// These need a profile that already exists. Configuring it is
				// setup rather than part of the recorded output.
				if setup := runCLIIn(t, stdin, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
					t.Fatalf("setup: exit %d\n%s", setup.exit, setup.stderr)
				}
				stdin = ""
			}
			if tc.name == "profile-rm-unconfirmed.txt" {
				// Needs something to remove. Not part of the recorded output.
				if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
					t.Fatalf("setup: exit %d\n%s", setup.exit, setup.stderr)
				}
			}

			got := runCLIIn(t, stdin, tc.args...)
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

// needsAProfile reports whether a golden case has to have one configured first.
//
// By name prefix rather than by inspecting the arguments, because the arguments
// are the thing being recorded and a helper that parsed them would be a second
// implementation of the command tree.
func needsAProfile(name string) bool {
	for _, prefix := range []string{"send", "spaces-", "messages-", "alias-", "tail-", "react-", "watch-", "mcp-", "sync-", "search-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
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

// chatShapedServer stands up something that answers a webhook POST the way the
// Chat API does, and returns a webhook URL for it.
//
// The response body is a copy of a real one. A fixture that does not match the
// wire tests the fixture.
func chatShapedServer(t *testing.T) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		// What a real webhook send returns, measured: name, space, text and
		// thread, and nothing else. No createTime, no sender, and no
		// formattedText. A fixture more generous than the wire produces a
		// golden showing fields a user never sees.
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB","space":{"name":"spaces/AAAATestSpace"},"thread":{"name":"spaces/AAAATestSpace/threads/BBB"}}`)
	}))
	t.Cleanup(server.Close)

	return server.URL + "/v1/spaces/AAAATestSpace/messages?key=" + testKey + "&token=" + testToken
}
