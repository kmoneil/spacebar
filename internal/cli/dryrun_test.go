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
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/output"
)

// Every runnable command in the tree is in exactly one of these two maps, and
// the walk below fails when one is in neither. That is the whole mechanism: a
// command added in Milestone 3 or 4 cannot be merged without somebody deciding,
// in writing, whether it can put something into a space.
//
// The alternative, a test that names the write commands one at a time, passes
// forever after the day somebody forgets to add one to it.

// writeCommands can cause a request that changes something at the far end, and
// each one is run with --dry-run against a server that fails the test if it is
// ever reached.
//
// needsURL marks a command that reads the webhook URL from stdin, which the
// walk supplies because only it knows the address of the server standing in for
// Chat.
var writeCommands = map[string]struct {
	args     []string
	needsURL bool
}{
	"spacebar send": {args: []string{"send", "deploy done"}},

	// A verification message is a real message in a real space, so this is a
	// write even though the command is named for configuration.
	"spacebar profile set-webhook": {args: []string{"profile", "set-webhook", "alerts", "--verify"}, needsURL: true},
}

// readOnlyCommands reach no network at all, so --dry-run has nothing to show
// and nothing to stop. Each is listed with the reason, because "this one is
// fine" is the sentence that stops being true without anybody noticing.
var readOnlyCommands = map[string]string{
	"spacebar":            "the root prints help",
	"spacebar version":    "reports the binary",
	"spacebar licenses":   "reads an embedded file",
	"spacebar completion": "generates a shell script",
	"spacebar profile":    "the group prints help",

	// Reads the configuration file and deliberately not the keyring, so that it
	// answers on a machine whose keyring is locked.
	"spacebar profile list": "reports what is configured, touching neither keyring nor network",

	// Removes a profile and its credential, both local. Nothing leaves the
	// machine, so there is no request for a dry run to show. It has a
	// confirmation instead, which is the gate that matters for it.
	"spacebar profile rm": "deletes local state only, and is gated by a confirmation",

	// Cobra's own, and not ours to classify.
	"spacebar help": "cobra's built-in",

	"spacebar auth": "the group prints help",

	// The criterion is whether a command can put something into a space, and
	// none of these can. auth login does reach the network, and it has a rule of
	// its own about --dry-run for that reason: consenting is a side effect at
	// Google's end and on this machine, so a dry run of it opens no browser,
	// binds no listener, and stores nothing.
	// TestADryRunOfLoginConsentsToNothing holds that separately.
	"spacebar auth login":  "authorizes this machine and puts nothing in a space",
	"spacebar auth status": "reads the stored token and deliberately not the network",
	"spacebar auth logout": "deletes a local token, and does not tell Google to forget anything",
}

// TestEveryCommandIsClassifiedAsWritingOrNot is the forcing function.
//
// It walks the tree rather than naming commands, so that the day somebody adds
// `spacebar react` the test fails and asks which kind it is. A list maintained
// by hand answers that question once, on the day it was written.
func TestEveryCommandIsClassifiedAsWritingOrNot(t *testing.T) {
	walkCommands(New(&Options{}), func(cmd *cobra.Command) {
		path := cmd.CommandPath()
		_, writes := writeCommands[path]
		_, readOnly := readOnlyCommands[path]

		switch {
		case writes && readOnly:
			t.Errorf("%s is listed as both writing and read-only", path)
		case !writes && !readOnly:
			t.Errorf("%s is in neither writeCommands nor readOnlyCommands.\n"+
				"Decide whether it can put something into a space. If it can, add it to writeCommands "+
				"with arguments that would send, and --dry-run will be held to making no request. If it "+
				"cannot, add it to readOnlyCommands with the reason.", path)
		}
	})
}

// TestEveryWriteCommandHonoursDryRun is the claim the card asks for, run
// against a server that fails the test rather than against a returned error.
//
// The distinction is the point. A --dry-run that printed the right thing and
// posted anyway would look identical from the outside, and the difference is a
// message somebody's colleagues can see.
func TestEveryWriteCommandHonoursDryRun(t *testing.T) {
	for path, cmd := range writeCommands {
		t.Run(path, func(t *testing.T) {
			s := configuredRefusing(t)

			got := runCLIIn(t, s.stdinFor(cmd.needsURL), append([]string{"--dry-run"}, cmd.args...)...)
			if got.exit != output.ExitOK {
				t.Fatalf("exit = %d, want 0\n%s", got.exit, got.stderr)
			}
			if s.count() != 0 {
				t.Fatalf("--dry-run made %d requests", s.count())
			}
			if got.stdout == "" {
				t.Errorf("--dry-run printed nothing to stdout, so there is nothing to check")
			}
		})
	}
}

