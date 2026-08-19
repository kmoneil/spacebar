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
	"path/filepath"
	"regexp"
	"strconv"
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

	// variants are further invocations of the same command that change the
	// requests it makes, run under --dry-run beside the one above.
	//
	// One entry today and it is the reason this field exists. `send --file`
	// uploads before it posts, so it is two requests rather than one, and the
	// first was the only place in the tree a *chat.DryRun could arrive
	// unhandled. Walking commands does not reach it, because it is a flag on a
	// command the walk already had: `send` was covered and `send --file` was a
	// different code path with the same name.
	//
	// A flag belongs here when it changes which requests are made, and not when
	// it changes what is in one. --md and --mention alter a body; --file adds a
	// request.
	variants []dryRunVariant

	// refusedOnAWebhook says this command needs a capability a webhook does not
	// have, so on that profile the gate fires before the dry run, which is
	// correct and is not what this test is about.
	//
	// It used to mean the dry run was never reached at all. The reasoning was
	// that these tests can configure exactly one kind of profile, because a
	// user-OAuth one would need a client pointed at a test server and
	// chat.BaseURL is a constant on purpose: an environment variable that
	// redirects the API base is a lever for sending a credential somewhere
	// else.
	//
	// The constant still holds and there is still no server. What was wrong is
	// the conclusion: a dry run never reaches the network, so it needs no
	// server to be pointed at. configuredUserOAuth builds the profile without
	// one, and every command here now has its dry run reached on the transport
	// that can carry it. That gap is how `send --file` shipped exiting 1 and
	// printing nothing for four milestones.
	refusedOnAWebhook bool

	// silent says this command prints no request preview, so the two
	// assertions about one do not apply. `mcp` is the only one: it is a server
	// whose stdout is the protocol, and with nobody on the other end of the
	// pipe it says nothing at all, which is correct rather than a gap.
	silent bool
}{
	"spacebar send": {
		args: []string{"send", "deploy done"},
		variants: []dryRunVariant{{
			// The one this card was written for. It is refused on a webhook,
			// which has no CanUpload, so it is only ever reached on the
			// user-OAuth profile, which is why nothing reached it at all.
			name:              "--file",
			args:              []string{"send", "spaces/AAAATestSpace", "deploy done", "--file", attachmentPlaceholder},
			refusedOnAWebhook: true,
		}},
	},

	// A verification message is a real message in a real space, so this is a
	// write even though the command is named for configuration.
	"spacebar profile set-webhook": {args: []string{"profile", "set-webhook", "alerts", "--verify"}, needsURL: true},

	// The three mutations. delete carries --yes because the confirmation comes
	// before the request and would otherwise be what stopped it, which would
	// make this test pass for the wrong reason: --dry-run has to be what makes
	// no request, not the prompt in front of it.
	"spacebar messages edit": {args: []string{
		"messages", "edit", "spaces/AAAATestSpace/messages/BBBB", "the new text",
	}, refusedOnAWebhook: true},
	"spacebar messages delete": {args: []string{
		"messages", "delete", "spaces/AAAATestSpace/messages/BBBB", "--yes",
	}, refusedOnAWebhook: true},
	"spacebar react": {args: []string{
		"react", "spaces/AAAATestSpace/messages/BBBB", "👍",
	}, refusedOnAWebhook: true},

	// A server rather than a command, and a writing one the moment
	// --allow-write registers send_message. It moved here from readOnlyCommands
	// on the day that flag landed, which is what this gate is for: the question
	// gets asked again every time the command tree changes.
	//
	// The peer says nothing, because the walk hands it an empty stdin, so the
	// session ends at once and no tool is ever called. What is asserted is the
	// same as for every other entry: with --dry-run set, nothing reached the far
	// end.
	"spacebar mcp": {args: []string{"mcp", "--allow-write"}, silent: true},
}

