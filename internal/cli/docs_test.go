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
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/rows"
)

// docs/AGENTS.md and docs/SKILL.md are read by something that cannot tell a
// stale example from a current one.
//
// A person who pastes a broken command sees an error and adapts. An agent that
// was told the output shape has a `messages` array writes code against a
// `messages` array, gets an NDJSON stream, and fails somewhere else entirely,
// with a stack trace pointing at its own parser. So an example in these two
// files is closer to an interface definition than to documentation, and the
// only way it stays true is if a build fails when it stops being.
//
// Three things are checked here, in rising order of how much they catch.
// Every fenced JSON block parses. Every block that says which shape it is
// decodes into that shape with unknown fields refused. And every `spacebar`
// command line anywhere in either file names a command that exists, with flags
// that exist on it.
//
// What is deliberately not checked is that a documented read returns the
// documented row, because nothing in this package can produce one: the golden
// harness can only configure a webhook profile, and pointing an authorized one
// at a test server would mean an environment variable that redirects the API
// base, which is a lever for sending a credential somewhere else. The decode
// is what stands in for it, and it covers every shape rather than the seven
// that have goldens.
var agentDocs = []string{"docs/AGENTS.md", "docs/SKILL.md"}

// allDocs is every hand-written document that shows a command.
//
// Wider than agentDocs because the rule CLAUDE.md states is wider: a change
// that alters what an invocation does can falsify an example without touching
// the file it is in, and no generator catches that, because the example is
// prose that happens to be a command. The shape checks stay on the two files
// that publish output shapes; the command check covers everything a person or
// an agent might paste.
var allDocs = []string{
	"docs/AGENTS.md",
	"docs/SKILL.md",
	"docs/ADMIN.md",
	"README.md",
	"CONTRIBUTING.md",
	"SECURITY.md",
}

// shapes maps the name a document may claim to a fresh value of that type.
//
// Only the published row shapes are here, because only they are exported. A
// block whose shape lives behind an unexported type is held to a golden file
// instead, which is stricter anyway: a golden comparison is byte for byte.
var shapes = map[string]func() any{
	"rows.Space":   func() any { return &rows.Space{} },
	"rows.Member":  func() any { return &rows.Member{} },
	"rows.Message": func() any { return &rows.Message{} },
	"rows.Event":   func() any { return &rows.Event{} },
}

// block is one fenced code block with whatever the comment above it claimed.
type block struct {
	line     int
	language string
	body     string

	// shape is the type named by a `<!-- shape: X -->` comment, and golden the
	// file named by a `<!-- golden: NAME [stream] -->` one. Both may be empty.
	shape  string
	golden string
	stream string
}

var (
	shapeTag  = regexp.MustCompile(`^<!--\s*shape:\s*(\S+)\s*-->$`)
	goldenTag = regexp.MustCompile(`^<!--\s*golden:\s*(\S+)(?:\s+(\S+))?\s*-->$`)
)

// parseBlocks reads one markdown file into its fenced blocks.
//
// Hand-rolled rather than pulled in as a dependency, because the grammar it
// needs is three lines of it and this project counts its dependencies.
func parseBlocks(t *testing.T, source string) []block {
	t.Helper()

	var found []block
	var pending block

	lines := strings.Split(source, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])

		if match := shapeTag.FindStringSubmatch(line); match != nil {
			pending.shape = match[1]
			continue
		}
		if match := goldenTag.FindStringSubmatch(line); match != nil {
			pending.golden, pending.stream = match[1], match[2]
			continue
		}
		if !strings.HasPrefix(line, "```") {
			// A blank line between a tag and its fence is allowed; anything
			// else means the tag was not describing a block after all.
			if line != "" {
				pending = block{}
			}
			continue
		}

		language := strings.TrimPrefix(line, "```")
		var body strings.Builder
		start := i + 1
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "```"; i++ {
			body.WriteString(lines[i])
			body.WriteString("\n")
		}

		pending.line, pending.language, pending.body = start, language, body.String()
		found = append(found, pending)
		pending = block{}
	}
	return found
}

// TestEveryJSONExampleInTheAgentDocsIsRealJSON.
//
// The cheapest of the three and the one that catches a typo, a trailing comma,
// or a block somebody edited by hand and did not re-read.
func TestEveryJSONExampleInTheAgentDocsIsRealJSON(t *testing.T) {
	for _, name := range agentDocs {
		for _, b := range readBlocks(t, name) {
			if b.language != "json" {
				continue
			}
			var any any
			if err := json.Unmarshal([]byte(b.body), &any); err != nil {
				t.Errorf("%s:%d is fenced as json and does not parse: %v\n%s",
					name, b.line, err, b.body)
			}
		}
	}
}

