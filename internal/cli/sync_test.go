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
	"bytes"
	"context"
	"errors"
	"iter"
	"slices"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/store"
)

// This file is the harness the sync walk never had.
//
// `sync` gates on CanRead, which only a user-OAuth profile has, and no test in
// this package can point one at a test server: chat.BaseURL is a constant so
// that nothing can redirect where a credential goes. So the command cannot be
// driven end to end, and until messageLister existed no part of it could be
// driven at all. syncOne and fetchInto held the whole resumable-copy algorithm,
// including the batching rule that decides what an interrupted run keeps, and
// had no test of any kind.
//
// What is faked is one method. Everything else in these tests is the real
// thing: the real walk, the real index on a real temporary directory, the real
// Bounds. That is the point of faking at the narrowest seam rather than at the
// transport: the less that is fake, the more a green test is worth.

// syncClock is where the fixtures put their timestamps, so that every message
// in this file has a time somebody can read in a failure message.
func syncClock(minutes int) string {
	return time.Date(2026, 8, 17, 9, minutes, 0, 0, time.UTC).Format(time.RFC3339Nano)
}

// fakeMessages is a space the sync walk can read, and it honours the query.
//
// Honouring it is the whole value. A fake that returned its canned list
// whatever was asked would make every assertion below pass against a walk that
// had stopped filtering, which is the failure the group-membership fake in
// internal/mcpsrv was fixed for. So Since and Until are applied exclusively,
// because that is what the API does and what messageFilter builds; OrderBy
// decides the direction, with empty meaning newest first as internal/chat
// fills it in; and Limit truncates.
type fakeMessages struct {
	// messages are in create-time order, oldest first.
	messages []chat.Message

	// requests is every query the walk made, in order, so that a test can
	// assert what was asked for rather than only what came back. The number of
	// requests is the thing perf-03 is about.
	requests []chat.ListMessagesRequest

	// failAfter yields an error once this many messages have been handed over
	// across the whole walk, standing in for a run that was interrupted. Zero
	// means never.
	failAfter int
	delivered int
}

func (f *fakeMessages) Messages(_ context.Context, req chat.ListMessagesRequest) iter.Seq2[chat.Message, error] {
	f.requests = append(f.requests, req)

	kept := make([]chat.Message, 0, len(f.messages))
	for _, m := range f.messages {
		at, err := time.Parse(time.RFC3339Nano, m.CreateTime)
		if err != nil {
			continue
		}
		if !req.Since.IsZero() && !at.After(req.Since) {
			continue
		}
		if !req.Until.IsZero() && !at.Before(req.Until) {
			continue
		}
		kept = append(kept, m)
	}
	if req.OrderBy != chat.OrderOldestFirst {
		slices.Reverse(kept)
	}
	if req.Limit > 0 && req.Limit < len(kept) {
		kept = kept[:req.Limit]
	}

	return func(yield func(chat.Message, error) bool) {
		for _, m := range kept {
			if f.failAfter > 0 && f.delivered >= f.failAfter {
				yield(chat.Message{}, errors.New("the connection went away part way through a page"))
				return
			}
			if !yield(m, nil) {
				return
			}
			f.delivered++
		}
	}
}

// spaceOfMinutes builds a message in the test space at a given minute.
func spaceOfMinutes(space string, minutes ...int) []chat.Message {
	out := make([]chat.Message, 0, len(minutes))
	for _, m := range minutes {
		out = append(out, chat.Message{
			Name:       space + "/messages/" + time.Duration(m).String(),
			CreateTime: syncClock(m),
			Text:       "message at minute " + time.Duration(m).String(),
		})
	}
	return out
}

// countingIndex is a real index with a tally of how often it was scanned.
//
// Delegating rather than faking, because the index is the half of this that
// must not be pretended: what a sync leaves behind on disk is the whole answer.
// What it adds is a count of Bounds calls, which is a property of the walk that
// no assertion about the answer can see. Bounds is a full scan of the space, so
// three of them per run is three times the work of one, and only counting tells
// them apart.
type countingIndex struct {
	*store.NDJSON
	scans int
}

