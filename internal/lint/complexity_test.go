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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// maxCognitive is the ceiling for one function.
//
// Cognitive complexity, not cyclomatic: a flat switch with twenty arms is easy
// to read and scores low, while three levels of nested conditions score high,
// which is the right way round. Fifteen is comfortably above anything an
// honest function needs and comfortably below the point where a reviewer stops
// being able to hold it in their head.
//
// Raising it is not the fix when this fails. Splitting the function is.
const maxCognitive = 15

// TestNoFunctionIsTooComplex fails rather than skips when gocognit is missing.
//
// A complexity gate that did not run reads exactly like one that passed, and
// the whole value of a gate is that it is not optional on the day somebody is
// in a hurry. CI installs the tool as part of the test job for this reason;
// locally, `make tools` does.
func TestNoFunctionIsTooComplex(t *testing.T) {
	bin, err := exec.LookPath("gocognit")
	if err != nil {
		t.Fatalf("gocognit is not installed, so this gate did not run: %v\nrun: make tools", err)
	}

	root := repoRoot(t)
	// Test files are excluded. A table-driven test with a long literal and a
	// golden helper that branches on -update are both legitimately shaped that
	// way, and holding them to the same ceiling as shipped code produces
	// pressure to make the tests worse.
	cmd := exec.Command(bin,
		"-over", strconv.Itoa(maxCognitive),
		"-ignore", `_test\.go`,
		filepath.Join(root, "cmd"),
		filepath.Join(root, "internal"),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}

	report := strings.TrimSpace(string(out))
	if report == "" {
		t.Fatalf("gocognit failed with no output: %v", err)
	}
	t.Errorf("these functions are over the cognitive complexity ceiling of %d:\n\n%s\n\n"+
		"Split them. Raising the ceiling is not the fix.", maxCognitive, report)
}
