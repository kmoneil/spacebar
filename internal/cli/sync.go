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
	"iter"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/store"
	"github.com/kmoneil/spacebar/internal/transport"
)

// syncResult is what one space's sync reports.
type syncResult struct {
	Space   string `json:"space"`
	Fetched int    `json:"fetched"`
	Held    int    `json:"held"`

	// Oldest and Newest are the window the index covers after this run, which
	// is the answer to "how far back does my history go" and is not otherwise
	// visible anywhere.
	Oldest string `json:"oldest,omitempty"`
	Newest string `json:"newest,omitempty"`
}

func newSyncCmd(opts *Options) *cobra.Command {
	var (
		all     bool
		limit   int
		refresh bool
	)

	cmd := &cobra.Command{
		Use:   "sync [SPACE]",
		Short: "Copy a space's messages into the local index",
		Long: `Copy a space's messages into the local index.

  ` + meta.AppName + ` sync spaces/AAAAAAA
  ` + meta.AppName + ` sync eng --limit 5000
  ` + meta.AppName + ` sync --all

There is no message search API for an ordinary user. spaces.search is
administrator-only and searches spaces rather than messages, so searching what
was said means keeping a copy, and this is the command that makes one.
` + meta.AppName + ` search reads it back.

It is resumable and holds no cursor of its own. The index already knows the
window it covers, so a run fetches everything newer than the newest message it
holds and everything older than the oldest, and an interrupted run resumes by
asking the same question again. Nothing is fetched twice and no gap is left in
the middle. The cost is one request per run to discover there is nothing older,
which is cheaper than a second file that can disagree with the first.

--limit bounds how many messages one run fetches per space, so a space with a
hundred thousand messages can be brought down over several runs rather than in
one. It is not a truncation: the run stops where it stopped, and the next one
carries on from there.

The index is the only copy of a message that has since been deleted, because
the API will not answer for one twice. It lives under XDG_DATA_HOME rather than
the cache directory for that reason, and nothing here ever removes it.`,

		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkSyncTarget(all, args); err != nil {
				return err
			}

			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "sync", transport.CanRead); err != nil {
				return err
			}
			return runSync(cmd, r, opened, all, args, limit, refresh)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "sync every space this profile can reach")
	f.IntVar(&limit, "limit", 0, "stop after this many messages per space; 0 means every one")
	addRefreshFlag(cmd, &refresh)

	return cmd
}

// runSync walks the target spaces, split from the command for the complexity
// ceiling.
func runSync(cmd *cobra.Command, r *output.Renderer, opened *profile.Open,
	all bool, args []string, limit int, refresh bool,
) error {
	index, err := openIndex()
	if err != nil {
		return err
	}

	spaces, err := syncTargets(cmd.Context(), opened, all, args, refresh)
	if err != nil {
		return err
	}

	for _, space := range spaces {
		result, err := syncOne(cmd.Context(), r, opened.Transport, index, space, limit)
		if err != nil {
			return finish(r, opened, err)
		}
		if err := r.Item(result, result.Space,
			strconv.Itoa(result.Fetched), strconv.Itoa(result.Held)); err != nil {
			return finish(r, opened, err)
		}
	}
	return finish(r, opened, nil)
}

// checkSyncTarget refuses naming a space and --all at once, and naming neither.
func checkSyncTarget(all bool, args []string) error {
	switch {
	case all && len(args) > 0:
		return output.Usagef("sync --all covers every space, so it cannot also take %q.\n"+
			"Ask for one of them.", args[0])
	case !all && len(args) == 0:
		return output.Usagef("sync needs a space, or --all.\n"+
			"  %s sync spaces/AAAAAAA\n  %s sync --all", meta.AppName, meta.AppName)
	case !all && len(args) > 1:
		return output.Usagef("sync takes one space. Use --all to cover every one.")
	}
	return nil
}

// openIndex opens the on-disk index.
func openIndex() (*store.NDJSON, error) {
	dir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	return store.NewNDJSON(dir), nil
}

// syncTargets is the list of spaces to bring down.
func syncTargets(ctx context.Context, opened *profile.Open, all bool, args []string, refresh bool) ([]string, error) {
	if all {
		return everySpace(ctx, opened)
	}
	space, err := opened.Resolve(ctx, args[0], refresh)
	if err != nil {
		return nil, err
	}
	return []string{space}, nil
}

