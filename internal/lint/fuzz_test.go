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
	"bufio"
	"go/ast"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gate .github/workflows/fuzz-nightly.yml names, twice, as the one that
// holds the fuzz surface.
//
// The nightly sweep discovers what to fuzz by asking `go test -list`, which is
// the right way to run it and the wrong way to guard it: a workflow that greps
// for what exists cannot tell "never had one" from "somebody deleted it". It
// would go green on the day a target was removed, with a shorter matrix and no
// message, and that is a defence disappearing quietly, which is the failure
// every gate in this package exists to prevent.
//
// It was owed from Milestone 2 and written in Milestone 6, and the workflow
// described it in the present tense the whole time. internal/lint/doc.go says
// what that costs: a comment describing a gate nobody wrote is worse than no
// comment, because it stops the next person looking.

// requiredFuzzTargets is every fuzz target this repository must carry, by the
// directory it lives in, with the property each one states.
//
// A list rather than a discovery, and the reason is the whole point of the
// file: discovery answers "what is there", and what has to be held is "what
// must be there". The two agree until somebody deletes something.
//
// It is checked in both directions. A listed target that is gone fails, and a
// target that exists and is not listed fails too, so the list cannot rot into a
// subset of the truth. Adding a target is therefore two edits, and the second
// one is where somebody writes down what the first one is for.
//
// The reason strings are not decoration. Each names the invariant the target
// holds, so that somebody about to delete one can read what they would be
// switching off without going and finding it.
var requiredFuzzTargets = map[string]map[string]string{
	"internal/auth": {
		"FuzzARedactedURLCarriesNoCredential": "a credential never leaves this process unredacted, " +
			"stated over any URL rather than over the handful in the table beside it. It is the target " +
			"that found the semicolon and fragment defects.",
		"FuzzAClientFileThatIsAcceptedIsComplete": "the OAuth client file is a JSON document somebody " +
			"downloaded from somewhere else, and a half-parsed client fails at the consent screen rather " +
			"than at the paste that caused it.",
	},
	"internal/auth/loopback": {
		"FuzzACallbackOnlyCompletesOnAnExactStateMatch": "the state check is the whole defence against " +
			"a code injected by another page in the same browser, and it is the kind of check that " +
			"survives being written and then quietly stops applying.",
	},
	"internal/chat": {
		"FuzzAPathStaysOnTheBase": "a request path is relative, always, and is joined onto the base " +
			"rather than substituted for it.",
		"FuzzAnAcceptedWebhookURLStillSendsTheCredentialItCarried": "the credential somebody pasted is " +
			"the one that gets sent and the one that gets redacted. It spans two packages that check " +
			"their own half and neither of which knows what the other did.",
		"FuzzASpaceNameThatIsAcceptedIsSafeUnescaped": "a space name that passes the check produces a " +
			"request path byte-identical to itself, so escaping is the second layer and never the only one.",
		"FuzzAMessageNameThatIsAcceptedIsSafeUnescaped": "the same property for the pattern that admits " +
			"a dot, which is the one with more ways to be wrong.",
		"FuzzAMediaNameThatIsAcceptedIsSafeUnescaped": "and for the third pattern, whose value is the " +
			"only one of the three that nobody chose: it is whatever the API put in attachmentDataRef.",
		"FuzzTheSpaceOfAMessageIsAlwaysASpaceName": "--allow-space has to constrain a reaction, which " +
			"names a message rather than a space, so the space read out of a message name is checked again.",
		"FuzzRetryAfterIsAlwaysSaneOrIgnored": "a Retry-After is a number chosen by the far end, and a " +
			"far end that wants this process to stop for a week should not get it.",
	},
	"internal/cli": {
		"FuzzASafeFilenameIsAlwaysOneNameInTheDirectory": "an attachment's contentName is chosen by " +
			"whoever posted the message and cannot be allowed to leave the directory that was named.",
	},
	"internal/config": {
		"FuzzAConfigThatLoadsHoldsNoSecret": "a credential never reaches config.json, over any file body, " +
			"and by reflection over the struct so that a _ref field added later is covered the day it is added.",
	},
	"internal/format": {
		"FuzzTranslate": "Chat markup is generated and never concatenated, and valid UTF-8 in has to mean " +
			"valid UTF-8 out.",
		"FuzzCardsAreCheckedWithoutBeingRewritten": "a card is carried raw on purpose, so a card that is " +
			"accepted has to reach the wire as the one that was written.",
	},
	"internal/output": {
		"FuzzSanitize": "a data column never carries an escape sequence, and a cell can forge neither a " +
			"column nor a row.",
	},
	"internal/resolve": {
		"FuzzACachePathStaysUnderItsRoot": "a path this tool derives stays under its root.",
		"FuzzWhateverItReturnsIsASpaceName": "an alias, a fuzzy match and a DM lookup all produce a space " +
			"name from something else, and the something else may have come from the API.",
	},
	"internal/rows": {
		"FuzzAnIndexedMessageCarriesNoCredential": "the local index is a plaintext copy of message content " +
			"on the operator's disk, and downloadUri and thumbnailUri are credentials wearing the costume " +
			"of a link. What holds it today is an absence, which is the kind of defence somebody undoes " +
			"while thinking about something else.",
	},
	"internal/store": {
		"FuzzAnIndexPathStaysUnderItsRoot": "the same guarantee on the index's own directory, because this " +
			"package derives a path the same way the resolver does.",
		"FuzzARecordOnlyAnswersForItsOwnSpace": "a line off the local disk is the one input that is " +
			"neither the API's nor the operator's, and a foreign record moves where sync resumes.",
	},
}

