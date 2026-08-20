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
	"slices"
	"testing"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/store"
)

// What a command that changes a message does to the local copy, asserted
// through the command rather than through the walk behind it.
//
// Only the half that does not need a server can be asserted here, and that is
// the half worth having from this side. `messages edit` and `messages delete`
// both need a user-OAuth profile, and nothing in this package can point one at
// a test server, because chat.BaseURL is a constant so that no environment
// variable can redirect where a credential goes. So a successful mutation
// cannot be reached from a command, and internal/store/mutate_test.go is where
// that lives.
//
// What is reachable is the ways of not succeeding, and each one is a way of
// writing a record for something that did not happen: a --dry-run that made no
// request, and a confirmation that was refused. The index has to be untouched
// by both, and this is the only place that can be shown with the real command,
// the real flags and the real index on disk.
//
// A request that was made and failed is deliberately not here, though it was
// and it passed. The proxy configuredUserOAuth points at is unreachable, so a
// dial-stage failure is retried on the schedule internal/chat retries anything
// else, and the two cases cost sixteen seconds in a package that otherwise runs
// in half of one. The claim they held is the walk recording nothing when the
// transport failed, which internal/store/mutate_test.go holds at the seam in
// milliseconds. What is left uncovered by dropping them is the wiring alone,
// and TestAnAdapterCannotChangeAMessageWithoutRecordingIt is what holds that.

// indexedMessage seeds the index for one space and returns the name of the
// message it holds.
func indexedMessage(t *testing.T, space string) string {
	t.Helper()

	dir, err := config.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	name := space + "/messages/AAA"
	if err := store.NewNDJSON(dir).Append(context.Background(), space, []rows.Message{{
		Name:       name,
		CreateTime: "2026-08-17T09:00:00Z",
		Text:       "deploy done",
	}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	return name
}

// indexHolds reports how many messages a search of the whole index answers
// with, which is what a person would see and is therefore what these tests
// assert on.
func indexHolds(t *testing.T, text string) int {
	t.Helper()

	dir, err := config.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}

	held := 0
	for _, err := range store.NewNDJSON(dir).Search(context.Background(), store.Query{Text: text}) {
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		held++
	}
	return held
}

// TestAChangeThatDidNotHappenLeavesTheIndexAlone.
//
// The tombstone and the updated record are written after the API has answered,
// so none of these three should reach the index. Each of them would look like a
// success from inside the command if the order were the other way round, and
// each leaves a different trace: a local history of edits nobody made, or a
// search that stops answering with a message still sitting in the space.
//
// The dry run is the one worth having most. --dry-run is stopped in the client
// on the line before the send and comes back as *chat.DryRun, which is an
// error, so what keeps it out of the index is that the walk records nothing on
// a failed request. Nothing else in the tree asserts that a dry run leaves the
// disk alone.
func TestAChangeThatDidNotHappenLeavesTheIndexAlone(t *testing.T) {
	const space = "spaces/AAAATestSpace"

	for _, tc := range []struct {
		name string
		args []string

		// target replaces the message the args name, for a case that is about
		// a message the index was never holding.
		target string

		exit output.ExitCode
	}{
		{
			name: "a delete that was only a dry run",
			args: []string{"messages", "delete", "", "--yes", "--dry-run"},
			exit: output.ExitOK,
		},
		{
			name: "an edit that was only a dry run",
			args: []string{"messages", "edit", "", "the replacement words", "--dry-run"},
			exit: output.ExitOK,
		},
		{
			// Nobody on the other end of stdin, so there is nobody to ask and
			// the command stops in front of the request.
			name: "a delete nobody confirmed",
			args: []string{"messages", "delete", ""},
			exit: output.ExitRefused,
		},
		{
			// A space this index has never held, so the question is not
			// whether a record was written but whether a file was created.
			name:   "a delete in a space nobody has synced",
			args:   []string{"messages", "delete", "", "--yes", "--dry-run"},
			target: "spaces/AAAANeverSynced/messages/AAA",
			exit:   output.ExitOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configuredUserOAuth(t)
			message := indexedMessage(t, space)

			args := append([]string{}, tc.args...)
			args[2] = message
			if tc.target != "" {
				args[2] = tc.target
			}

			got := runCLIIn(t, "", args...)
			if got.exit != tc.exit {
				t.Fatalf("exit = %d, want %d\n%s", got.exit, tc.exit, got.stderr)
			}

			if held := indexHolds(t, "deploy done"); held != 1 {
				t.Errorf("the index holds %d copies of the message, want the one it started with", held)
			}
			if held := indexHolds(t, "replacement"); held != 0 {
				t.Errorf("text from an edit that never happened is in the index")
			}

			// And no space was brought into the index, which is the claim that
			// matters for the case whose message is in a space nobody synced. A
			// file for it would move that space from "never synced" to "synced
			// and empty", and `search` would stop naming it among the spaces it
			// did not look in.
			if got := indexedSpaces(t); !slices.Equal(got, []string{space}) {
				t.Errorf("the index holds %v, want only the space this test synced", got)
			}
		})
	}
}

// indexedSpaces is every space the index holds a file for.
func indexedSpaces(t *testing.T) []string {
	t.Helper()

	dir, err := config.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	spaces, err := store.NewNDJSON(dir).Spaces()
	if err != nil {
		t.Fatalf("Spaces: %v", err)
	}
	return spaces
}
