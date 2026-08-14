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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These two gates are structural rather than behavioural. Both of the rules
// they hold are of the form "one package owns this", and a rule of that shape
// cannot be tested by calling anything: it is a claim about which files exist
// and what is written in them.
//
// Both are written before the packages that could break them, deliberately. A
// boundary asserted in advance costs one file; the same boundary asserted after
// the fact costs somebody unpicking a diff that has already been reviewed.

// sourceFile is one .go file in this repository, parsed.
type sourceFile struct {
	rel  string // path from the repository root, in slash form.
	dir  string // the directory it is in, in slash form: "internal/output".
	test bool   // whether it is a _test.go file.
	syn  *ast.File
}

// skippedDirs hold no compiled source, or hold source that is deliberately not
// part of this module. testdata is the one that matters: a fixture is allowed
// to contain anything, including source that breaks a rule on purpose.
var skippedDirs = map[string]bool{
	".git":     true,
	"bin":      true,
	"dist":     true,
	"testdata": true,
	"_plans":   true,
	"_reviews": true,
	"_tmp":     true,
}

// repoSource parses every .go file in the repository.
//
// Walked from disk rather than asked of `go list`, because `go list` answers
// for one GOOS and GOARCH at a time. A file behind //go:build windows is
// invisible to a gate that runs on Linux, and that is not a hypothetical here:
// internal/lint/notice_test.go exists because mousetrap reaches the binary
// through exactly that route and a scan on the maintainer's machine cannot see
// it. A rule about what the source says should be asked of the source.
//
// The cost is that a file the go command would never compile is still checked.
// That is the right way round: a package excluded from today's build is one
// somebody intends to build somewhere.
func repoSource(t *testing.T) (*token.FileSet, []sourceFile) {
	t.Helper()

	root := repoRoot(t)
	fset := token.NewFileSet()
	var files []sourceFile

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skippedDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// Object resolution is skipped because nothing here needs it and the
		// ast.Object it populates is deprecated.
		syn, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", filepath.ToSlash(rel), err)
		}

		files = append(files, sourceFile{
			rel:  filepath.ToSlash(rel),
			dir:  filepath.ToSlash(filepath.Dir(rel)),
			test: strings.HasSuffix(d.Name(), "_test.go"),
			syn:  syn,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no .go files found, so this gate would pass by having nothing to check")
	}
	return fset, files
}

// importPaths maps the identifier a file refers to a package by onto that
// package's import path.
//
// The name is the alias when there is one and the last element of the path
// otherwise, which is an assumption that holds for every package these gates
// care about: os, fmt, and log all declare the name their path ends with.
//
// A dot import defeats this, because the package's names arrive unqualified and
// there is nothing left to match on. Rather than quietly miss one, the caller
// fails: a gate that did not run reads exactly like one that passed.
func importPaths(t *testing.T, f sourceFile) map[string]string {
	t.Helper()

	names := map[string]string{}
	for _, imp := range f.syn.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Errorf("%s: unparseable import %s", f.rel, imp.Path.Value)
			continue
		}
		switch {
		case imp.Name == nil:
			names[path[strings.LastIndex(path, "/")+1:]] = path
		case imp.Name.Name == "_":
			// Imported for its side effects. No identifier to qualify.
		case imp.Name.Name == ".":
			t.Errorf("%s dot-imports %q, which this gate cannot see through. "+
				"Import it under a name.", f.rel, path)
		default:
			names[imp.Name.Name] = path
		}
	}
	return names
}

// httpOwner is the one package that may build an HTTP request.
const httpOwner = "internal/chat"

// TestOnlyChatImportsNetHTTP holds the rule that makes redaction possible.
//
// Every credential this tool handles leaves the process the same way: in a
// request. An Authorization header, and a webhook URL whose key and token query
// parameters are the whole of the authentication for that space. Redaction
// happens where the request is built, so it holds only while there is one place
// that builds one. A package with a client of its own is not a second style, it
// is a second code path that nothing redacts, and it will be found by reading a
// log that should not have contained a credential.
//
// Test files are exempt. The rule is about shipped code, and a test that stands
// up an httptest server to assert that a request was never made is the rule
// working rather than a breach of it.
//
// What this does not cover: a dependency that speaks HTTP on our behalf.
// golang.org/x/oauth2 arrives in Milestone 3 and builds the token request
// itself, carrying the client secret and the refresh token, and no direct
// import of net/http appears anywhere near it. That is a real hole and it
// belongs to the card that adds the dependency, not to this gate.
func TestOnlyChatImportsNetHTTP(t *testing.T) {
	fset, files := repoSource(t)

	for _, f := range files {
		if f.test || f.dir == httpOwner || strings.HasPrefix(f.dir, httpOwner+"/") {
			continue
		}
		for _, imp := range f.syn.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Errorf("%s: unparseable import %s", f.rel, imp.Path.Value)
				continue
			}
			if path != "net/http" && !strings.HasPrefix(path, "net/http/") {
				continue
			}
			t.Errorf("%s:%d imports %q, and only %s may.\n"+
				"Redaction happens where the request is built, so a package with a client of "+
				"its own sends an Authorization header or a webhook URL that nothing redacted.\n"+
				"Build the request through %s instead.",
				f.rel, fset.Position(imp.Pos()).Line, path, httpOwner, httpOwner)
		}
	}
}