func (c *countingIndex) Bounds(ctx context.Context, space string) (time.Time, time.Time, int, error) {
	c.scans++
	return c.NDJSON.Bounds(ctx, space)
}

// syncHarness is an index on a temporary directory, a discard renderer, and a
// source. Everything a syncOne call needs.
type syncHarness struct {
	index  *countingIndex
	source *fakeMessages
	r      *output.Renderer
	space  string
}

// newSyncHarness seeds an index with `held` and offers `available` to fetch.
func newSyncHarness(t *testing.T, held, available []chat.Message) *syncHarness {
	t.Helper()
	const space = "spaces/AAAATestSpace"

	index := &countingIndex{NDJSON: store.NewNDJSON(t.TempDir())}
	if len(held) > 0 {
		rowsHeld := make([]rows.Message, 0, len(held))
		for _, m := range held {
			row, _ := rows.ForMessage(m)
			rowsHeld = append(rowsHeld, row)
		}
		if err := index.Append(context.Background(), space, rowsHeld); err != nil {
			t.Fatalf("seeding the index: %v", err)
		}
	}

	return &syncHarness{
		index:  index,
		source: &fakeMessages{messages: available},
		r:      output.NewRenderer(&bytes.Buffer{}, &bytes.Buffer{}, output.Options{}),
		space:  space,
	}
}

func (h *syncHarness) run(t *testing.T, limit int) syncResult {
	t.Helper()
	got, err := syncOne(context.Background(), h.r, h.source, h.index, h.space, limit)
	if err != nil {
		t.Fatalf("syncOne: %v", err)
	}
	return got
}

// bounds reads the index directly, which is what a reported result has to agree
// with.
func (h *syncHarness) bounds(t *testing.T) (oldest, newest string, count int) {
	t.Helper()

	// Through the real index rather than the counter, so that reading the
	// answer does not change the number the test is asserting on.
	from, to, n, err := h.index.NDJSON.Bounds(context.Background(), h.space)
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	return formatSyncBound(from), formatSyncBound(to), n
}

