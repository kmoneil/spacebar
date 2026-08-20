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
	"strings"
	"testing"

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
