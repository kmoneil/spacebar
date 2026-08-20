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

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/resolve"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/store"
)

// searchable configures a profile and puts one message in the index.
//
// A webhook profile, deliberately. `search` requires no capability because it
// makes no request, so this is the population that proves it: somebody whose
// organization allows nothing but a webhook can still search what a colleague's
// user-authorized profile copied down.
func searchable(t *testing.T, spaces ...string) {
	t.Helper()
	configured(t)

	dir, err := config.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	index := store.NewNDJSON(dir)
	for _, space := range spaces {
		if err := index.Append(context.Background(), space, []rows.Message{{
			Name:       space + "/messages/AAA",
			CreateTime: "2026-08-17T09:00:00Z",
			Text:       "deploy done",
		}}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// warningsOn decodes the warning envelopes out of a --json run's stderr.
//
// Every line has to be one, because --json promises a stream of documents on
// stderr rather than a mixture of JSON and prose a caller has to guess at.
func warningsOn(t *testing.T, stderr string) []struct {
	Code    string `json:"code"`
	Message string `json:"message"`
} {
	t.Helper()

	var found []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if line == "" {
			continue
		}
		var envelope struct {
			Warning *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"warning"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("a line on stderr under --json is not a JSON document: %v\n%s", err, line)
		}
		if envelope.Warning != nil {
			found = append(found, *envelope.Warning)
		}
	}
	return found
}

// TestASearchSaysWhatItCouldNotSearchToAProgramAsWellAsToAPerson.
//
// reportUnsearched has three outcomes and two of them went out through Note,
// which returns early under --json. So the branch that says "there is no way to
// know what is missing" did not exist for a program, and the comment above it
// already said why it has to: "3 results" reads as complete.
//
// It is not a rare branch. resolve.Cache treats a list older than resolve.TTL
// as a miss, and that is 24 hours, so a --json search run the morning after a
// space listing said nothing about its own coverage at all.
//
// Three cases, one per outcome, and the codes are what a program branches on.
// The complete case stays a Note deliberately: a caveat on a complete answer is
// the noise that teaches people to skip the line that mattered, and asserting
// its absence is what stops somebody "fixing" that later.
func TestASearchSaysWhatItCouldNotSearchToAProgramAsWellAsToAPerson(t *testing.T) {
	const held = "spaces/AAAATestSpace"
	const never = "spaces/AAAANeverSynced"

	t.Run("no cached space list", func(t *testing.T) {
		searchable(t, held)

		got := runCLIIn(t, "", "search", "--json", "deploy")
		if got.exit != output.ExitOK {
			t.Fatalf("exit = %d\n%s", got.exit, got.stderr)
		}

		warnings := warningsOn(t, got.stderr)
		if len(warnings) != 1 || warnings[0].Code != output.WarnPartialCoverage {
			t.Fatalf("want one %s warning, got %+v\nstderr:\n%s",
				output.WarnPartialCoverage, warnings, got.stderr)
		}
		if !strings.Contains(warnings[0].Message, "no cached space list") {
			t.Errorf("the warning does not say why coverage is unknown: %q", warnings[0].Message)
		}
	})

	t.Run("a space that was never synced", func(t *testing.T) {
		searchable(t, held)
		if err := resolve.NewCache("alerts").Write([]chat.Space{{Name: held}, {Name: never}}); err != nil {
			t.Fatalf("seeding the space list: %v", err)
		}

		got := runCLIIn(t, "", "search", "--json", "deploy")
		if got.exit != output.ExitOK {
			t.Fatalf("exit = %d\n%s", got.exit, got.stderr)
		}

		warnings := warningsOn(t, got.stderr)
		if len(warnings) != 1 || warnings[0].Code != output.WarnPartialCoverage {
			t.Fatalf("want one %s warning, got %+v\nstderr:\n%s",
				output.WarnPartialCoverage, warnings, got.stderr)
		}
		if !strings.Contains(warnings[0].Message, never) {
			t.Errorf("the warning does not name the unsearched space: %q", warnings[0].Message)
		}
	})

	// The state this test had no case for until 2026-08-20, and it is the one
	// that was on a real machine: a cache file that parses, names this profile
	// and is fresh, and lists nothing. resolve.Cache.Read used to answer
	// "known: nothing" for it, so the comparison found nothing missing and the
	// search called itself complete, which is exactly what the two cases above
	// exist to prevent.
	//
	// Written by hand rather than through Write, because Write now declines an
	// empty list and this has to hold against a file however it arrived: a
	// restore, a copy between machines, or a build older than the fix.
	t.Run("a cached space list with nothing in it", func(t *testing.T) {
		searchable(t, held)
		writeSpaceList(t, "alerts", "[]")

		got := runCLIIn(t, "", "search", "--json", "deploy")
		if got.exit != output.ExitOK {
			t.Fatalf("exit = %d\n%s", got.exit, got.stderr)
		}

		warnings := warningsOn(t, got.stderr)
		if len(warnings) != 1 || warnings[0].Code != output.WarnPartialCoverage {
			t.Fatalf("want one %s warning, got %+v\nstderr:\n%s",
				output.WarnPartialCoverage, warnings, got.stderr)
		}
		if !strings.Contains(warnings[0].Message, "no cached space list") {
			t.Errorf("the warning does not say why coverage is unknown: %q", warnings[0].Message)
		}
	})

	// And a null, which is what the machine this was found on actually held.
	// encoding/json writes an empty slice as null, so this is the shape a real
	// file takes and the other is the shape somebody hand-edits.
	t.Run("a cached space list that is null", func(t *testing.T) {
		searchable(t, held)
		writeSpaceList(t, "alerts", "null")

		got := runCLIIn(t, "", "search", "--json", "deploy")
		if got.exit != output.ExitOK {
			t.Fatalf("exit = %d\n%s", got.exit, got.stderr)
		}
		warnings := warningsOn(t, got.stderr)
		if len(warnings) != 1 || warnings[0].Code != output.WarnPartialCoverage {
			t.Fatalf("want one %s warning, got %+v\nstderr:\n%s",
				output.WarnPartialCoverage, warnings, got.stderr)
		}
	})

	t.Run("nothing missing stays a note and says nothing under json", func(t *testing.T) {
		searchable(t, held)
		if err := resolve.NewCache("alerts").Write([]chat.Space{{Name: held}}); err != nil {
			t.Fatalf("seeding the space list: %v", err)
		}

		got := runCLIIn(t, "", "search", "--json", "deploy")
		if got.exit != output.ExitOK {
			t.Fatalf("exit = %d\n%s", got.exit, got.stderr)
		}
		if warnings := warningsOn(t, got.stderr); len(warnings) != 0 {
			t.Errorf("a complete search warned about its own completeness: %+v", warnings)
		}
		if !strings.Contains(got.stdout, "deploy done") {
			t.Errorf("the search found nothing:\n%s", got.stdout)
		}
	})
}

// writeSpaceList puts a cache file on disk with the spaces field spelled
// exactly as given, which resolve.Cache.Write will no longer produce.
//
// By hand for that reason. The states worth defending against are the ones a
// file can be in however it got there: restored from a backup, copied from
// another machine, or written by a build from before the empty list was
// refused.
func writeSpaceList(t *testing.T, profile, spaces string) {
	t.Helper()

	dir, err := config.CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating the cache directory: %v", err)
	}

	body := fmt.Sprintf(`{"fetched":%q,"profile":%q,"spaces":%s}`,
		time.Now().UTC().Format(time.RFC3339Nano), profile, spaces)
	path := filepath.Join(dir, "spaces-"+profile+".json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestACompleteSearchCountsWhatItComparedAgainst.
//
// The Note names the spaces this profile can reach, and counted the spaces in
// the index. Those are the same number only while the index holds nothing the
// profile cannot reach, and it can: a space that was synced and then left, or
// synced under a profile name since authorized as somebody else, stays in the
// index forever, because nothing removes it.
//
// So this is the one case that tells the two counts apart, and without it the
// sentence can overstate by any number without a test noticing.
func TestACompleteSearchCountsWhatItComparedAgainst(t *testing.T) {
	const reachable = "spaces/AAAATestSpace"
	const left = "spaces/AAAALeftBehind"

	searchable(t, reachable, left)
	if err := resolve.NewCache("alerts").Write([]chat.Space{{Name: reachable}}); err != nil {
		t.Fatalf("seeding the space list: %v", err)
	}

	got := runCLIIn(t, "", "search", "deploy")
	if got.exit != output.ExitOK {
		t.Fatalf("exit = %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "searched all 1 spaces this profile can reach") {
		t.Errorf("the note counts the index rather than the comparison:\n%s", got.stderr)
	}
}