// TestNoDryRunOutputAnywhereCarriesACredential.
//
// The regex is the card's, corrected for the same reason the verbose one was:
// grepping for "key=" matches the redacted form too, so it would pass on output
// that leaked. What is asserted is that the values never appear and that the
// placeholders do, in both renderings and on both streams.
func TestNoDryRunOutputAnywhereCarriesACredential(t *testing.T) {
	leak := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(testKey) + `|` + regexp.QuoteMeta(testToken) + `|Bearer\s+\S`)

	for path, cmd := range writeCommands {
		for _, mode := range [][]string{{"--dry-run"}, {"--dry-run", "--json"}, {"--dry-run", "--verbose"}} {
			t.Run(path+strings.Join(mode, " "), func(t *testing.T) {
				s := configuredRefusing(t)

				got := runCLIIn(t, s.stdinFor(cmd.needsURL), append(append([]string{}, mode...), cmd.args...)...)
				if got.exit != output.ExitOK {
					t.Fatalf("exit = %d\n%s", got.exit, got.stderr)
				}

				both := got.stdout + "\n" + got.stderr
				if leak.MatchString(both) {
					t.Errorf("a dry run carried a credential:\n%s", both)
				}
				if !strings.Contains(both, "key=REDACTED") || !strings.Contains(both, "token=REDACTED") {
					t.Errorf("nothing was redacted, so this test would pass on a build that printed nothing:\n%s", both)
				}
			})
		}
	}
}

// TestTheAuthorizationHeaderPrintsAsRedactedRatherThanBeingOmitted.
//
// A missing line reads as "no header was sent", which is a different and wrong
// answer to the question somebody is using --dry-run to ask. There is no
// Authorization header on a webhook, whose credential is in the URL, so this is
// asserted where the header exists: in internal/chat, against a client carrying
// a bearer token. See TestADryRunShowsTheHeaderAsRedacted there.
//
// This one holds the other half, that a webhook dry run does not invent a
// header it never sends.
func TestTheAuthorizationHeaderPrintsAsRedactedRatherThanBeingOmitted(t *testing.T) {
	configuredRefusing(t)

	got := runCLIIn(t, "", "--dry-run", "send", "deploy done")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if strings.Contains(got.stdout, "Authorization") {
		t.Errorf("a webhook dry run shows an Authorization header it does not send:\n%s", got.stdout)
	}
}

// configuredRefusing is a configured profile pointed at a server that fails the
// test if anything reaches it.
func configuredRefusing(t *testing.T) *space {
	t.Helper()

	s := configured(t)
	s.refuse(t)
	return s
}

// TestADryRunOfSetupStoresNothing.
//
// --dry-run on a command that writes to disk has to mean the whole command. The
// other reading, where it stores the credential and only declines to send,
// would be a --dry-run that wrote, and somebody who typed it to find out what
// would happen would have had half of it happen.
func TestADryRunOfSetupStoresNothing(t *testing.T) {
	path := isolate(t)

	url := "https://chat.googleapis.com/v1/spaces/AAAATestSpace/messages?key=" + testKey + "&token=" + testToken
	got := runCLIIn(t, url+"\n", "--dry-run", "profile", "set-webhook", "alerts")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stdout, "nothing was stored") {
		t.Errorf("the dry run does not say it stored nothing:\n%s", got.stdout)
	}

	if _, err := os.Stat(path); err == nil {
		t.Error("a dry run wrote a configuration file")
	}
	if listed := runCLIIn(t, "", "profile", "list"); listed.stdout != "" {
		t.Errorf("a dry run created a profile: %s", listed.stdout)
	}

	// A URL that would be refused is still refused, because a dry run that
	// accepted one is a dry run that answered the wrong question.
	bad := runCLIIn(t, "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=only\n",
		"--dry-run", "profile", "set-webhook", "alerts")
	if bad.exit != output.ExitUsage {
		t.Errorf("a truncated URL passed a dry run: exit %d", bad.exit)
	}
}
