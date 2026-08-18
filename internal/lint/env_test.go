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
	"go/token"
	"strconv"
	"testing"

	"github.com/kmoneil/spacebar/internal/config"
)

// envOwner is the package that owns the list, as a directory and as an import
// path, written out for the reason secrets_test.go writes its pair out.
const (
	envOwner   = "internal/config"
	envPackage = "github.com/kmoneil/spacebar/internal/config"
)

// This gate exists because a variable that works and that nobody can find is
// indistinguishable from one that does not exist.
//
// SPACEBAR_PROFILE was read from the first milestone and appeared nowhere in
// the hand-written documentation. Its whole public existence was the
// parenthesis at the end of --profile's help string, which is not where
// somebody wiring up a CI job looks, and the one document written for a program
// driving this tool said "every command accepts --profile NAME" and stopped
// there. NO_COLOR, TERM and XDG_CONFIG_HOME were in no document at all.
//
// config.EnvVars is the fix and this is what stops it going stale. The other
// half, that every entry is actually written down, is
// TestEveryEnvironmentVariableIsDocumented in internal/cli, next to the gate
// that holds the same rule for commands.

// envReader is a call that reads the environment, and which of its arguments
// names the variable.
//
// config.Resolve is here because it is a reader with the name passed in: the
// os.Getenv inside it can only be checked at the call sites that supply the
// name. Recognising it by name is deliberate and is the narrow part of this
// gate, so a forwarder that is not listed fails rather than passing quietly.
//
// What is not covered is os.Environ, which hands the whole environment over
// rather than naming anything. It has no call site today. A future one would be
// a subprocess inheriting the environment, which is a different claim from this
// one and belongs to SECURITY.md rather than here.
type envReader struct {
	pkg  string // import path, or envOwner's, for a call qualified by a package.
	dir  string // the directory in which the same call appears unqualified.
	fn   string
	name int // index of the argument that names the variable.
}

var envReaders = []envReader{
	{pkg: "os", dir: "", fn: "Getenv", name: 0},
	{pkg: "os", dir: "", fn: "LookupEnv", name: 0},
	{pkg: envPackage, dir: envOwner, fn: "Resolve", name: 1},
}

// TestEveryEnvironmentReadNamesAListedVariable is the gate with teeth.
//
// Every read of the environment outside a test has to name something in
// config.EnvVars. An unknown expression is a failure rather than a skip,
// because a gate that could not read a call site reads exactly like one that
// found nothing wrong with it.
func TestEveryEnvironmentReadNamesAListedVariable(t *testing.T) {
	fset, files := repoSource(t)

	listed := map[string]bool{}
	for _, v := range config.EnvVars {
		listed[v.Name] = true
	}
	if len(listed) != len(config.EnvVars) {
		t.Errorf("config.EnvVars has %d entries and %d distinct names, so one is repeated",
			len(config.EnvVars), len(listed))
	}

	helpers := envNameHelpers(t, fset, files)
	read := map[string]bool{}
	sites := 0

	for _, f := range files {
		if f.test {
			continue
		}
		imports := importPaths(t, f)

		for _, decl := range f.syn.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			forwards := forwarderParams(fn, f)

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				arg, isRead := envNameArg(call, f, imports)
				if !isRead {
					return true
				}
				sites++

				where := f.rel + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)
				name, ok := envNameOf(arg, f, imports, helpers, forwards)
				switch {
				case name == forwarded:
					// The name arrives as a parameter of a listed reader, so
					// this is the read every call site of that reader funnels
					// into. Those call sites are checked instead.
				case !ok:
					t.Errorf("%s: the environment is read under a name this gate cannot resolve.\n"+
						"It has to be a string literal, a call to config.Env with one, or a "+
						"no-argument helper that returns one, so that config.EnvVars can be held "+
						"to what is actually read. Documented at %s.", where, envOwner+"/env.go")
				case !listed[name]:
					t.Errorf("%s: reads %s, and config.EnvVars does not list it.\n"+
						"A variable nobody documents is one nobody finds, and the documentation "+
						"gate reads that list. Add it, with the line somebody deciding whether to "+
						"set it needs.", where, name)
				default:
					read[name] = true
				}
				return true
			})
		}
	}

	if sites == 0 {
		t.Fatal("no environment reads found outside tests, so this gate would pass by having nothing to check")
	}

	// The other direction, and it is the worse failure of the two: an entry
	// nothing reads is a variable the documentation promises and the binary
	// ignores, which sends somebody looking for a bug in their own job.
	for _, v := range config.EnvVars {
		if !read[v.Name] {
			t.Errorf("config.EnvVars lists %s and nothing outside a test reads it.\n"+
				"Either the read was removed and the entry should go, or it moved to a shape "+
				"this gate cannot see, which is the same problem wearing a hat.", v.Name)
		}
	}
}

// forwarded is what envNameOf answers when the name is a parameter rather than
// a value. A sentinel rather than a bool, because it is one of four outcomes
// and three of them are already carried by the name and the ok.
const forwarded = "\x00forwarded"

