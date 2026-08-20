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
	"strings"
	"testing"
)

// TestGolangciLintVersionIsDeclaredOnce is the one tool version that has to be
// written twice.
//
// Every other tool is installed by `go install ...@$(make -s print-X_VERSION)`,
// so the Makefile is the only place its version appears. golangci-lint is
// installed by an action instead, which cannot ask the Makefile anything, so
// its version is a literal in ci.yml. Two places declaring one version is
// exactly the drift the pinning exists to prevent, so the second place is held
// to the first here.
//
// Pinned at all (rather than `latest`) because golangci-lint refuses a
// module whose Go directive is newer than the Go it was itself built with, so
// a floating version can fail a pull request that changed nothing, on a day
// nobody touched the linter.
func TestGolangciLintVersionIsDeclaredOnce(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	m := regexp.MustCompile(`(?m)^GOLANGCI_LINT_VERSION\s*:=\s*(\S+)\s*$`).FindStringSubmatch(makefile)
	if m == nil {
		t.Fatal("the Makefile does not declare GOLANGCI_LINT_VERSION")
	}
	want := m[1]

	ci := readRepoFile(t, ".github/workflows/ci.yml")
	// The action and its version are on separate lines, so the pattern spans
	// both rather than trying to find a bare `version:` and hoping it belongs
	// to the right step.
	//
	// `[^\n]*` after the ref rather than `\s*`, because the ref is a commit SHA
	// with the tag it came from in a trailing comment. Anchoring straight to the
	// newline made this pattern miss and the test fail with "does not pin a
	// version", which is a true sentence about the wrong thing.
	action := regexp.MustCompile(`(?m)^\s*-\s*uses:\s*golangci/golangci-lint-action@\S+[^\n]*\n\s*with:\s*\n\s*version:\s*(\S+)\s*$`)
	got := action.FindStringSubmatch(ci)
	if got == nil {
		t.Fatal("ci.yml does not pin a version for golangci-lint-action; `latest` can fail a pull request that changed nothing")
	}
	if got[1] != want {
		t.Errorf("ci.yml runs golangci-lint %s but the Makefile pins %s; `make lint` and CI have to be the same linter", got[1], want)
	}
}

// TestEveryPinnedToolIsInstallable holds `make tools` to the version variables
// beside it. A pin nothing installs is a pin that does not bind, and the
// release gate restores its tool cache on a key derived from all of them.
func TestEveryPinnedToolIsInstallable(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")

	pins := regexp.MustCompile(`(?m)^([A-Z0-9_]+)_VERSION\s*:=`).FindAllStringSubmatch(makefile, -1)
	if len(pins) == 0 {
		t.Fatal("the Makefile declares no tool versions; this test would pass by having nothing to check")
	}

	toolsBody := regexp.MustCompile(`(?ms)^tools:.*?\n\n`).FindString(makefile)
	if toolsBody == "" {
		t.Fatal("the Makefile has no `tools` target")
	}
	keyBody := regexp.MustCompile(`(?ms)^tools-key:.*?\n\n`).FindString(makefile)
	if keyBody == "" {
		t.Fatal("the Makefile has no `tools-key` target")
	}

	for _, p := range pins {
		name := p[1] + "_VERSION"
		if !regexp.MustCompile(`\$\(` + name + `\)`).MatchString(toolsBody) {
			t.Errorf("%s is pinned but `make tools` does not install it", name)
		}
		// The release gate skips its tool install entirely on a cache hit. A
		// version that is not in the key is a version a stale cache keeps
		// serving the old binary for, silently.
		if !regexp.MustCompile(`\$\(` + name + `\)`).MatchString(keyBody) {
			t.Errorf("%s is pinned but is not in the `tools-key` cache key, so bumping it would not bust the cache", name)
		}
	}
}

// jobBlock splits a workflow into its jobs, keyed by the job id.
//
// By indentation rather than by parsing YAML, which is what every other gate in
// this package does with a workflow and which keeps this from needing a
// dependency the licence gate would then have to account for. A job starts at
// two spaces and a name, and runs until the next one.
func jobBlocks(t *testing.T, workflow string) map[string]string {
	t.Helper()

	header := regexp.MustCompile(`(?m)^  ([a-zA-Z0-9_-]+):[ \t]*$`)
	found := header.FindAllStringSubmatchIndex(workflow, -1)
	if len(found) == 0 {
		t.Fatalf("no jobs found in a workflow, so this gate would pass by checking nothing")
	}

	jobs := map[string]string{}
	for i, m := range found {
		end := len(workflow)
		if i+1 < len(found) {
			end = found[i+1][0]
		}
		jobs[workflow[m[2]:m[3]]] = workflow[m[0]:end]
	}
	return jobs
}

// TestEveryToolAWorkflowInstallsIsCachedFirst.
//
// Every `go install` in a workflow is a network call to the module proxy in
// front of a gate, on every run of every pull request, and the gate is
// protected by a ruleset that requires all six checks green under a strict
// policy. So a proxy hiccup is not a red square somebody shrugs at: it is a
// merge that does not happen until a person notices and re-runs.
//
// It is not hypothetical and it is not rare enough to ignore. On 2026-08-20 the
// post-merge run on `main` failed in the licence gate's first step, before a
// licence had been looked at, because proxy.golang.org dropped the connection
// sending a transitive dependency of go-licenses that this repository does not
// import. Five of six jobs passed and the same commit had been green on its
// branch minutes earlier.
//
// release.yml had already made this decision, after a transient
// sum.golang.org error killed a release whose tag was already public, and
// ci.yml did not have it. This gate is what stops the next job being added
// without it, which is how the first one came to be missing.
//
// It asks for the cache and for the guard, because a restore step with no `if`
// on the install below it is a cache that costs an upload and saves nothing.
func TestEveryToolAWorkflowInstallsIsCachedFirst(t *testing.T) {
	for _, path := range workflowFiles(t) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		name := filepath.Base(path)

		for job, body := range jobBlocks(t, string(b)) {
			if !strings.Contains(body, "go install") && !strings.Contains(body, "make tools") {
				continue
			}
			if !strings.Contains(body, "uses: actions/cache@") {
				t.Errorf("%s: job %q installs a tool and does not restore it from a cache first.\n"+
					"That is a module-proxy fetch in front of a required check on every run, and one "+
					"dropped connection is a merge that waits for somebody to notice and re-run.\n"+
					"Copy the three steps from the job beside it: read `make tools-key`, restore "+
					"~/go/bin, and install only on a miss.", name, job)
				continue
			}
			if !strings.Contains(body, "cache-hit != 'true'") {
				t.Errorf("%s: job %q restores a tool cache and installs anyway.\n"+
					"Without the `if` on the install step the fetch still happens on every run, so "+
					"the cache costs an upload and prevents nothing.", name, job)
			}
		}
	}
}