func formatSyncBound(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

// TestASyncReportsTheWindowTheIndexActuallyHolds.
//
// syncResult carries Oldest, Newest and Held, and syncOne reads all three by
// asking Bounds a third time after both fetches. Whatever else changes about
// how often it asks, the answer has to be the one the index would give, because
// that is what a caller acts on: Oldest is the answer to "how far back does my
// history go", and it is not visible anywhere else.
//
// This is the claim perf-03 has to be held to. It says nothing about how many
// times Bounds is called, deliberately; the request count is asserted separately
// below, so that a change to one cannot quietly be read as a change to the
// other.
//
// The last case is the trap. An index whose records all have unreadable create
// times has a count and no window, which internal/store pins in
// TestBoundsIsTheWindowAndTheCountAndNothingElse, so the forward fetch runs
// unbounded and can move the oldest end. A reduction guarded on "the index was empty" would be wrong there and
// right everywhere else, which is the shape of a bug that ships.
func TestASyncReportsTheWindowTheIndexActuallyHolds(t *testing.T) {
	const space = "spaces/AAAATestSpace"

	for _, tc := range []struct {
		name      string
		held      []chat.Message
		available []chat.Message
		limit     int

		// fetched is what one run copies down, and it is the number that
		// catches a window bounded by a stale value: an unbounded backward
		// pass fetches everything a second time, supersedes it by name, and
		// leaves Held looking exactly right.
		fetched int

		// requests is how many list windows the run opened. A limit spent
		// entirely going forwards skips the backward pass, and that is the
		// only case with one.
		requests int
	}{
		{
			name:      "an empty index fetches everything",
			available: spaceOfMinutes(space, 1, 5, 9),
			fetched:   3,
			requests:  2,
		},
		{
			name:      "only newer messages exist",
			held:      spaceOfMinutes(space, 5),
			available: spaceOfMinutes(space, 5, 7, 9),
			fetched:   2,
			requests:  2,
		},
		{
			name:      "only older messages exist",
			held:      spaceOfMinutes(space, 5),
			available: spaceOfMinutes(space, 1, 3, 5),
			fetched:   2,
			requests:  2,
		},
		{
			name:      "older and newer both exist",
			held:      spaceOfMinutes(space, 5),
			available: spaceOfMinutes(space, 1, 3, 5, 7, 9),
			fetched:   4,
			requests:  2,
		},
		{
			name:      "a limit spent entirely going forwards",
			held:      spaceOfMinutes(space, 5),
			available: spaceOfMinutes(space, 1, 3, 5, 7, 9),
			limit:     1,
			fetched:   1,
			requests:  1,
		},
		{
			name:      "a limit that spills into the backward pass",
			held:      spaceOfMinutes(space, 5),
			available: spaceOfMinutes(space, 1, 3, 5, 7, 9),
			limit:     3,
			fetched:   3,
			requests:  2,
		},
		{
			name: "an index whose times are all unreadable",
			held: []chat.Message{
				{Name: space + "/messages/BAD", CreateTime: "not a time", Text: "held"},
			},
			available: spaceOfMinutes(space, 1, 5, 9),
			fetched:   3,
			requests:  2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSyncHarness(t, tc.held, tc.available)
			got := h.run(t, tc.limit)

			oldest, newest, count := h.bounds(t)
			if got.Oldest != oldest {
				t.Errorf("reported oldest %q, index holds %q", got.Oldest, oldest)
			}
			if got.Newest != newest {
				t.Errorf("reported newest %q, index holds %q", got.Newest, newest)
			}
			if got.Held != count {
				t.Errorf("reported held %d, index holds %d", got.Held, count)
			}
			if got.Fetched != tc.fetched {
				t.Errorf("fetched %d, want %d: a window bounded by a stale value refetches "+
					"what is already held, and supersession hides it from Held", got.Fetched, tc.fetched)
			}
			if len(h.source.requests) != tc.requests {
				t.Errorf("opened %d list windows, want %d", len(h.source.requests), tc.requests)
			}
		})
	}
}

// TestAnInterruptedSyncKeepsWhatItAlreadyFetched.
//
// fetchInto's doc comment says a batch is written by one locked Write so that
// "an interrupted sync leaves whole records and resumes from the last one", and
// that nothing had ever run it. The failure path flushes the partial batch
// before reporting, which is the line the claim rests on.
//
// Two halves. The records already fetched survive the failure, and a second run
// against a source that has recovered picks up from there rather than starting
// over or leaving a gap.
func TestAnInterruptedSyncKeepsWhatItAlreadyFetched(t *testing.T) {
	const space = "spaces/AAAATestSpace"
	available := spaceOfMinutes(space, 1, 3, 5, 7, 9)

	h := newSyncHarness(t, nil, available)
	h.source.failAfter = 2

	if _, err := syncOne(context.Background(), h.r, h.source, h.index, h.space, 0); err == nil {
		t.Fatal("an interrupted sync reported success")
	}

	_, _, count := h.bounds(t)
	if count != 2 {
		t.Fatalf("the index holds %d records after an interrupted run, want the 2 already fetched", count)
	}

	// The source recovers and the same command is run again, which is the whole
	// of what "resumable and holds no cursor" means.
	h.source.failAfter = 0
	h.source.delivered = 0
	got := h.run(t, 0)

	if got.Held != len(available) {
		t.Errorf("after resuming, the index holds %d of %d messages", got.Held, len(available))
	}
	if got.Fetched != len(available)-2 {
		t.Errorf("the second run fetched %d, want %d: nothing should be fetched twice",
			got.Fetched, len(available)-2)
	}
	if oldest, newest, _ := h.bounds(t); oldest != syncClock(1) || newest != syncClock(9) {
		t.Errorf("the window after resuming is %s to %s, want %s to %s",
			oldest, newest, syncClock(1), syncClock(9))
	}
}

