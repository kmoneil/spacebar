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

// usesLine matches one `uses:` in a workflow, capturing what it names, the ref
// after the @, and whatever follows on the line.
var usesLine = regexp.MustCompile(`(?m)^\s*(?:-\s*)?uses:\s*([^@\s]+)@(\S+)(.*)$`)

// commitSHA is a full git object name, which is the only ref that cannot be
// moved. Forty lowercase hex digits: the abbreviated form is refused because
// two of them can collide and because Dependabot writes the full one.
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// versionComment is the trailing `# v1.2.3` that says what the SHA was when it
// was written.
var versionComment = regexp.MustCompile(`^\s*#\s*v\S+\s*$`)

// TestEveryActionIsPinnedToACommit holds every workflow step to a commit rather
// than to a tag.
//
// A tag is a mutable ref. `actions/checkout@v7` is whatever that ref points at
// on the morning CI runs, and whoever can move the tag chooses what executes
// with this repository's token. That is the supply-chain hole a signed release
// does not close: `release.yml` attests the provenance of the binary, and an
// action that ran before the attestation was made is inside the thing being
// attested.
//
// A local action, `uses: ./something`, is exempt because it is this repository
// and is already whatever the commit under test says it is.
func TestEveryActionIsPinnedToACommit(t *testing.T) {
	for _, path := range workflowFiles(t) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}

		name := filepath.Base(path)
		found := 0
		for _, m := range usesLine.FindAllStringSubmatch(string(b), -1) {
			action, ref, rest := m[1], m[2], m[3]
			if strings.HasPrefix(action, "./") {
				continue
			}
			found++

			if !commitSHA.MatchString(ref) {
				t.Errorf("%s: %s is pinned to %q, which is a name somebody can move; use the commit SHA and put the tag in a trailing comment",
					name, action, ref)
				continue
			}

			// The comment is not decoration. Dependabot reads it to know which
			// version the SHA is, and rewrites both together; without it a
			// reader has forty hex digits and no way to tell a current pin from
			// a two-year-old one.
			if !versionComment.MatchString(rest) {
				t.Errorf("%s: %s is pinned to a commit with no trailing version comment; write `@%s # vX.Y.Z` so the pin says what it is",
					name, action, ref)
			}
		}

		if found == 0 {
			t.Errorf("%s: no third-party action found, so this file was checked by having nothing in it", name)
		}
	}
}
