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
	"slices"
	"sort"
	"strconv"
	"testing"

	"github.com/kmoneil/spacebar/internal/auth"
)

// secretOwner is the package that owns what a profile's secrets are called,
// as a directory and as an import path. The two are written out rather than
// derived from each other, because the pair is read once here and a helper that
// joined them would be longer than both.
const (
	secretOwner   = "internal/auth"
	secretPackage = "github.com/kmoneil/spacebar/internal/auth"
)

// secretNameType is the type every one of those names carries.
const secretNameType = "SecretName"

// The two gates in this file exist because of one bug, and it is worth writing
// down what the bug was rather than only what the rules are.
//
// `profile rm` deleted one secret by name, the webhook URL, out of the three a
// profile can hold. On a user-OAuth profile it therefore removed the
// configuration entry, printed "removed", exited 0, and left the OAuth token
// and the client secret in the keyring. The token record carries a refresh
// token, so what survived the command that claimed to destroy it was a
// long-lived credential for somebody's Chat account.
//
// The fix is auth.ProfileSecrets, which removal walks. What these gates protect
// is the thing a list cannot protect itself from: a secret added later and not
// added to it. Neither gate needs to know what the secrets are, which is the
// point, because a gate that carried its own copy of the list would be one more
// place for the list to be wrong.

// TestEverySecretNameIsInProfileSecrets holds the list to the constants.
//
// One direction is the one that matters: a SecretName declared in internal/auth
// and missing from ProfileSecrets is a credential nothing removes. The other
// direction is checked too, because a name in the list that no constant
// declares is a list that has outlived something, and it is free to check.
func TestEverySecretNameIsInProfileSecrets(t *testing.T) {
	declared := declaredSecretNames(t)
	if len(declared) == 0 {
		t.Fatalf("no %s constants found in %s, so this gate would pass by having nothing to check",
			secretNameType, secretOwner)
	}

	listed := map[string]bool{}
	for _, name := range auth.ProfileSecrets {
		listed[string(name)] = true
	}
	if len(listed) != len(auth.ProfileSecrets) {
		t.Errorf("auth.ProfileSecrets has %d entries and %d distinct values, so one is repeated",
			len(auth.ProfileSecrets), len(listed))
	}

	for value, where := range declared {
		if !listed[value] {
			t.Errorf("%s declares the secret %q and auth.ProfileSecrets does not list it.\n"+
				"A profile's credential that removal does not walk outlives `profile rm`, "+
				"and the command reports success anyway. Add it to the list.", where, value)
		}
	}
	for value := range listed {
		if _, ok := declared[value]; !ok {
			t.Errorf("auth.ProfileSecrets lists %q and no %s constant in %s declares it",
				value, secretNameType, secretOwner)
		}
	}
}

// TestEveryStoredSecretNamesAConstant is the gate with teeth.
//
// auth.SecretName is a named type and not a guarantee: an untyped literal
// converts to it implicitly, so Ref(profile, "made-up") compiles and stores a
// secret under a name nothing will ever delete. The type is what makes the set
// legible; this is what makes it closed.
//
// Every call to auth.Ref outside a test has to name one of the identifiers in
// the ProfileSecrets literal. Not one of the values, one of the identifiers,
// which is deliberate: matching on the string would let a second constant with
// the same value through, and the question this asks is whether the name being
// stored under is one the list already knows about.
//
// Tests are exempt, like every other boundary gate here. They plant secrets to
// assert things about them and are not what ships.
func TestEveryStoredSecretNamesAConstant(t *testing.T) {
	fset, files := repoSource(t)

	known := profileSecretIdents(t, fset, files)
	if len(known) == 0 {
		t.Fatal("could not read the identifiers out of auth.ProfileSecrets, so this gate did not run")
	}

	calls := 0
	for _, f := range files {
		if f.test {
			continue
		}
		imports := importPaths(t, f)

		// A variable that ranges over the list is in the list, by construction.
		// That is how removal reaches every secret, so the one call site that
		// walks it has to be allowed, and allowing it by recognising the range
		// is narrower than exempting a function by name.
		allowed := rangeOverProfileSecrets(f)
		for name := range known {
			allowed[name] = true
		}

		ast.Inspect(f.syn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isAuthRef(call.Fun, f, imports) || len(call.Args) < 2 {
				return true
			}
			calls++

			name, ok := secretArgName(call.Args[1])
			if !ok {
				t.Errorf("%s:%d: Ref is called with a secret name that is not a constant.\n"+
					"It has to be one of %v, because auth.RemoveProfile walks that list and "+
					"anything stored under another name outlives the profile it belongs to.",
					f.rel, fset.Position(call.Pos()).Line, sorted(known))
				return true
			}
			if !allowed[name] {
				t.Errorf("%s:%d: Ref stores a secret under %s, which auth.ProfileSecrets does not list.\n"+
					"`profile rm` walks that list, so this credential would outlive the profile "+
					"and the command would report success. Add it to the list.",
					f.rel, fset.Position(call.Pos()).Line, name)
			}
			return true
		})
	}

	if calls == 0 {
		t.Fatal("no calls to auth.Ref found outside tests, so this gate would pass by having nothing to check")
	}
}