// envNameArg returns the argument that names the variable, for a call that
// reads the environment.
func envNameArg(call *ast.CallExpr, f sourceFile, imports map[string]string) (ast.Expr, bool) {
	for _, r := range envReaders {
		if !isCallTo(call.Fun, r.fn, r.pkg, r.dir, f, imports) {
			continue
		}
		if r.name >= len(call.Args) {
			return nil, false
		}
		return call.Args[r.name], true
	}
	return nil, false
}

// envNameOf resolves the expression that names a variable.
//
// Four shapes, and every one of them appears in the tree: a literal for the
// variables somebody else named, config.Env with a suffix for the ones this
// project owns, a no-argument helper wrapping that call, and a parameter of a
// reader that takes the name from its own caller.
func envNameOf(
	arg ast.Expr,
	f sourceFile,
	imports map[string]string,
	helpers map[string]string,
	forwards map[string]bool,
) (string, bool) {
	switch e := arg.(type) {
	case *ast.BasicLit:
		return stringLit(e)

	case *ast.Ident:
		if forwards[e.Name] {
			return forwarded, true
		}
		return "", false

	case *ast.CallExpr:
		if suffix, ok := envSuffixArg(e, f, imports); ok {
			return config.Env(suffix), true
		}
		name, ok := calleeName(e.Fun)
		if !ok || len(e.Args) != 0 {
			return "", false
		}
		suffix, known := helpers[name]
		if !known {
			return "", false
		}
		return config.Env(suffix), true
	}
	return "", false
}

// envSuffixArg reads the suffix out of a call to config.Env.
func envSuffixArg(call *ast.CallExpr, f sourceFile, imports map[string]string) (string, bool) {
	if !isCallTo(call.Fun, "Env", envPackage, envOwner, f, imports) || len(call.Args) != 1 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return "", false
	}
	return stringLit(lit)
}

// envNameHelpers finds the no-argument functions that return a call to
// config.Env, and maps each onto the suffix it names.
//
// config.EnvProfile and the CLI's own webhookURLEnv are both this shape. They
// are keyed by bare function name rather than by package, which is enough
// while no two of them collide, and the collision is checked rather than
// assumed: two packages wrapping different variables behind one name would
// otherwise resolve to whichever was parsed last.
func envNameHelpers(t *testing.T, fset *token.FileSet, files []sourceFile) map[string]string {
	t.Helper()

	found := map[string]string{}
	for _, f := range files {
		if f.test {
			continue
		}
		imports := importPaths(t, f)

		for _, decl := range f.syn.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			suffix, ok := envHelperSuffix(fn, f, imports)
			if !ok {
				continue
			}
			if was, seen := found[fn.Name.Name]; seen && was != suffix {
				t.Errorf("%s:%d: two packages declare %s() and they name different variables, %s and %s.\n"+
					"This gate resolves a helper by its bare name. Rename one of them.",
					f.rel, fset.Position(fn.Pos()).Line, fn.Name.Name, config.Env(was), config.Env(suffix))
			}
			found[fn.Name.Name] = suffix
		}
	}
	return found
}

// envHelperSuffix answers for a function whose whole body is a return of one
// call to config.Env with a literal.
//
// Deliberately that narrow. A helper with anything else in it is doing
// something this gate cannot reason about, and the honest answer there is to
// fail at the call site rather than to guess here.
func envHelperSuffix(fn *ast.FuncDecl, f sourceFile, imports map[string]string) (string, bool) {
	if fn.Recv != nil || fn.Body == nil || len(fn.Body.List) != 1 || fn.Type.Params.NumFields() != 0 {
		return "", false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "", false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return "", false
	}
	return envSuffixArg(call, f, imports)
}

// forwarderParams names the parameters of fn that a caller of fn supplies a
// variable name in, and answers empty for a function that is not a listed
// reader.
func forwarderParams(fn *ast.FuncDecl, f sourceFile) map[string]bool {
	params := map[string]bool{}
	for _, r := range envReaders {
		if fn.Recv != nil || fn.Name.Name != r.fn || f.dir != r.dir {
			continue
		}
		if at := paramAt(fn, r.name); at != "" {
			params[at] = true
		}
	}
	return params
}

// paramAt is the name of the nth parameter, counting the way a caller counts:
// `func f(a, b string)` has two, not one.
func paramAt(fn *ast.FuncDecl, n int) string {
	i := 0
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if i == n {
				return name.Name
			}
			i++
		}
	}
	return ""
}

// isCallTo reports whether fun names the given function, qualified by its
// package or unqualified inside the package that declares it.
func isCallTo(fun ast.Expr, name, pkg, dir string, f sourceFile, imports map[string]string) bool {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name == name && dir != "" && f.dir == dir
	case *ast.SelectorExpr:
		qualifier, ok := e.X.(*ast.Ident)
		if !ok || e.Sel.Name != name {
			return false
		}
		return imports[qualifier.Name] == pkg
	}
	return false
}

// calleeName is the last element of a call's function expression.
func calleeName(fun ast.Expr) (string, bool) {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name, true
	case *ast.SelectorExpr:
		if _, ok := e.X.(*ast.Ident); ok {
			return e.Sel.Name, true
		}
	}
	return "", false
}

// stringLit unquotes a string literal, and answers no for anything else.
func stringLit(lit *ast.BasicLit) (string, bool) {
	if lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}