// TestASyncAsksForOneWindowInEachDirection.
//
// Two list requests and what each one bounds. The forward window is ordered
// oldest first, so a caught-up run reads in the order the conversation
// happened; the backward window is ordered newest first, so a limit cuts from
// the recent end. Getting either direction wrong leaves the answer looking
// right and the resume point wrong.
//
// The number of index scans is a different property and is asserted separately,
// in TestASyncScansTheIndexTwiceRatherThanThreeTimes. An earlier draft of this
// test was named for a Bounds count it never checked, which is the shape of a
// gate that reads as coverage.
func TestASyncAsksForOneWindowInEachDirection(t *testing.T) {
	const space = "spaces/AAAATestSpace"

	h := newSyncHarness(t, spaceOfMinutes(space, 5), spaceOfMinutes(space, 1, 3, 5, 7, 9))
	h.run(t, 0)

	if len(h.source.requests) != 2 {
		t.Errorf("the walk made %d list requests, want 2: one forward window and one backward",
			len(h.source.requests))
	}
	forward, backward := h.source.requests[0], h.source.requests[1]
	if forward.OrderBy != chat.OrderOldestFirst || forward.Since.IsZero() || !forward.Until.IsZero() {
		t.Errorf("the forward window is %+v; it has to be bounded below and ordered oldest first "+
			"so a caught-up run reads in the order the conversation happened", forward)
	}
	if backward.OrderBy != chat.OrderNewestFirst || backward.Until.IsZero() || !backward.Since.IsZero() {
		t.Errorf("the backward window is %+v; it has to be bounded above and ordered newest first "+
			"so a limit cuts from the recent end", backward)
	}
}

// TestASyncScansTheIndexTwiceRatherThanThreeTimes.
//
// Bounds is a full scan of the space, and syncOne asked three times: once for
// the watermark to fetch forwards from, once for the oldest end to fetch
// backwards from, and once for the report. The first already computed the
// oldest end that the second recomputed, and the forward fetch between them
// cannot have moved it, because that fetch is bounded above the newest message
// held.
//
// At a hundred thousand records one scan is 480ms and 131MB, per
// BenchmarkBounds, so the third of them was about a third of what a caught-up
// sync costs before it reaches the network. sync --all multiplies it by the
// space count.
//
// Counted rather than argued, because a count is the only thing that can tell
// two scans from three: every assertion about the answer passes either way.
//
// The zero-watermark case keeps its third scan and that is correct rather than
// a miss. See syncOne.
func TestASyncScansTheIndexTwiceRatherThanThreeTimes(t *testing.T) {
	const space = "spaces/AAAATestSpace"

	t.Run("an ordinary run", func(t *testing.T) {
		h := newSyncHarness(t, spaceOfMinutes(space, 5), spaceOfMinutes(space, 1, 3, 5, 7, 9))
		h.run(t, 0)

		if h.index.scans != 2 {
			t.Errorf("scanned the index %d times, want 2: the watermark scan already answers "+
				"the oldest end, and the forward fetch cannot have moved it", h.index.scans)
		}
	})

	t.Run("an empty index still needs the third", func(t *testing.T) {
		h := newSyncHarness(t, nil, spaceOfMinutes(space, 1, 5, 9))
		h.run(t, 0)

		if h.index.scans != 3 {
			t.Errorf("scanned the index %d times, want 3: with no watermark the forward fetch "+
				"is unbounded, so the oldest end has to be read again", h.index.scans)
		}
	})

	t.Run("a zero watermark on a non-empty index still needs the third", func(t *testing.T) {
		h := newSyncHarness(t, []chat.Message{
			{Name: space + "/messages/BAD", CreateTime: "not a time", Text: "held"},
		}, spaceOfMinutes(space, 1, 5, 9))
		h.run(t, 0)

		if h.index.scans != 3 {
			t.Errorf("scanned the index %d times, want 3: a space whose times will not parse "+
				"has a count and no window, so the count is the wrong thing to guard on",
				h.index.scans)
		}
	})
}
