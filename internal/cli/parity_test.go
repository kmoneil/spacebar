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
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/mcpsrv"
	"github.com/kmoneil/spacebar/internal/profile"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/store"
)

// This file holds one claim: a job this tool can do from the command line can
// be done over MCP, or somebody has written down why not.
//
// internal/cli and internal/mcpsrv are both thin adapters over the same
// internal packages, so a difference between them is a bug rather than a
// feature of either. Nothing checked that. Four commands were absent from the
// tool list for every profile, including one that could run all four, while
// docs/SKILL.md told a model that a missing tool means the profile cannot do
// the thing and to say so to the person. That turns a gap into a confident
// false statement to somebody with no way to check it.
//
// Two checks rather than one, and the second is not redundant. Recon on the
// first draft of this gate found each one blind where the other sees:
//
//   - By command. `sync`, `messages download`, `tail` and `watch` all gate on
//     CanRead, which MCP already serves three tools behind, so a capability
//     check answers "served" for every one of them.
//   - By capability. `send --file` and `send --card` are flags on a command
//     that already has a tool, so walking commands never reaches them. A flag
//     earns its own entry when it changes which requests are made rather than
//     what is in one, which is the rule the dry-run walk in dryrun_test.go
//     already follows for the same flag.
//
// Neither check knows a command's capability from its own source: the
// transport.Require call is inside a RunE closure and its first argument is a
// human-facing label rather than a command path. So the tables here are written
// by hand and held complete by the walk, which is the shape writeCommands and
// readOnlyCommands already use: a new command fails the build until somebody
// decides which column it belongs in.

// servedByMCP is a command and the tool that does the same job.
var servedByMCP = map[string]string{
	"spacebar spaces list":    "list_spaces",
	"spacebar spaces get":     "get_space",
	"spacebar spaces members": "list_members",
	"spacebar messages list":  "list_messages",
	"spacebar messages get":   "get_message",
	"spacebar send":           "send_message",
	"spacebar react":          "react_to_message",
	"spacebar search":         "search_messages",
	"spacebar sync":           "sync_space",
}

// cliOnly is a command that deliberately has no tool, and why.
//
// The reason is the point of the entry. "There is no tool" is the state this
// gate exists to stop being silent about, so an entry that does not say why is
// worth no more than the silence it replaced.
var cliOnly = map[string]string{
	"spacebar": "the root prints help and refuses an unknown command",

	// Groups. They print help and do nothing else.
	"spacebar messages": "the group prints help",
	"spacebar spaces":   "the group prints help",
	"spacebar alias":    "the group prints help",
	"spacebar auth":     "the group prints help",
	"spacebar profile":  "the group prints help",

	"spacebar licenses": "prints an embedded file, which is not an operation on a space",

	// The MCP handshake carries the server's name and version, so a tool would
	// be a second answer to a question the protocol has already answered, and
	// two answers that can disagree.
	"spacebar version": "the MCP initialize handshake already carries the version",

	"spacebar mcp": "is this server; a tool for it would start a second one",

	// Authorization is an operator action at a terminal with a browser in it.
	// A model that could start one would be asking somebody to approve a
	// consent screen it chose, and a model that could end one would be taking
	// away access the person still wanted. Neither is what --allow-write is
	// asking about.
	"spacebar auth login":  "starts a browser consent flow, which is the operator's action and not a model's",
	"spacebar auth logout": "deletes a credential, which a model must not be able to do",
	"spacebar auth setup":  "files an OAuth client from a secret on stdin, at a terminal",
	"spacebar auth status": "reports this machine's stored authorization, not anything in a space",

	// Local configuration and credentials. `profile rm` is gated by a
	// confirmation, and a confirmation a model answers is not a confirmation.
	"spacebar profile list":        "reports what is configured on this machine",
	"spacebar profile set-webhook": "stores a credential read from stdin",
	"spacebar profile rm":          "deletes credentials behind a confirmation, which a model answering defeats",

	// Every tool that names a space already accepts an alias, so the part a
	// model needs is present. Choosing what an alias means is the operator
	// naming their own machine.
	"spacebar alias set":  "names a space on this machine; every tool already accepts an alias as input",
	"spacebar alias list": "reports this machine's own naming",
	"spacebar alias rm":   "removes this machine's own naming",

	// A tool result is one document. A follow has nothing to return until it
	// ends and does not end, so the shape does not fit the protocol rather than
	// the work being unavailable: list_messages with a since bound answers the
	// same question in the shape a tool call has.
	"spacebar tail":  "follows a space forever, and a tool result is one document; list_messages with since is the shape that fits",
	"spacebar watch": "follows a space's events forever, for the reason tail does",
}

