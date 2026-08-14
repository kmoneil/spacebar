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
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var (
	goDirective = regexp.MustCompile(`(?m)^go (\S+)\s*$`)
	patchSemver = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	workflowGo  = regexp.MustCompile(`(?m)^\s*GO_VERSION:\s*"([^"]+)"`)
)

// TestGoModNamesAPatchVersion holds the reason every workflow can pin a single
// number.
//
// A directive of "1.26" would let each runner pick its own patch, and then the
// standard library govulncheck reports on is not the standard library the
// release was built against. Naming the patch makes the toolchain part of the
// build rather than part of the machine.
func TestGoModNamesAPatchVersion(t *testing.T) {
	m := goDirective.FindStringSubmatch(readRepoFile(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	if !patchSemver.MatchString(m[1]) {
		t.Errorf("go.mod says 'go %s'; it has to name the patch, e.g. 'go 1.26.6'", m[1])
	}
}

// TestWorkflowsAgreeWithGoMod is the invariant every workflow's own comment
// points at.
//
// actions/setup-go exports GOTOOLCHAIN=local, so the go command will not fetch
// what go.mod asks for. It refuses, and every job fails at once with "go.mod
// requires go >= X (running go Y; GOTOOLCHAIN=local)".
//
// The subtler failure is the licence gate. go-licenses decides what is
// standard library by comparing a package's source path against the GOROOT
// compiled into go-licenses itself. The moment go.mod names a toolchain the
// runner does not have, the go command fetches it into the module cache,
// GOROOT moves there, no package looks like stdlib any more, and the run dies
// with "some errors occurred when loading direct and transitive dependency
// packages", which points at none of this. The Makefile passes the real
// GOROOT through so that case is survivable, but the two numbers still move
// together or the rest of CI does not run at all.
func TestWorkflowsAgreeWithGoMod(t *testing.T) {
	m := goDirective.FindStringSubmatch(readRepoFile(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	want := m[1]

	for _, path := range workflowFiles(t) {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}
			found := workflowGo.FindAllStringSubmatch(string(b), -1)
			if found == nil {
				t.Fatalf("%s does not set GO_VERSION; it would run on whatever the runner ships", name)
			}
			for _, f := range found {
				if f[1] != want {
					t.Errorf("%s pins GO_VERSION %q but go.mod says %q; they move together", name, f[1], want)
				}
			}
		})
	}
}