// TestEveryDocumentedShapeIsTheShapeThisToolPublishes.
//
// This is the one the card was written for. Decoding with DisallowUnknownFields
// means a key in the document that the struct does not have is a failure, so a
// field that was renamed, dropped, or never existed cannot survive here. A
// wrong type fails the same way.
//
// It does not catch an omission, and it is not meant to: nearly every field is
// omitempty, so a shorter example is a real thing a caller will see rather than
// a documentation error.
func TestEveryDocumentedShapeIsTheShapeThisToolPublishes(t *testing.T) {
	seen := map[string]bool{}

	for _, name := range agentDocs {
		for _, b := range readBlocks(t, name) {
			if b.shape == "" {
				continue
			}
			seen[b.shape] = true

			build, known := shapes[b.shape]
			if !known {
				t.Errorf("%s:%d claims the shape %q, which this test cannot check.\n"+
					"Add it to shapes, or hold the block to a golden file instead.",
					name, b.line, b.shape)
				continue
			}

			decoder := json.NewDecoder(strings.NewReader(b.body))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(build()); err != nil {
				t.Errorf("%s:%d does not decode as %s: %v\n%s\n\n"+
					"An agent writes code against this. Fix the document, or the shape.",
					name, b.line, b.shape, err, b.body)
			}
		}
	}

	// A shape nothing documents is a gap in the documentation rather than in
	// the code, and it is worth failing on: these files exist so that every
	// published shape has a worked example, and a new one added to
	// internal/rows should arrive with the paragraph that describes it.
	for shape := range shapes {
		if !seen[shape] {
			t.Errorf("no example anywhere in the agent docs claims to be a %s.\n"+
				"Every published shape needs a worked example: that is what these files are for.", shape)
		}
	}
}

// TestEveryDocumentedShapeThatHasAGoldenMatchesIt.
//
// Where a recorded output contract exists, the document quotes it exactly. This
// is the card's claim taken literally, and it is stricter than the decode: it
// catches a value that is plausible and not what the tool actually writes, and
// it catches the indentation.
func TestEveryDocumentedShapeThatHasAGoldenMatchesIt(t *testing.T) {
	for _, name := range agentDocs {
		for _, b := range readBlocks(t, name) {
			if b.golden == "" {
				continue
			}

			path := filepath.Join("testdata", "golden", b.golden)
			recorded, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s:%d quotes %s, which does not exist: %v", name, b.line, path, err)
				continue
			}

			stream := b.stream
			if stream == "" {
				stream = "stdout"
			}
			want, ok := section(string(recorded), stream)
			if !ok {
				t.Errorf("%s:%d wants the %s of %s, which has no such section",
					name, b.line, stream, b.golden)
				continue
			}

			if strings.TrimSpace(b.body) != strings.TrimSpace(want) {
				t.Errorf("%s:%d does not match the %s recorded in %s.\n\n"+
					"--- the document\n%s\n--- the contract\n%s\n\n"+
					"The golden is the truth. Copy it in, do not adjust it.",
					name, b.line, stream, b.golden, b.body, want)
			}
		}
	}
}

// section lifts one stream out of a golden file, which records the exit code
// and then stdout and stderr under their own headings.
func section(golden, want string) (string, bool) {
	marker := "--- " + want
	start := strings.Index(golden, marker+"\n")
	if start < 0 {
		return "", false
	}
	rest := golden[start+len(marker)+1:]

	if end := strings.Index(rest, "\n--- "); end >= 0 {
		return rest[:end+1], true
	}
	return rest, true
}

// spacebarLine matches an invocation anywhere in a document, in a fenced block
// or in a sentence, with or without a shell prompt in front of it.
var spacebarLine = regexp.MustCompile(`(?m)^\s*(?:\$\s+)?spacebar\s+(.+)$`)

// TestEveryCommandShownInTheAgentDocsExists.
//
// CLAUDE.md's rule about worked examples, enforced rather than remembered. A
// behaviour change can falsify an example without touching the file it is in,
// and no generator catches that, because the example is prose that happens to
// be a command.
//
// What this walks is the real command tree, so a renamed command, a removed
// subcommand, or a flag that was never spelled the way the document spells it
// all fail here. It stops short of running the commands: most of them need an
// authorized profile and a network, and a test that talks to anybody is a rule
// this repository does not bend.
func TestEveryCommandShownInTheAgentDocsExists(t *testing.T) {
	for _, name := range allDocs {
		source := readSource(t, name)
		for _, match := range spacebarLine.FindAllStringSubmatch(source, -1) {
			invocation := strings.TrimSpace(match[1])

			// Everything after a pipe belongs to another program, and a comment
			// is prose. Both are cut before the invocation is read.
			if cut := strings.IndexAny(invocation, "|#<"); cut >= 0 {
				invocation = strings.TrimSpace(invocation[:cut])
			}
			if invocation == "" {
				continue
			}
			checkInvocation(t, name, invocation)
		}
	}
}

