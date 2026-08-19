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
	"go/ast"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// This repository cites tests by name constantly: CLAUDE.md names 116 and
// SECURITY.md names 161, and a claim in either is only worth what the named
// test holds. A citation that resolves to nothing is worse than no citation,
// because it sends the next reader looking for a gate that is not there and
// they conclude the rule is covered.
//
// Nothing checked them until 2026-08-19, and four had rotted by then. Three
// were renames the citing document did not follow. The fourth was created
// while writing the gate's own paragraph, by wrapping a long name across two
// lines, which is the failure mode that makes this worth automating rather
// than reviewing: it is invisible in a diff and obvious to a matcher.
//
// internal/cli/docs_test.go already resolves every `spacebar` command line in
// the documents against the real command tree. This is the same idea pointed
// at test names, and it covers the source as well, because a doc comment that
// cites a test is making the same promise a document is.

// citedTest matches a Go test or fuzz target named in prose. Anchored on the
// Test or Fuzz prefix and an upper-case letter after it, which is the
// convention every one of them follows.
var citedTest = regexp.MustCompile(`\b((?:Test|Fuzz)[A-Z][A-Za-z0-9]*)\b`)

// citingDocs are the tracked documents that make claims about tests, and every
// one of them must exist.
//
// Listed rather than globbed, because a glob would pick up CHANGELOG.md, whose
// entries describe releases and may legitimately name a test that has since
// been renamed: history is not a claim about today.
var citingDocs = []string{
	"SECURITY.md",
	"CONTRIBUTING.md",
	"README.md",
	"docs/ADMIN.md",
	"docs/AGENTS.md",
	"docs/SKILL.md",
}

// localDocs are checked when they are there and not missed when they are not.
//
// CLAUDE.md is gitignored, so it is on the machine somebody works on and is
// absent from a fresh clone. Reading it unconditionally is how this gate went
// red on its first CI run before it ever reached one: a test that requires a
// file the repository does not carry fails everywhere except where it was
// written.
//
// It is still worth checking where it exists. It names more tests than any
// other document in the tree and it is the one a session reads first, so a
// citation that has rotted there misleads the reader most likely to act on it.
var localDocs = []string{"CLAUDE.md"}

// TestEveryTestNamedInADocumentExists.
func TestEveryTestNamedInADocumentExists(t *testing.T) {
	defined := definedTests(t)

	for _, doc := range append(append([]string(nil), citingDocs...), localDocs...) {
		body, err := os.ReadFile(filepath.Join(repoRoot(t), doc))
		if err != nil {
			if os.IsNotExist(err) && slices.Contains(localDocs, doc) {
				continue
			}
			t.Fatalf("reading %s: %v", doc, err)
		}
		for _, name := range citedTest.FindAllString(string(body), -1) {
			if !defined[name] {
				t.Errorf("%s names %s, and no test by that name exists.\n"+
					"Either it was renamed and this citation did not follow, or the name is "+
					"broken across a line: a wrapped name is invisible in a diff and reads as a "+
					"gate that is not there.", doc, name)
			}
		}
	}
}

// TestEveryTestNamedInACommentExists is the same claim about the source.
//
// A doc comment that cites a test is making a document's promise in a place a
// document's reader will never look, which is if anything the more load-bearing
// of the two: it is read by whoever is about to change the code beside it.
//
// Only comments. A name in code is a call and the compiler holds it.
func TestEveryTestNamedInACommentExists(t *testing.T) {
	defined := definedTests(t)
	fset, files := repoSource(t)

	for _, f := range files {
		for _, group := range f.syn.Comments {
			for _, line := range group.List {
				for _, name := range citedTest.FindAllString(line.Text, -1) {
					if defined[name] {
						continue
					}
					// A comment introducing the function directly below it is
					// the commonest citation there is, and the commonest way
					// to get it wrong is to rename the function and not the
					// comment. Reported the same way as any other.
					pos := fset.Position(line.Pos())
					t.Errorf("%s:%d names %s, and no test by that name exists.",
						f.rel, pos.Line, name)
				}
			}
		}
	}
}

// definedTests is every test and fuzz target in the repository, by name.
//
// Read out of the syntax rather than by grepping for "func Test", so that a
// name inside a string or a comment cannot masquerade as a definition. That
// matters here more than usual: this gate's own failure messages contain test
// names, and a grep would find them and call the citation satisfied by the
// error that reports it.
func definedTests(t *testing.T) map[string]bool {
	t.Helper()

	_, files := repoSource(t)
	defined := map[string]bool{}

	for _, f := range files {
		if !f.test {
			continue
		}
		for _, decl := range f.syn.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Test") || strings.HasPrefix(fn.Name.Name, "Fuzz") {
				defined[fn.Name.Name] = true
			}
		}
	}

	if len(defined) == 0 {
		t.Fatal("no tests found, so every citation would fail and this gate would be noise")
	}
	return defined
}