// readOnlyCommands cannot put anything into a space, so --dry-run has nothing
// to stop. Each is listed with the reason, because "this one is fine" is the
// sentence that stops being true without anybody noticing.
//
// Until m3-04 every entry here also reached no network at all, and the comment
// said so. The read commands broke that: `spaces list` makes a real request to
// a real API and is still read-only, because the criterion is whether a command
// can change something at the far end rather than whether it makes a request.
// The distinction is worth keeping straight, because a future command that
// reaches the network and does change something belongs in the other map even
// if it feels like an inspection.
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

	"spacebar alias": "the group prints help",

	// Reads from the API and writes to this machine's own index. It puts
	// nothing into a space, which is the question this list asks, and the fact
	// that it writes a great deal to disk is a different property: --dry-run
	// has no request of its own to show, because every request it makes is a
	// read somebody could have made with `messages list`.
	"spacebar sync": "copies messages down and writes only to the local index",

	// Touches no network at all. It is the one read command that works on a
	// webhook profile, because the answer is already on disk.
	"spacebar search": "reads the local index and makes no request",

	// Polls messages.list forever and puts nothing anywhere. It is the one
	// read-only command that does not terminate on its own, which is a
	// different property from this one and is why the entry says so: --dry-run
	// has nothing to stop, and Ctrl-C is how it ends.
	"spacebar tail": "polls for messages and writes nothing, ending on a signal rather than on its own",

	// Writes files on this machine and nothing in a space. The interesting
	// question about it is not --dry-run, which has no request to stop that
	// matters, but where the bytes land: the name comes from whoever posted the
	// message, and TestAServerSuppliedFilenameCannotLeaveTheDirectory is what
	// holds that.
	"spacebar messages download": "fetches bytes and writes them locally, changing nothing at the far end",

	// Polls spaceEvents forever and puts nothing anywhere. The same shape as
	// tail: --dry-run has nothing to stop and Ctrl-C is how it ends. What it
	// sees that tail cannot is edits, deletions and reactions, and seeing a
	// deletion is not making one.
	"spacebar watch": "polls for events and writes nothing, ending on a signal rather than on its own",

	// An alias is a line in the configuration file pointing at a space that is
	// already there, so nothing it does can be seen from inside a space. `set`
	// is the interesting one: it resolves its target, so it can list spaces
	// over the network, and listing is still a read. The criterion is whether a
	// command can change something at the far end, and naming a space locally
	// cannot.
	"spacebar alias set":  "writes one line of local configuration, after a read to resolve the target",
	"spacebar alias list": "reports what is configured, touching neither keyring nor network",
	"spacebar alias rm":   "deletes one line of local configuration",

	// The criterion is whether a command can put something into a space, and
	// none of these can. auth login does reach the network, and it has a rule of
	// its own about --dry-run for that reason: consenting is a side effect at
	// Google's end and on this machine, so a dry run of it opens no browser,
	// binds no listener, and stores nothing.
	// TestADryRunOfLoginConsentsToNothing holds that separately.
	"spacebar auth setup":  "prints instructions and stores a client locally, reaching no network at all",
	"spacebar auth login":  "authorizes this machine and puts nothing in a space",
	"spacebar auth status": "reads the stored token and deliberately not the network",
	"spacebar auth logout": "deletes a local token, and does not tell Google to forget anything",

	// The read commands. Every one of these does reach the network, and none of
	// them can change anything there: they are GETs, and the transport that
	// carries them refuses a write it has no capability for before the request
	// is built.
	"spacebar spaces":         "the group prints help",
	"spacebar spaces list":    "a GET; it reads what the account can already see",
	"spacebar spaces get":     "a GET for one space",
	"spacebar spaces members": "a GET for a space's memberships",
	"spacebar messages":       "the group prints help",
	"spacebar messages list":  "a GET; reading a space changes nothing in it",
	"spacebar messages get":   "a GET for one message",
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
		for _, v := range append([]dryRunVariant{{
			args:              cmd.args,
			refusedOnAWebhook: cmd.refusedOnAWebhook,
			silent:            cmd.silent,
			needsURL:          cmd.needsURL,
		}}, withDefaults(cmd.variants, cmd)...) {
			name := path
			if v.name != "" {
				name += " " + v.name
			}

			t.Run(name+" on a webhook", func(t *testing.T) {
				s := configuredRefusing(t)
				args := append([]string{"--dry-run"}, resolvePlaceholders(t, v.args)...)
				got := runCLIIn(t, s.stdinFor(v.needsURL), args...)

				// Either it printed the request it would have made, or it
				// refused before building one. Both are the claim this walk is
				// for: no write command reaches the network with --dry-run set,
				// whatever else it does.
				want := output.ExitOK
				if v.refusedOnAWebhook {
					want = output.ExitUnsupported
				}
				assertDryRun(t, got, want, v.silent || v.refusedOnAWebhook)

				// The request count is the assertion this half has and the
				// other half cannot: the webhook is pointed at a server that
				// fails the test if it is reached.
				if s.count() != 0 {
					t.Fatalf("--dry-run made %d requests", s.count())
				}
			})

			if !v.refusedOnAWebhook || v.needsURL {
				// Two reasons to stop here, and they are different.
				//
				// A command the webhook could carry has had its dry run reached
				// already, and a second transport would run the same code: the
				// stop is in the client either way.
				//
				// A command that reads a webhook URL from stdin builds its own
				// transport out of it and pays no attention to the active
				// profile, so running it here would test the webhook path
				// again while claiming to test the other one.
				continue
			}

			t.Run(name+" as an authorized user", func(t *testing.T) {
				configuredUserOAuth(t)
				args := append([]string{"--dry-run"}, resolvePlaceholders(t, v.args)...)
				got := runCLIIn(t, "", args...)

				// No request count here, and it is worth being plain about why.
				// There is no server to count at, because chat.BaseURL is a
				// constant. What holds "nothing was sent" on this half is that
				// nothing could be: an escaping request dials the unreachable
				// proxy configuredUserOAuth sets and fails there, so a regressed
				// dry-run stop shows up as a non-zero exit rather than as a
				// message in somebody's space. The stop itself is held in
				// internal/chat, against a server that fails if it is reached.
				assertDryRun(t, got, output.ExitOK, v.silent)
			})
		}
	}
}