// checkInvocation resolves one command line against the real tree.
func checkInvocation(t *testing.T, doc, invocation string) {
	t.Helper()

	fields := strings.Fields(invocation)
	root := New(&Options{})

	// Cobra resolves a command from its arguments, ignoring anything that looks
	// like a flag, which is exactly the walk this needs.
	cmd, rest, err := root.Find(fields)
	if err != nil {
		t.Errorf("%s shows `spacebar %s`, and that command does not exist: %v", doc, invocation, err)
		return
	}

	// Find returns the deepest command that matched and everything it did not
	// consume, so a document naming a subcommand that never existed lands
	// quietly on the parent. `messages fetch` resolves to `messages` with
	// "fetch" left over, and reads as a valid invocation unless somebody looks
	// at the leftovers.
	//
	// A group is where that matters: it has subcommands and nothing to run
	// itself, so a leftover word can only have been meant as one of them. A
	// runnable command's leftovers are its arguments and are not this test's
	// business.
	if cmd.HasSubCommands() && !cmd.Runnable() {
		for _, leftover := range rest {
			if strings.HasPrefix(leftover, "-") {
				continue
			}
			t.Errorf("%s shows `spacebar %s`, and %q has no %q subcommand.",
				doc, invocation, cmd.CommandPath(), leftover)
			break
		}
	}

	cmd.InitDefaultHelpFlag()

	for _, field := range fields {
		if !strings.HasPrefix(field, "--") {
			continue
		}
		flag := strings.TrimPrefix(strings.SplitN(field, "=", 2)[0], "--")
		if flag == "" || lookup(cmd, flag) != nil {
			continue
		}
		t.Errorf("%s shows `spacebar %s`, and %q has no --%s flag.\n"+
			"An example that no longer works reads as the tool being broken, "+
			"by exactly the person who cannot tell the difference.",
			doc, invocation, cmd.CommandPath(), flag)
	}
}

// lookup finds a flag on a command or on anything it inherits from.
func lookup(cmd *cobra.Command, name string) *pflag.Flag {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		return flag
	}
	return cmd.InheritedFlags().Lookup(name)
}

// TestBothAgentDocumentsStateTheConfirmationRequirement.
//
// The card's third deliverable, and the one most easily lost in an edit. Both
// files are read by something deciding whether to post to a space full of
// people, and the sentence that stops it is the same sentence the MCP tool
// description carries.
//
// Asserted against the words rather than against a constant, for the reason
// TestEveryWriteToolSaysToConfirmFirst is: a test comparing a document to the
// constant it was generated from moves when the constant moves, and passes.
func TestBothAgentDocumentsStateTheConfirmationRequirement(t *testing.T) {
	for _, name := range agentDocs {
		source := readSource(t, name)
		if !strings.Contains(source, "Confirm") {
			t.Errorf("%s never tells its reader to confirm before writing", name)
		}
		if !strings.Contains(source, "cannot be unsent") {
			t.Errorf("%s does not say a message cannot be unsent, "+
				"which is the reason the confirmation exists", name)
		}
	}

	// Flattened, because prose wraps and a blockquote carries a marker on every
	// line. What is being asserted is the sentence, not where the lines break.
	skill := flatten(readSource(t, "docs/SKILL.md"))
	if !strings.Contains(skill, "This posts a visible message to a real Google Chat space. "+
		"Confirm with the user before calling.") {
		t.Error("docs/SKILL.md does not quote, word for word, the sentence every write tool's " +
			"description ends with. A paraphrase is a different promise.")
	}
}

// flatten reduces markdown prose to one line of single-spaced words, dropping
// the blockquote markers, so that a substring test is about the words.
func flatten(source string) string {
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(strings.TrimSpace(line), "> ")
	}
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}

func readSource(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join("..", "..", filepath.FromSlash(name))
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n")))
}

func readBlocks(t *testing.T, name string) []block {
	t.Helper()
	return parseBlocks(t, readSource(t, name))
}