// owed is a tool that should exist and does not.
type owed struct {
	// tool is the name it will have, so that this entry has to move rather
	// than be forgotten when it arrives.
	tool string

	// capability is what the command gates on, which is how this table and the
	// capability check below stay one record rather than two.
	//
	// Often a capability MCP already serves, because the gap is the command
	// rather than the capability: `sync` and `messages download` both gate on
	// CanRead, which three tools are registered behind. Naming it anyway is
	// what lets the capability check treat CanEdit and CanDelete as recorded
	// here instead of repeating them, and a name that no command gates on
	// fails, so a typo does not read as coverage.
	capability string

	// why says what has to happen first, or that nothing does.
	why string
}

// owedTools is the gap this gate was written for, recorded rather than
// accepted.
//
// These were absent because nobody built them, not because a profile cannot do
// them: a user-OAuth profile runs all four from the command line today. They
// are here rather than in cliOnly so that the difference between "decided" and
// "not done" survives, which is the same distinction SECURITY.md's (Mn) markers
// keep and for the same reason. TestNoOwedToolHasQuietlyArrived is what stops
// an entry outliving the gap.
var owedTools = map[string]owed{
	"spacebar messages edit": {
		tool:       "edit_message",
		capability: "CanEdit",
		why:        "nothing blocks it; internal/transport already exposes EditMessage",
	},
	"spacebar messages delete": {
		tool:       "delete_message",
		capability: "CanDelete",
		why:        "nothing blocks it, but it destroys a message, so it belongs behind --allow-write with the confirmation sentence",
	},
	"spacebar messages download": {
		tool:       "download_attachment",
		capability: "CanRead",
		why: "writes a file to this machine, so it needs a decision about where a tool may write " +
			"before it needs code; the CLI answers that with --out and a model has no cwd to mean",
	},
}