// TestEveryFuzzTargetThisRepositoryClaimsExists holds the list above against
// the source, both ways.
func TestEveryFuzzTargetThisRepositoryClaimsExists(t *testing.T) {
	found := fuzzTargets(t)

	for dir, targets := range requiredFuzzTargets {
		for name, why := range targets {
			if _, ok := found[dir][name]; !ok {
				t.Errorf("%s no longer has %s, which held: %s\n"+
					"A fuzz target that is deleted takes its property with it and nothing else notices, "+
					"because the nightly sweep discovers what to run. Restore it, or remove the entry "+
					"here in the same change and say why the property stopped mattering.", dir, name, why)
			}
		}
	}

	for dir, targets := range found {
		for name := range targets {
			if _, ok := requiredFuzzTargets[dir][name]; !ok {
				t.Errorf("%s has a fuzz target %s that requiredFuzzTargets does not name.\n"+
					"Add it, with the property it holds. The list is checked both ways so that it cannot "+
					"rot into a subset of what is there, which is how it would stop being worth reading.",
					dir, name)
			}
		}
	}
}

// TestEveryFuzzTargetCarriesASeed is the half that decides whether any of this
// runs on a pull request.
//
// The nightly sweep is scheduled and does not block a merge, deliberately: a
// find is usually about code the last change never touched, and a
// nondeterministic red on somebody's unrelated merge teaches people to press
// re-run. What blocks a merge is `go test`, which runs each target against its
// seed corpus and every crasher ever committed, deterministically and with no
// fuzzing budget at all.
//
// So a target with no seeds is a target that executes nothing on the gate. It
// would still be swept overnight, from a corpus that persists in a cache, and
// the property it states would be checked by nothing at all on the day somebody
// broke it.
func TestEveryFuzzTargetCarriesASeed(t *testing.T) {
	for dir, targets := range fuzzTargets(t) {
		for name, seeds := range targets {
			if seeds == 0 {
				t.Errorf("%s.%s calls f.Add nowhere, so `go test` runs it against nothing and the "+
					"property it states is checked only by the nightly sweep.\n"+
					"Seed it with the shapes the input really takes, and with the ones that have gone "+
					"wrong before.", dir, name)
			}
		}
	}
}

// TestEveryCommittedCrasherStillBelongsToATarget is the regression half.
//
// An input that Go wrote under testdata/fuzz is a bug that happened. `go test`
// replays every one of them on every run, which is what stops a fixed bug from
// coming back, and it does so by matching the directory name against a
// function name. Rename or delete the target and the corpus directory is
// orphaned: it is still committed, still looks like coverage in a diff, and is
// executed by nothing.
//
// That is a silent loss of exactly the regressions worth keeping, so it is
// checked rather than trusted. There were none when this was written and there
// are four now, all found by targets added in the same change and all of them
// the test being wrong rather than the code. Each is committed twice, as the
// corpus file that replays it and as an f.Add seed that reads in the source,
// which is what fuzz-nightly.yml tells whoever opens the issue to do.
func TestEveryCommittedCrasherStillBelongsToATarget(t *testing.T) {
	found := fuzzTargets(t)
	root := repoRoot(t)

	for _, dir := range corpusDirs(t, root) {
		// <pkg>/testdata/fuzz/<Target>, so the package is three up and not
		// two. Written as two first, which named the testdata directory as the
		// package and reported every corpus as orphaned including the ones that
		// were not: a gate that fails on everything is as useless as one that
		// fails on nothing, and it was found by planting a corpus that should
		// have been accepted rather than only one that should not.
		pkg := filepath.ToSlash(filepath.Dir(filepath.Dir(filepath.Dir(dir))))
		target := filepath.Base(dir)
		if _, ok := found[pkg][target]; !ok {
			t.Errorf("%s holds a corpus for %s, and %s has no such fuzz target.\n"+
				"Those inputs are regressions that ran once and are executed by nothing now. Either "+
				"the target was renamed, in which case rename the directory with it, or it was deleted, "+
				"in which case delete the corpus deliberately rather than leaving it to read as coverage.",
				filepath.Join(pkg, "testdata", "fuzz", target), target, pkg)
		}
	}
}

