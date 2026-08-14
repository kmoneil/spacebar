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
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/meta"
)

// nameOwner is the one package that may write the product name out.
const nameOwner = "internal/meta"

// TestOnlyMetaSpellsTheProductName holds the rule internal/meta says is held.
//
// The name is a placeholder. It was chosen because it contains no Google
// trademark, and this is an independent third-party client that must never put
// one in its name, its module path, its repository, or its binary. A rename is
// therefore a live possibility rather than a hypothetical, and SPEC.md requires
// it to be a change to meta.AppName and to nothing else.
//
// What that costs is this test. A literal in a help string, an error message,
// an environment variable, a keyring service name, or a path under the user's
// home directory all survive a rename and go on naming the old product, and
// most of them do it silently: the binary is called one thing and reads its
// configuration from a directory called another.
//
// Import paths are exempt, because the module path is not the product name even
// though it currently contains it, and renaming the module is a different and
// louder operation. Test files are exempt, for the same reason the boundary
// gates exempt them: the rule is about what ships.
func TestOnlyMetaSpellsTheProductName(t *testing.T) {
	fset, files := repoSource(t)
	needle := strings.ToLower(meta.AppName)

	for _, f := range files {
		if f.test || f.dir == nameOwner || strings.HasPrefix(f.dir, nameOwner+"/") {
			continue
		}

		// An import path is a string literal like any other, so the ones this
		// file declares are collected and skipped by position.
		imports := map[token.Pos]bool{}
		for _, imp := range f.syn.Imports {
			imports[imp.Path.Pos()] = true
		}

		ast.Inspect(f.syn, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || imports[lit.Pos()] {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if !strings.Contains(strings.ToLower(value), needle) {
				return true
			}
			t.Errorf("%s:%d writes the product name into a string literal, and only %s may.\n"+
				"A rename has to be a change to meta.AppName and to nothing else, and a literal here "+
				"would survive it and go on naming the old product.\n"+
				"Use meta.AppName.",
				f.rel, fset.Position(lit.Pos()).Line, nameOwner)
			return true
		})
	}
}