// dryRunVariant is one invocation of a write command under --dry-run.
type dryRunVariant struct {
	// name distinguishes this from the command's plain form in the test output.
	// Empty for the plain form itself.
	name string

	args              []string
	needsURL          bool
	refusedOnAWebhook bool
	silent            bool
}

// attachmentPlaceholder stands in for a path only the test can know, because
// the file has to exist and live in a directory the test owns.
const attachmentPlaceholder = "<attachment>"

// resolvePlaceholders swaps in the paths that cannot be written in a table.
func resolvePlaceholders(t *testing.T, args []string) []string {
	t.Helper()

	out := make([]string, len(args))
	copy(out, args)
	for i, arg := range out {
		if arg != attachmentPlaceholder {
			continue
		}
		path := filepath.Join(t.TempDir(), "report.txt")
		if err := os.WriteFile(path, []byte("an attachment\n"), 0o600); err != nil {
			t.Fatalf("writing the attachment: %v", err)
		}
		out[i] = path
	}
	return out
}

// withDefaults fills a variant's unset fields from the command it belongs to,
// so a table entry only says what differs.
func withDefaults(variants []dryRunVariant, cmd struct {
	args              []string
	needsURL          bool
	variants          []dryRunVariant
	refusedOnAWebhook bool
	silent            bool
},
) []dryRunVariant {
	out := make([]dryRunVariant, 0, len(variants))
	for _, v := range variants {
		if !v.needsURL {
			v.needsURL = cmd.needsURL
		}
		if !v.silent {
			v.silent = cmd.silent
		}
		out = append(out, v)
	}
	return out
}