// streamOwner is the one package that may name the streams of the process it is
// running in.
const streamOwner = "internal/output"

// bannedSelectors are the qualified names that reach a process stream without
// being handed a writer, by import path and then by identifier. The value is
// the stream each one reaches.
//
// The fmt and log entries matter more than the os ones. os.Stdout is visible in
// review and somebody would ask about it; fmt.Println is what a debugging line
// looks like, and it writes to exactly the stream that is supposed to carry
// nothing but data.
var bannedSelectors = map[string]map[string]string{
	"os": {
		"Stdout": "stdout",
		"Stderr": "stderr",
	},
	"fmt": {
		"Print":   "stdout",
		"Printf":  "stdout",
		"Println": "stdout",
	},
	"log": {
		"Print":   "stderr",
		"Printf":  "stderr",
		"Println": "stderr",
		"Fatal":   "stderr",
		"Fatalf":  "stderr",
		"Fatalln": "stderr",
		"Panic":   "stderr",
		"Panicf":  "stderr",
		"Panicln": "stderr",
	},
}

// TestOnlyOutputWritesToTheProcessStreams holds the rule that makes an escaping
// decision hold everywhere or nowhere.
//
// Two claims rest on it. stdout is data and nothing else, so a warning or a
// progress line that lands there corrupts the output of a caller parsing it,
// and a failing command has to write nothing to it at all. And a message body
// comes from people the operator may not know, while a terminal is a program
// that interprets bytes, so a body reaching a terminal has to have its control
// characters escaped first. Neither survives being everybody's responsibility.
//
// Nothing is exempt but internal/output itself. cmd/spacebar in particular is
// not, though it is the natural place for a process to bind its own streams:
// today it is two lines and needs neither, and a command that finds it needs to
// print something has found a reason to call into internal/output rather than a
// reason to widen this list.
//
// Test files are exempt, because a test binary's stdout belongs to `go test`.
//
// What this does not cover: cobra reaches os.Stdout on its own behalf, through
// cmd.OutOrStdout, and that is how internal/cli is meant to write. This gate
// says nobody may pick up the stream directly. It cannot say that what travels
// through cobra's writer was rendered by internal/output, and the golden files
// are what hold that.
func TestOnlyOutputWritesToTheProcessStreams(t *testing.T) {
	fset, files := repoSource(t)

	for _, f := range files {
		if f.test || f.dir == streamOwner || strings.HasPrefix(f.dir, streamOwner+"/") {
			continue
		}
		names := importPaths(t, f)

		ast.Inspect(f.syn, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				// A local identifier shadowing an import name would be read
				// here as the package. It has never happened, and the failure
				// is a loud one that names the file and the line, so the cost
				// of being wrong is a minute rather than a bug.
				pkg, ok := node.X.(*ast.Ident)
				if !ok {
					return true
				}
				stream, ok := bannedSelectors[names[pkg.Name]][node.Sel.Name]
				if !ok {
					return true
				}
				reportStreamUse(t, fset, f, node.Pos(),
					fmt.Sprintf("%s.%s", pkg.Name, node.Sel.Name), stream)

			case *ast.CallExpr:
				// The builtins, which take no import and so appear in no list
				// of dependencies. println is what somebody reaches for when
				// they do not want to add an import to print one value.
				fn, ok := node.Fun.(*ast.Ident)
				if !ok || (fn.Name != "print" && fn.Name != "println") {
					return true
				}
				if _, shadowed := names[fn.Name]; shadowed {
					return true
				}
				reportStreamUse(t, fset, f, node.Pos(), fn.Name, "stderr")
			}
			return true
		})
	}
}

func reportStreamUse(t *testing.T, fset *token.FileSet, f sourceFile, pos token.Pos, what, stream string) {
	t.Helper()
	t.Errorf("%s:%d uses %s, which writes to %s, and only %s may.\n"+
		"stdout carries data and nothing else, and anything printed to either stream has to be "+
		"escaped by the one package that knows how.\n"+
		"Take an io.Writer, or render through %s.",
		f.rel, fset.Position(pos).Line, what, stream, streamOwner, streamOwner)
}
