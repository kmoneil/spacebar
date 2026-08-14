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
	"regexp"
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
	action := regexp.MustCompile(`(?m)^\s*-\s*uses:\s*golangci/golangci-lint-action@\S+\s*\n\s*with:\s*\n\s*version:\s*(\S+)\s*$`)
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
