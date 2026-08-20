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

package store

import (
	"context"
	"iter"
	"time"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/rows"
)

// The resumable copy that fills the index (SPEC.md §12.1).
//
// It lived in internal/cli until 2026-08-20, which made it a decision inside an
// adapter: internal/cli and internal/mcpsrv are both thin adapters over the
// same packages, so a walk only one of them can reach is a job only one of them
// can do. The visible consequence was that search_messages is registered only
// when the index holds something and nothing over MCP could put anything in it,
// so an agent-driven session could not reach the tool the index exists for
// unless a person ran a command out of band.
//
// It lands here rather than in a package of its own, and that was measured
// rather than argued. internal/store already imports internal/chat,
// internal/rows and internal/output, so the walk adds nothing to its dependency
// closure: 203 packages before and after. A package of its own would import all
// three anyway, cost a name, and collide with the standard library's sync,
// which this package already imports for the append lock.
//
// What did not move is the printing. internal/store returns warnings rather
// than writing them, because only internal/output writes to a process stream,
// and the "nothing new" line the walk used to print is now read off
// Result.Fetched by whichever adapter is rendering.

// Result is what one space's sync reports.
type Result struct {
	Space   string `json:"space"`
	Fetched int    `json:"fetched"`
	Held    int    `json:"held"`

	// Oldest and Newest are the window the index covers after this run, which
	// is the answer to "how far back does my history go" and is not otherwise
	// visible anywhere.
	Oldest string `json:"oldest,omitempty"`
	Newest string `json:"newest,omitempty"`
}

// MessageLister is the part of a transport a sync reads through, which is one
// method.
//
// An interface here rather than a transport, and that is what keeps
// internal/store off internal/transport: this package names the one method it
// uses and every implementation stays on the other side of it.
//
// It is also what makes the walk testable at all. A sync needs read access,
// which only a user-OAuth profile has, and nothing can point one at a test
// server, because chat.BaseURL is a constant so that no environment variable
// can redirect where a credential goes. The alternative was to give
// internal/chat a way to be aimed somewhere else, which trades the guarantee
// that keeps a credential where it belongs for the convenience of testing.
// This costs one interface.
//
// Narrow for a second reason. A sync reads messages and does nothing else, so a
// parameter that could also send is a parameter one bug away from sending,
// which is the argument transport.Profiled already makes for a refusal needing
// only three methods.
type MessageLister interface {
	Messages(ctx context.Context, req chat.ListMessagesRequest) iter.Seq2[chat.Message, error]
}

// Target is the part of the store a sync writes through.
//
// Three methods, for the reason MessageLister is one: this is what the walk
// uses, and a parameter narrowed to it cannot reach Search or Delete.
//
// It also makes how often the index is scanned observable. Bounds reads every
// record in the space, and how many times one run does that is a property of
// this walk that no assertion about the answer can see: a test can only tell
// three scans from two by counting them. The counter in sync_test.go delegates
// to a real *NDJSON, so nothing about the index itself is faked.
type Target interface {
	Visit(space string) error
	Bounds(ctx context.Context, space string) (oldest, newest time.Time, count int, err error)
	Append(ctx context.Context, space string, msgs []rows.Message) error
}