// TestEveryCommandIsDocumentedSomewhere.
//
// The other direction from TestEveryCommandShownInTheAgentDocsExists, and the
// one that catches the opposite mistake: a command that was built and never
// written about. That is quieter than a broken example, because nothing fails
// and nobody looking at the documentation knows to ask.
//
// Found `version` and `completion` documented nowhere at all, two milestones
// after they shipped, and both are commands somebody would actually want to
// know exist. Shell completion in particular is the sort of thing a person
// never discovers by accident.
//
// A group that only prints help is exempt, because there is nothing to say
// about it that its children do not say better. `help` is cobra's own.
func TestEveryCommandIsDocumentedSomewhere(t *testing.T) {
	sources := map[string]string{}
	for _, name := range allDocs {
		sources[name] = readSource(t, name)
	}

	root := New(&Options{})

	// cobra adds `completion` lazily, at execute time rather than at
	// construction, so a walk over a freshly built root does not see it. That
	// is not a hypothetical: `completion` was one of the two commands this test
	// was written to catch, and the first version of it could not, which a
	// planted violation is what revealed.
	root.InitDefaultCompletionCmd()

	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			walk(child)
		}

		// A group with children and nothing to run is documented by them.
		if cmd.HasSubCommands() && !cmd.Runnable() {
			return
		}
		path := cmd.CommandPath()
		if path == "spacebar" || strings.HasPrefix(path, "spacebar help") {
			return
		}

		for _, source := range sources {
			if strings.Contains(source, path) {
				return
			}
		}
		t.Errorf("%q exists and no hand-written document mentions it.\n"+
			"A command nobody wrote about is one nobody finds. Add it to the README "+
			"at least, or to docs/AGENTS.md if an agent would use it.", path)
	}
	walk(root)
}

// envReference is the document that has to list every environment variable.
//
// One document rather than any of them, because the failure this gate exists
// for is not that a variable was never written down anywhere: SPACEBAR_CLIENT_SECRET
// was in SECURITY.md, in a sentence about where a credential may come from, and
// XDG_CACHE_HOME was in a paragraph about quota. Both were true and neither is
// findable by somebody who wants to know what this reads from the environment.
// So the README carries the list, and the gate is that the list is complete.
const envReference = "README.md"

// TestEveryEnvironmentVariableIsDocumented is the environment's version of
// TestEveryCommandIsDocumentedSomewhere, and it caught more.
//
// SPACEBAR_PROFILE was read from the first milestone and written down nowhere.
// Its entire public existence was the parenthesis at the end of --profile's
// help string, so the way to discover it was to already know. NO_COLOR, TERM
// and XDG_CONFIG_HOME were in no hand-written document at all, and the one
// document written for a program driving this tool mentioned no variable of
// any kind.
//
// The other half, that the list is what the binary actually reads, is
// TestEveryEnvironmentReadNamesAListedVariable in internal/lint.
func TestEveryEnvironmentVariableIsDocumented(t *testing.T) {
	if len(config.EnvVars) == 0 {
		t.Fatal("config.EnvVars is empty, so this gate would pass by having nothing to check")
	}

	reference := readSource(t, envReference)
	for _, v := range config.EnvVars {
		if strings.Contains(reference, v.Name) {
			continue
		}
		t.Errorf("%s does not mention %s, and it is read on every invocation that needs it.\n"+
			"A variable nobody can find is one nobody sets. Add it to the environment table, "+
			"with the line from config.EnvVars: %s", envReference, v.Name, v.Purpose)
	}
}

// TestEveryVariableTheDocumentsNameIsOneThisToolReads is the other direction,
// and it is the one that goes stale on its own.
//
// A renamed variable leaves the old name behind in prose that still reads
// correctly, and nothing about the new name failing to work says which document
// is lying. Only the ones this project owns are checked, because those are the
// ones it can rename: NO_COLOR and the XDG variables are named by somebody else
// and appear in these documents in sentences about other tools' behaviour.
func TestEveryVariableTheDocumentsNameIsOneThisToolReads(t *testing.T) {
	listed := map[string]bool{}
	for _, v := range config.EnvVars {
		listed[v.Name] = true
	}

	owned := regexp.MustCompile(regexp.QuoteMeta(config.Env("")) + `[A-Z0-9_]+`)
	for _, doc := range allDocs {
		source := readSource(t, doc)
		for _, name := range owned.FindAllString(source, -1) {
			if listed[name] {
				continue
			}
			t.Errorf("%s names the environment variable %s, and nothing reads it.\n"+
				"Either it was renamed and this document was not, or it never existed. "+
				"config.EnvVars is what is read.", doc, name)
		}
	}
}