// secretArgName is the identifier a Ref call names its secret by.
//
// Two shapes again, and for the same reason as isAuthRef: internal/auth writes
// WebhookSecret and internal/cli writes auth.WebhookSecret. Only the last
// element is returned, because that is what the ProfileSecrets literal spells.
func secretArgName(arg ast.Expr) (string, bool) {
	switch e := arg.(type) {
	case *ast.Ident:
		return e.Name, true
	case *ast.SelectorExpr:
		if _, ok := e.X.(*ast.Ident); ok {
			return e.Sel.Name, true
		}
	}
	return "", false
}

// rangeOverProfileSecrets collects the variables a file binds by ranging over
// auth.ProfileSecrets.
//
// `for _, name := range ProfileSecrets` gives `name` a value from the list on
// every iteration, so a Ref built from it is a Ref the list already covers.
// Recognising that is what lets the gate stay strict everywhere else: the
// alternative is exempting RemoveProfile by name, which would go on exempting
// it after somebody changed what it does.
func rangeOverProfileSecrets(f sourceFile) map[string]bool {
	bound := map[string]bool{}

	ast.Inspect(f.syn, func(n ast.Node) bool {
		stmt, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		if name, isName := secretArgName(stmt.X); !isName || name != "ProfileSecrets" {
			return true
		}
		if value, isIdent := stmt.Value.(*ast.Ident); isIdent {
			bound[value.Name] = true
		}
		return true
	})
	return bound
}

// isAuthRef reports whether this call expression is a call to auth.Ref.
//
// Two shapes, because internal/auth calls it unqualified and everything else
// calls it through the import. Anything else named Ref elsewhere is not this
// function and is left alone.
func isAuthRef(fun ast.Expr, f sourceFile, imports map[string]string) bool {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name == "Ref" && f.dir == secretOwner
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		return ok && e.Sel.Name == "Ref" && imports[pkg.Name] == secretPackage
	}
	return false
}

// declaredSecretNames finds every SecretName constant in internal/auth and maps
// its value onto where it was declared.
//
// Read out of the source rather than out of the package, because Go has no
// reflection over constants: the values are reachable at run time only through
// the list this is checking, which would make the check circular.
func declaredSecretNames(t *testing.T) map[string]string {
	t.Helper()

	fset, files := repoSource(t)
	found := map[string]string{}

	for _, f := range files {
		if f.test || f.dir != secretOwner {
			continue
		}
		for _, decl := range f.syn.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, where, ok := secretNameConst(spec, f, fset)
				if ok {
					found[value] = where
				}
			}
		}
	}
	return found
}

// secretNameConst reads one const spec, and answers only for a spec that
// declares the type by name.
//
// A const block that names the type once and then omits it does not extend it
// to the later entries: a spec with a value and no type is an untyped constant,
// not a SecretName, so it would not be found here and would be caught by the
// other gate instead, at the call that stored it. Which is the right order:
// what matters is not how a name was declared but whether removal walks it.
func secretNameConst(spec ast.Spec, f sourceFile, fset *token.FileSet) (value, where string, ok bool) {
	v, isValue := spec.(*ast.ValueSpec)
	if !isValue || len(v.Values) != 1 {
		return "", "", false
	}
	typeName, isIdent := v.Type.(*ast.Ident)
	if !isIdent || typeName.Name != secretNameType {
		return "", "", false
	}
	lit, isLit := v.Values[0].(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING {
		return "", "", false
	}
	text, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", "", false
	}
	return text, f.rel + ":" + strconv.Itoa(fset.Position(spec.Pos()).Line), true
}

// profileSecretIdents reads the identifiers out of the ProfileSecrets literal.
//
// The declaration is `var ProfileSecrets = []SecretName{A, B, C}`, and what is
// wanted is A, B and C by name. Taking them from the source rather than from
// the package is what lets the other gate compare an identifier at a call site
// against them: at run time the list holds values, and the names are gone.
func profileSecretIdents(t *testing.T, _ *token.FileSet, files []sourceFile) map[string]bool {
	t.Helper()

	names := map[string]bool{}
	for _, f := range files {
		if f.test || f.dir != secretOwner {
			continue
		}
		for _, decl := range f.syn.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				collectProfileSecrets(spec, names)
			}
		}
	}
	return names
}

func collectProfileSecrets(spec ast.Spec, into map[string]bool) {
	v, ok := spec.(*ast.ValueSpec)
	if !ok || len(v.Names) != 1 || v.Names[0].Name != "ProfileSecrets" || len(v.Values) != 1 {
		return
	}
	lit, ok := v.Values[0].(*ast.CompositeLit)
	if !ok {
		return
	}
	for _, element := range lit.Elts {
		if ident, isIdent := element.(*ast.Ident); isIdent {
			into[ident.Name] = true
		}
	}
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return slices.Clip(out)
}