// assertDryRun holds what every dry run owes its caller: exit 0 and a request
// on stdout, or a refusal that reached no request at all.
func assertDryRun(t *testing.T, got result, want output.ExitCode, silent bool) {
	t.Helper()

	if got.exit != want {
		t.Fatalf("exit = %d, want %d\n%s", got.exit, want, got.stderr)
	}
	if want != output.ExitOK || silent {
		return
	}
	if got.stdout == "" {
		t.Errorf("--dry-run printed nothing to stdout, so there is nothing to check.\nstderr:\n%s", got.stderr)
	}
}

// TestADryRunOfASendWithAFileShowsTheUploadAndSaysWhatFollows holds the
// decision, which the walk above does not: it asserts that a dry run happened,
// and this asserts what it said.
//
// A send with an attachment is two requests. The second carries an upload token
// this API returns from the first, so there is no way to show it without making
// the first, and the choice was between printing one exact request and saying
// what follows it, or printing an approximation of a request that would not be
// sent in that form. This tool does not approximate.
func TestADryRunOfASendWithAFileShowsTheUploadAndSaysWhatFollows(t *testing.T) {
	configuredUserOAuth(t)

	// Recognisable bytes, so that "the file was not printed" is a claim about
	// this file rather than about the absence of something.
	const marker = "PRETEND-THIS-IS-A-PNG"
	path := filepath.Join(t.TempDir(), "diagram.png")
	if err := os.WriteFile(path, []byte(strings.Repeat(marker, 100)), 0o600); err != nil {
		t.Fatalf("writing the attachment: %v", err)
	}

	got := runCLIIn(t, "", "--dry-run", "send", "spaces/AAAATestSpace", "deploy done", "--file", path)
	if got.exit != output.ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", got.exit, got.stderr)
	}

	// The request that would actually be sent first, exactly.
	for _, want := range []string{
		"POST https://chat.googleapis.com/upload/v1/spaces/AAAATestSpace/attachments:upload",
		"uploadType=multipart",
		"Content-Type: multipart/related",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the upload request does not carry %q:\n%s", want, got.stdout)
		}
	}

	// And the file is described rather than printed. A two-hundred-megabyte
	// attachment is allowed, and printing one is not showing a request, it is
	// copying a file to stdout.
	if strings.Contains(got.stdout, marker) {
		t.Errorf("the attachment's bytes were printed to stdout:\n%.500s", got.stdout)
	}
	if !strings.Contains(got.stdout, "bytes, not shown") {
		t.Errorf("the body is not described:\n%s", got.stdout)
	}

	// The count is of the request body, which is the multipart document and so
	// is larger than the file inside it. Asserted as that relationship rather
	// than as a number, because a number here would be recording the length of
	// a boundary and the metadata part.
	size := regexp.MustCompile(`<(\d+) bytes, not shown`).FindStringSubmatch(got.stdout)
	if size == nil {
		t.Fatalf("the description carries no byte count:\n%s", got.stdout)
	}
	counted, err := strconv.Atoi(size[1])
	if err != nil {
		t.Fatalf("the byte count is not a number: %q", size[1])
	}
	if fileSize := len(marker) * 100; counted <= fileSize {
		t.Errorf("the body is %d bytes and the file alone is %d, so the count is not of the request",
			counted, fileSize)
	}

	// Saying so is the other half of the decision. A caller who sees one
	// request and is not told a second would follow has been told half of what
	// would happen.
	if !strings.Contains(got.stderr, "first of two requests") {
		t.Errorf("stderr does not say a second request would follow:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "Nothing was uploaded and nothing was sent") {
		t.Errorf("stderr does not say nothing happened:\n%s", got.stderr)
	}

	// stdout is the request and nothing else, which is the rule every other dry
	// run follows: the notes above are on stderr precisely so a caller piping
	// stdout into a parser is handed one document.
	if strings.Contains(got.stdout, "first of two requests") {
		t.Errorf("a note reached stdout:\n%s", got.stdout)
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
		if cmd.refusedOnAWebhook || cmd.silent {
			// Neither reaches a request, so neither renders one. What they do
			// print is held by the goldens, which record the whole of stderr.
			continue
		}
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
// a bearer token. See TestADryRunShowsTheHeaderAsRedactedRatherThanOmittingIt there.
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