// maximalServer is a server with every tool it could ever register.
//
// A user-OAuth profile with --allow-write and an index holding something, which
// is the only shape that reaches all of them. Read off a built server rather
// than off a list written here, so that this file cannot disagree with what the
// package actually registers.
func maximalServer(t *testing.T) *mcpsrv.Server {
	t.Helper()

	dir := t.TempDir()
	index := store.NewNDJSON(dir)
	if err := index.Append(context.Background(), "spaces/AAAATestSpace", []rows.Message{
		{Name: "spaces/AAAATestSpace/messages/AAA", CreateTime: "2026-08-17T09:00:00Z", Text: "deploy done"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	server, err := mcpsrv.New(mcpsrv.Options{
		Profile:    &profile.Open{Name: "work", Transport: roaming{}},
		AllowWrite: true,
		Index:      index,
	})
	if err != nil {
		t.Fatalf("building a server with every capability: %v", err)
	}
	return server
}

// TestEveryCommandIsServedOverMCPOrSaysWhyNot.
//
// The completeness half, and the half that stops the drift. A command added
// tomorrow is in none of the three tables and fails here until somebody decides
// whether a model should be able to do it.
func TestEveryCommandIsServedOverMCPOrSaysWhyNot(t *testing.T) {
	walkCommands(New(&Options{}), func(cmd *cobra.Command) {
		path := cmd.CommandPath()
		_, served := servedByMCP[path]
		_, refused := cliOnly[path]
		_, owing := owedTools[path]

		switch {
		case served && refused, served && owing, refused && owing:
			t.Errorf("%s is in more than one of servedByMCP, cliOnly and owedTools", path)
		case !served && !refused && !owing:
			t.Errorf("%s is in none of servedByMCP, cliOnly or owedTools.\n"+
				"Decide whether a model should be able to do this. If a tool serves it, add it to "+
				"servedByMCP naming the tool. If one deliberately never will, add it to cliOnly with "+
				"the reason. If it should have one and nobody has written it, add it to owedTools with "+
				"the name it will have.\n"+
				"internal/cli and internal/mcpsrv are adapters over the same packages, so a job only "+
				"one of them can do is a bug rather than a feature of either.", path)
		}
	})
}

// TestEveryToolIsClaimedByExactlyOneCommand.
//
// The other direction. A tool named in servedByMCP that is not registered is a
// claim this file is making on its own, and a tool the server registers that no
// command claims is the same gap running the other way: something a model can
// do that a person cannot, which is worth noticing for its own reasons.
func TestEveryToolIsClaimedByExactlyOneCommand(t *testing.T) {
	registered := maximalServer(t).Tools()

	claimed := map[string][]string{}
	for path, tool := range servedByMCP {
		claimed[tool] = append(claimed[tool], path)
	}

	for tool, paths := range claimed {
		sort.Strings(paths)
		if len(paths) > 1 {
			t.Errorf("%s is claimed by %v; one tool serves one command", tool, paths)
		}
		if !slices.Contains(registered, tool) {
			t.Errorf("servedByMCP names %s for %v, and no profile registers a tool by that name.\n"+
				"Registered: %v", tool, paths, registered)
		}
	}

	for _, tool := range registered {
		if _, ok := claimed[tool]; !ok {
			t.Errorf("the server registers %s and no command in servedByMCP claims it.\n"+
				"Either a command serves the same job and belongs in the table, or a model can do "+
				"something nobody can do from a terminal.", tool)
		}
	}
}

// TestNoOwedToolHasQuietlyArrived.
//
// An entry that outlives its gap is a claim nobody implemented, which is the
// failure SECURITY.md's markers are kept honest against. When the tool lands
// its command moves to servedByMCP, and this is what makes moving it the only
// way to get a green build.
func TestNoOwedToolHasQuietlyArrived(t *testing.T) {
	registered := maximalServer(t).Tools()

	for path, o := range owedTools {
		if o.tool == "" || o.capability == "" || o.why == "" {
			t.Errorf("the owedTools entry for %s does not name the tool, the capability and what it waits on", path)
		}
		if slices.Contains(registered, o.tool) {
			t.Errorf("%s is registered, so %s is no longer owed.\n"+
				"Move it from owedTools to servedByMCP.", o.tool, path)
		}
	}
}

// requireCall finds a capability gate in this package's source.
//
// The label is captured and thrown away deliberately. It reads as a command
// name and is not one: `spaces list` and `tail` and `send --file` are all in
// there, so joining on it would make an error message's wording load bearing.
// The capability is the part with a type.
var requireCall = regexp.MustCompile(`transport\.Require\([^,]+,\s*"[^"]*",\s*transport\.(Can\w+)\)`)

// registerCall finds a tool's capability gate in internal/mcpsrv.
var registerCall = regexp.MustCompile(`register\(s, caps, transport\.(Can\w+),`)

// TestEveryCapabilityTheCLIGatesOnIsServedOverMCPOrSaysWhyNot.
//
// The check the command walk cannot make. A flag that changes which requests
// are made carries its own transport.Require, and walking commands never
// reaches one: `send --file` and `send --card` are both flags on a command that
// already has a tool.
//
// Read out of the source rather than by calling anything, because a
// transport.Require lives inside a RunE closure and there is nothing to ask at
// run time. internal/lint reads source for the same reason.
//
// Scoped to internal/cli. internal/resolve calls transport.Require too, for
// CanResolveDM and CanListSpaces, and both adapters go through it, so those are
// not a difference between them.
func TestEveryCapabilityTheCLIGatesOnIsServedOverMCPOrSaysWhyNot(t *testing.T) {
	// owedCapabilities is the flag-level half of owedTools, keyed by the
	// capability because that is what this check can see. A capability some
	// owedTools entry already names is not repeated here; the two tables are
	// one record read two ways.
	owedCapabilities := map[string]string{
		"CanUpload": "send --file uploads an attachment, and send_message has no attachment field",
		"CanSendCards": "send --card posts a card, and send_message has no card field. " +
			"This is the webhook population's gap: a card needs app authentication, so a webhook " +
			"can post one and a user-OAuth profile cannot, which makes it the one capability the " +
			"write-only transport has and the full one does not",
	}

	cliFiles, err := filepath.Glob(filepath.Join("..", "..", "internal", "cli", "*.go"))
	if err != nil {
		t.Fatalf("listing this package's source: %v", err)
	}
	if len(cliFiles) == 0 {
		t.Fatal("found no source in internal/cli, so this gate did not run")
	}

	gated := map[string]bool{}
	for _, path := range cliFiles {
		for _, m := range requireCall.FindAllStringSubmatch(readSource(t, toRepoPath(path)), -1) {
			gated[m[1]] = true
		}
	}
	if len(gated) == 0 {
		t.Fatal("found no transport.Require call in internal/cli, so this gate did not run")
	}

	served := map[string]bool{}
	for _, m := range registerCall.FindAllStringSubmatch(readSource(t, "internal/mcpsrv/mcpsrv.go"), -1) {
		served[m[1]] = true
	}
	if len(served) == 0 {
		t.Fatal("found no register call in internal/mcpsrv, so this gate did not run")
	}

	// A capability an owedTools entry already names is recorded, and recording
	// it once is the point: the command table and this one see different gaps
	// and must not become two places to update for one change.
	recorded := map[string]bool{}
	for _, o := range owedTools {
		recorded[o.capability] = true
	}

	for capability := range gated {
		switch {
		case served[capability]:
		case recorded[capability]:
		case owedCapabilities[capability] != "":
		default:
			t.Errorf("internal/cli gates a command on transport.%s and no MCP tool does.\n"+
				"Either register a tool behind it, or record it: in owedTools if a whole command is "+
				"missing, in owedCapabilities if it is a flag on a command that already has a tool. "+
				"A capability a person can reach and a model cannot is a difference between two "+
				"adapters over the same packages.", capability)
		}
	}

	// A capability named in owedTools has to be one a command really gates on,
	// or the entry is a typo reading as coverage.
	for path, o := range owedTools {
		if !gated[o.capability] {
			t.Errorf("owedTools says %s gates on transport.%s, and no transport.Require in "+
				"internal/cli names that capability.", path, o.capability)
		}
	}

	// And a marker cannot outlive its gap, for the reason owedTools' cannot.
	for capability := range owedCapabilities {
		switch {
		case served[capability]:
			t.Errorf("a tool now gates on transport.%s, so it is no longer owed.\n"+
				"Remove it from owedCapabilities.", capability)
		case recorded[capability]:
			t.Errorf("transport.%s is named by an owedTools entry as well, which is two records "+
				"of one gap.\nKeep the owedTools one and remove this.", capability)
		case !gated[capability]:
			t.Errorf("owedCapabilities names transport.%s and no command in internal/cli gates on it.\n"+
				"Remove it: there is no difference left to record.", capability)
		}
	}
}

// toRepoPath turns a path relative to this package back into one relative to
// the repository root, which is what readSource takes.
func toRepoPath(path string) string {
	return filepath.ToSlash(filepath.Clean(filepath.Join("internal", "cli", filepath.Base(path))))
}