// messageLister is the part of a transport a sync uses, which is one method.
//
// Declared here, at the point of use, rather than taken as the whole
// *profile.Open the caller happens to hold. Two things come of that and the
// second is the reason.
//
// A sync reads messages and does nothing else, so a parameter that could also
// send is a parameter one bug away from sending. That is the same argument
// transport.Profiled makes for a refusal needing only three methods.
//
// And it is what makes the walk testable at all. `sync` gates on CanRead, which
// only a user-OAuth profile has, and no test in this package can point one at a
// test server, because chat.BaseURL is a constant so that nothing can redirect
// where a credential goes. So the whole command cannot be driven end to end,
// and until this existed neither could any part of it: syncOne and fetchInto
// held the resumable-copy algorithm and had no test of any kind.
//
// The alternative was to give internal/chat a way to be pointed somewhere else,
// which trades the guarantee that keeps a credential where it belongs for the
// convenience of testing. This costs one interface.
//
// refactor-01 moves this walk out of internal/cli so that an MCP tool can call
// it too, and proposes the same interface in whatever package it lands in. This
// is a step along that path rather than away from it.
type messageLister interface {
	Messages(ctx context.Context, req chat.ListMessagesRequest) iter.Seq2[chat.Message, error]
}

// syncIndex is the part of the store a sync writes through.
//
// Three methods, for the reason messageLister is one: this is what the walk
// uses, and a parameter narrowed to it cannot reach Search or Delete.
//
// It also makes the thing perf-03 is about observable. Bounds is a full scan of
// the space, and how many times one run makes it is a property of this walk that
// no assertion about the answer can see: a test can only tell three scans from
// two by counting them. The fake in sync_test.go counts and delegates to a real
// *store.NDJSON, so nothing about the index itself is faked.
type syncIndex interface {
	Visit(space string) error
	Bounds(ctx context.Context, space string) (oldest, newest time.Time, count int, err error)
	Append(ctx context.Context, space string, msgs []rows.Message) error
}

// syncOne brings one space down, forwards from the newest held and backwards
// from the oldest.
func syncOne(ctx context.Context, r *output.Renderer, messages messageLister,
	index syncIndex, space string, limit int,
) (syncResult, error) {
	// Recorded before anything is fetched, so that a space which turns out to
	// have no messages still counts as looked at. Otherwise `search` reports it
	// as missing from the index and tells somebody to run the sync they just
	// ran.
	if err := index.Visit(space); err != nil {
		return syncResult{}, err
	}

	oldest, newest, _, err := index.Bounds(ctx, space)
	if err != nil {
		return syncResult{}, err
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
		return syncResult{}, err
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
			return syncResult{}, err
		}
	}

	// Then backwards, but only while there is budget left. A --limit that was
	// spent catching up is spent.
	older, err := fetchOlder(ctx, messages, index, space, oldest, limit, fetched)
	if err != nil {
		return syncResult{}, err
	}
	fetched += older

	return reportOn(ctx, r, index, space, fetched)
}

// fetchOlder is the backward pass, and it spends what the forward pass left.
//
// Split from syncOne for the complexity ceiling, and the split is where the two
// jobs already were: catching up and going back are different questions about
// the same space, and only this one has budget arithmetic in it.
//
// A limit that was spent catching up is spent, so this makes no request at all
// then. That is deliberate: a run bounded by --limit stops where it stopped and
// the next one carries on, so spending the last of a budget on history rather
// than on today would make a caught-up run fall behind.
func fetchOlder(ctx context.Context, messages messageLister, index syncIndex,
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
// answer what the index now holds. See syncOne for why the other two collapsed
// into one.
//
// Split from syncOne for the complexity ceiling. It is also the only part of
// that function that writes anything to a stream, which makes it the natural
// piece to lift out.
func reportOn(ctx context.Context, r *output.Renderer, index syncIndex,
	space string, fetched int,
) (syncResult, error) {
	from, to, total, err := index.Bounds(ctx, space)
	if err != nil {
		return syncResult{}, err
	}

	result := syncResult{Space: space, Fetched: fetched, Held: total}
	if !from.IsZero() {
		result.Oldest = from.UTC().Format(time.RFC3339Nano)
	}
	if !to.IsZero() {
		result.Newest = to.UTC().Format(time.RFC3339Nano)
	}

	if fetched == 0 {
		r.Note("%s: nothing new, %d messages held.", space, total)
	}
	return result, nil
}

// fetchInto walks one request and appends what it finds, in batches.
//
// Batched rather than one append per message, because each append takes the
// file lock, and a hundred thousand locks is a hundred thousand chances to be
// interrupted between two of them. A batch is written by one locked Write, so
// an interrupted sync leaves whole records and resumes from the last one.
func fetchInto(ctx context.Context, messages messageLister, index syncIndex,
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