// TestEveryCommittedCrasherIsAWellFormedCorpusFile.
//
// Go writes these files and a person commits them, which is one hand-copy from
// a CI artifact into a repository. A file that is not in the corpus format is
// skipped by the fuzzing engine rather than refused, so a crasher pasted with a
// mangled header is a regression this repository believes it is running and is
// not.
func TestEveryCommittedCrasherIsAWellFormedCorpusFile(t *testing.T) {
	const header = "go test fuzz v1"

	root := repoRoot(t)
	for _, dir := range corpusDirs(t, root) {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if first := firstLine(t, filepath.Join(root, path)); first != header {
				t.Errorf("%s does not begin with %q but with %q.\n"+
					"The fuzzing engine skips a file it cannot read rather than refusing it, so this "+
					"input is committed and is executed by nothing.", path, header, first)
			}
		}
	}
}

// fuzzTargets is every fuzz target in the repository, by directory, with the
// number of f.Add calls in each.
//
// Read out of the source rather than asked of `go test -list`, for the reason
// repoSource is walked from disk: `go list` answers for one GOOS at a time, and
// a gate that cannot see a file behind a build tag is a gate with a hole in it
// shaped like a platform.
func fuzzTargets(t *testing.T) map[string]map[string]int {
	t.Helper()

	_, files := repoSource(t)
	found := map[string]map[string]int{}

	for _, f := range files {
		if !f.test {
			continue
		}
		for _, decl := range f.syn.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !isFuzzTarget(fn) {
				continue
			}
			if found[f.dir] == nil {
				found[f.dir] = map[string]int{}
			}
			found[f.dir][fn.Name.Name] = countAdds(fn)
		}
	}

	if len(found) == 0 {
		t.Fatal("no fuzz targets found at all, so every gate in this file would pass by having nothing to check")
	}
	return found
}

// isFuzzTarget reports whether fn is func FuzzX(f *testing.F).
//
// The receiver and the parameter type are both checked rather than the name
// alone. A helper called FuzzHelper(t *testing.T) is not a target, and counting
// it would make the list above disagree with what the toolchain runs.
func isFuzzTarget(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Fuzz") || fn.Body == nil {
		return false
	}
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return false
	}

	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "F"
}

// countAdds counts the f.Add calls in a fuzz target's body.
//
// Counted by the method name on any receiver rather than by tracking the
// parameter's identifier through the function. The looser rule is the right one
// here: a seed added through a helper that takes the *testing.F is still a
// seed, and this only ever has to tell nought from more than nought.
func countAdds(fn *ast.FuncDecl) int {
	adds := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Add" {
			adds++
		}
		return true
	})
	return adds
}

// corpusDirs is every testdata/fuzz/<Target> directory in the repository,
// relative to the root and in slash form.
//
// Walked rather than globbed at a fixed depth, because the packages that carry
// a corpus are not known in advance: the first one will be created by a
// workflow run and committed by whoever reads the issue it opened.
func corpusDirs(t *testing.T, root string) []string {
	t.Helper()

	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return fs.SkipDir
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// testdata/fuzz/<Target>, and nothing deeper: Go writes corpus files
		// directly into the target's directory.
		if parent := filepath.Dir(rel); filepath.Base(parent) == "fuzz" &&
			filepath.Base(filepath.Dir(parent)) == "testdata" {
			dirs = append(dirs, filepath.ToSlash(rel))
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking for corpus directories: %v", err)
	}
	return dirs
}

// firstLine reads the first line of a file, for a check that is about a header.
func firstLine(t *testing.T, path string) string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return ""
	}
	return strings.TrimRight(scanner.Text(), "\r")
}