// Sync brings one space down, forwards from the newest message held and
// backwards from the oldest, and reports the window the index then covers.
//
// Resumable and holding no cursor of its own: the index already knows what it
// has, so an interrupted run resumes by being called again, with nothing
// fetched twice and no gap left in the middle.
//
// limit bounds one run rather than truncating the answer. A run that stops on
// it stops where it stopped and the next one carries on, which is why the
// forward pass spends it first: a budget spent on history rather than on today
// would make a caught-up copy fall behind.
func Sync(ctx context.Context, index Target, messages MessageLister,
	space string, limit int,
) (Result, error) {
	// Recorded before anything is fetched, so that a space which turns out to
	// have no messages still counts as looked at. Otherwise `search` reports it
	// as missing from the index and tells somebody to run the sync they just
	// ran.
	if err := index.Visit(space); err != nil {
		return Result{}, err
	}

	oldest, newest, _, err := index.Bounds(ctx, space)
	if err != nil {
		return Result{}, err
	}

	// Forwards first. A person who leaves this running wants today's messages
	// before last year's, and an interrupted run should have caught up rather
	// than have gone further back.
	fetched, err := fetchInto(ctx, messages, index, space, chat.ListMessagesRequest{
		Space:   space,
		Since:   newest,
		OrderBy: chat.OrderOldestFirst,
	}, limit)
	if err != nil {
		return Result{}, err
	}

	// The oldest end, and usually it is the one already in hand.
	//
	// The forward fetch above is bounded createTime > newest, so everything it
	// appended is newer than the newest message held, and newest is at or after
	// oldest. Nothing it could have added is older than the oldest end, so that
	// end has not moved and the scan to re-read it is a scan of the whole space
	// for an answer that cannot have changed. Bounds is 480ms and 131MB at a
	// hundred thousand records, per BenchmarkBounds, so it is not a small one.
	//
	// The exception is a zero watermark, and it is not only the empty index.
	// Bounds returns no window for a space whose records all have create times
	// that will not parse, and reports a count for them anyway, which
	// TestBoundsIsTheWindowAndTheCountAndNothingElse pins. A zero newest makes
	// messageFilter build no lower bound at all, so the forward fetch runs
	// unbounded and does move the oldest end.
	//
	// So the guard is the watermark and not the count, and the difference
	// between those two is a bug that ships: a count of zero is right about the
	// empty index and wrong about the unreadable one, where the stale oldest
	// then leaves the backward window unbounded as well and the run copies
	// every message down a second time. Held looks correct afterwards, because
	// a repeat supersedes by name, so the only visible trace is in Fetched.
	// TestASyncReportsTheWindowTheIndexActuallyHolds asserts that number for
	// exactly this reason.
	if newest.IsZero() {
		oldest, _, _, err = index.Bounds(ctx, space)
		if err != nil {
			return Result{}, err
		}
	}

	// Then backwards, but only while there is budget left. A --limit that was
	// spent catching up is spent.
	older, err := fetchOlder(ctx, messages, index, space, oldest, limit, fetched)
	if err != nil {
		return Result{}, err
	}
	fetched += older

	return reportOn(ctx, index, space, fetched)
}

// fetchOlder is the backward pass, and it spends what the forward pass left.
//
// Split from Sync for the complexity ceiling, and the split is where the two
// jobs already were: catching up and going back are different questions about
// the same space, and only this one has budget arithmetic in it.
//
// A limit that was spent catching up is spent, so this makes no request at all
// then. That is deliberate: a run bounded by --limit stops where it stopped and
// the next one carries on, so spending the last of a budget on history rather
// than on today would make a caught-up run fall behind.
func fetchOlder(ctx context.Context, messages MessageLister, index Target,
	space string, oldest time.Time, limit, spent int,
) (int, error) {
	if limit > 0 && spent >= limit {
		return 0, nil
	}

	remaining := 0
	if limit > 0 {
		remaining = limit - spent
	}
	return fetchInto(ctx, messages, index, space, chat.ListMessagesRequest{
		Space:   space,
		Until:   oldest,
		OrderBy: chat.OrderNewestFirst,
	}, remaining)
}

// reportOn is the window the index covers once a run has finished.
//
// The third and last scan of the space, and the one that cannot be avoided: it
// is the only one taken after both fetches, so it is the only one that can
// answer what the index now holds. See Sync for why the other two collapsed
// into one.
//
// Split from Sync for the complexity ceiling. It used to end by printing a line
// when a run found nothing new, and that is the one thing this walk lost on the
// way out of internal/cli: only internal/output writes to a process stream, so
// the fact travels as Result.Fetched and whichever adapter is rendering decides
// what to say about it.
func reportOn(ctx context.Context, index Target, space string, fetched int) (Result, error) {
	from, to, total, err := index.Bounds(ctx, space)
	if err != nil {
		return Result{}, err
	}

	result := Result{Space: space, Fetched: fetched, Held: total}
	if !from.IsZero() {
		result.Oldest = from.UTC().Format(time.RFC3339Nano)
	}
	if !to.IsZero() {
		result.Newest = to.UTC().Format(time.RFC3339Nano)
	}

	return result, nil
}

// fetchInto walks one request and appends what it finds, in batches.
//
// Batched rather than one append per message, because each append takes the
// file lock, and a hundred thousand locks is a hundred thousand chances to be
// interrupted between two of them. A batch is written by one locked Write, so
// an interrupted sync leaves whole records and resumes from the last one.
func fetchInto(ctx context.Context, messages MessageLister, index Target,
	space string, req chat.ListMessagesRequest, limit int,
) (int, error) {
	const batchSize = 500

	fetched := 0
	batch := make([]rows.Message, 0, batchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := index.Append(ctx, space, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	req.Limit = limit
	for message, err := range messages.Messages(ctx, req) {
		if err != nil {
			// Whatever was already batched is written before the failure is
			// reported, so an interrupted sync keeps its work and the next run
			// starts from further along.
			_ = flush()
			return fetched, err
		}

		row, _ := rows.ForMessage(message)
		batch = append(batch, row)
		fetched++

		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return fetched, err
			}
		}
		if limit > 0 && fetched >= limit {
			break
		}
	}
	return fetched, flush()
}
