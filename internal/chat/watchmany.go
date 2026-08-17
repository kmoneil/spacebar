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

package chat

import (
	"context"
	"errors"
	"iter"
	"time"
)

// PollBudget is how many requests a second one process will make while
// watching, however many spaces it was pointed at.
//
// Measured rather than guessed, on 2026-08-17, from the Cloud console's own
// quota page for the project behind the release client:
//
//	gcloud alpha services quota list --service=chat.googleapis.com \
//	  --consumer=projects/PROJECT
//
// Every Chat quota is 1/min/{project}, and spaceEvent_reads is 3000 of them,
// which is fifty requests a second. This takes a fifth of that.
//
// A fifth rather than all of it, because the unit is the project and not the
// person: the quota belongs to the OAuth client, and an organization following
// docs/ADMIN.md shares one. Five people can each run `watch --all` at full
// budget on a shared client before anybody is refused. Taking the whole ceiling
// would mean the first person to start denies the second, and neither of them
// could tell why from what Google returns, because no response says how much of
// the project's quota somebody else has spent.
//
// It is a fixed number here rather than something discovered at runtime,
// because nothing in a response carries it and an administrator can raise the
// limit without this tool ever learning.
const PollBudget = 10

// IntervalForSpaces is how often each space is polled when several are watched
// at once.
//
// The rate this process makes requests at is the space count over the interval,
// so holding that under PollBudget means the interval grows with the count:
//
//	effective = max(requested, MinPollInterval, spaces / PollBudget)
//
// The crossover is twenty spaces, which is MinPollInterval times PollBudget:
// below it the 2s floor decides the pace and the budget is never reached, and
// above it the budget decides and the floor is moot. Thirty spaces is polled
// every 3s, a hundred every 10s, a thousand every 100s.
//
// This raises an interval and never lowers one. Somebody who asked for a minute
// gets a minute whatever the arithmetic says, because a budget is a ceiling on
// the rate and not an instruction to spend it.
func IntervalForSpaces(requested time.Duration, spaces int) time.Duration {
	if requested == 0 {
		requested = DefaultPollInterval
	}
	if requested < MinPollInterval {
		requested = MinPollInterval
	}
	if spaces < 1 {
		return requested
	}

	needed := time.Duration(spaces) * time.Second / PollBudget
	if needed > requested {
		return needed
	}
	return requested
}

// WatchManyRequest asks to follow what happens in several spaces at once.
type WatchManyRequest struct {
	// Spaces is what to watch, taken once. A space created after this starts is
	// not picked up: re-listing costs a request of its own on the same quota
	// this is being careful with, and a watch whose subject changes underneath
	// it is harder to reason about than one whose subject does not. The command
	// says so at startup rather than leaving it to be discovered.
	Spaces []string

	Types    []string
	Filter   string
	Interval time.Duration
	Since    time.Time

	// OnDropped is called when a space stops being watched, with the reason,
	// and the walk carries on without it.
	//
	// A callback because internal/chat may not write to a stream, and a
	// stream-ending error because one space out of forty went away is the
	// behaviour this exists to avoid. May be nil.
	OnDropped func(space string, err error)
}

// WatchMany follows several spaces at once, yielding each event as it arrives.
//
// One space is polled at a time, every IntervalForSpaces divided by the space
// count, round robin. That is what makes the rate constant rather than merely
// correct on average: a poller per space, all firing on the same tick, would
// make the whole count of requests in one instant and then nothing for an
// interval, which is the same number a minute and a burst that arrives at
// Google as a burst.
//
// It also means no goroutines, no channel to drain on cancellation, and a
// schedule a test can read straight off the recorded waits.
func (c *Client) WatchMany(ctx context.Context, req WatchManyRequest) iter.Seq2[SpaceEvent, error] {
	return func(yield func(SpaceEvent, error) bool) {
		if len(req.Spaces) == 0 {
			yield(SpaceEvent{}, clientErr("there is nothing to watch: no space was named and none was found."))
			return
		}
		for _, space := range req.Spaces {
			if err := CheckSpaceName(space); err != nil {
				yield(SpaceEvent{}, err)
				return
			}
		}
		if err := CheckInterval(req.Interval); err != nil {
			yield(SpaceEvent{}, err)
			return
		}
		if _, err := eventFilter(SpaceEventsRequest{Types: req.Types, Filter: req.Filter}); err != nil {
			yield(SpaceEvent{}, err)
			return
		}

		c.rotate(ctx, req, yield)
	}
}

// watched is one space's place in the rotation.
type watched struct {
	space string
	since time.Time
}

// tickFor is the gap between one request and the next, rather than between one
// poll of a given space and the next. Each space still comes round every
// IntervalForSpaces, because there are that many of them in the rotation.
//
// A function rather than a line in rotate because it is computed twice: once at
// the start and again whenever a space leaves. Two copies of this arithmetic
// would be two chances for the pace after a drop to disagree with the pace
// before it, which is the bug it was written for.
//
// spaces has to be at least one. Both callers are inside a loop whose condition
// is that the rotation is not empty.
func tickFor(requested time.Duration, spaces int) time.Duration {
	return IntervalForSpaces(requested, spaces) / time.Duration(spaces)
}

// rotate is the poll loop, split from WatchMany for the complexity ceiling.
func (c *Client) rotate(ctx context.Context, req WatchManyRequest, yield func(SpaceEvent, error) bool) {
	start := req.Since
	if start.IsZero() {
		start = c.now()
	}

	live := make([]watched, 0, len(req.Spaces))
	for _, space := range req.Spaces {
		live = append(live, watched{space: space, since: start})
	}

	tick := tickFor(req.Interval, len(live))

	var stop bool
	for at := 0; len(live) > 0; {
		if at >= len(live) {
			at = 0
		}
		if err := c.sleep(ctx, tick); err != nil {
			return
		}

		count := len(live)
		if live, at, stop = c.step(ctx, req, live, at, yield); stop {
			return
		}

		// A space left the rotation, so the cycle is shorter and the gap has to
		// grow to match. Without this the pace stayed the one it had when the
		// rotation was full, and every survivor was polled faster than anybody
		// asked for: eight spaces at --interval 4s tick every 500ms, and after
		// seven of them are dropped the last one is polled every 500ms rather
		// than every 4s, which is four times under the floor.
		//
		// Per-space quota is shared with every other app acting in that space,
		// so the cost of that lands on everybody in the one space still being
		// watched. It is the harm MinPollInterval exists to prevent, produced by
		// the rotation that spends the budget rather than by anybody asking for
		// it.
		//
		// Recomputing can only lengthen the gap. IntervalForSpaces never returns
		// less than what was asked for, and dividing it by a smaller count gives
		// a larger tick, so no poll is brought forward by this.
		if len(live) > 0 && len(live) != count {
			tick = tickFor(req.Interval, len(live))
		}
	}

	// Every space went away. Ending at zero here would report a watch that
	// watched nothing as a clean run, which is the truncation rule wearing
	// another costume: a caller checking the exit code must never be told a
	// walk finished when it was abandoned.
	yield(SpaceEvent{}, ErrTruncated)
}

// step polls one space and answers where the rotation goes next.
//
// It returns the rotation to carry on with, the index to poll after this one,
// and whether the walk is over. Split from rotate for the complexity ceiling,
// and it earns the split: the three ways a poll can end need three different
// answers and reading them beside the scheduling made both harder.
func (c *Client) step(ctx context.Context, req WatchManyRequest, live []watched, at int,
	yield func(SpaceEvent, error) bool,
) ([]watched, int, bool) {
	newest, pollErr, keepGoing := c.pollSpace(ctx, req, live[at], yield)
	switch {
	case !keepGoing:
		// The consumer stopped ranging, which ends everything.
		return live, at, true

	case pollErr == nil:
		live[at].since = newest
		return live, at + 1, false

	case ctx.Err() != nil:
		return live, at, true

	case !permanent(pollErr):
		// The request layer has already exhausted its retries. A far end that
		// is briefly unwell must not cost somebody a space for the rest of a
		// run that may last days, so this one keeps its place and comes round
		// again.
		return live, at + 1, false
	}

	if req.OnDropped != nil {
		req.OnDropped(live[at].space, pollErr)
	}
	// Removed rather than skipped, and the index deliberately not advanced: the
	// next space has shifted into it.
	return append(live[:at], live[at+1:]...), at, false
}

// pollSpace fetches one space's events and hands any failure back to the
// rotation rather than to the consumer.
//
// That is the whole difference from pollEvents, which yields a failure straight
// through and ends the walk. Here one space failing is a fact about that space,
// and the caller decides whether it ends anything.
//
// The third return says whether to carry on at all, which is false only when
// the consumer stopped ranging.
func (c *Client) pollSpace(ctx context.Context, req WatchManyRequest, w watched,
	yield func(SpaceEvent, error) bool,
) (time.Time, error, bool) {
	newest := w.since

	for event, err := range c.SpaceEvents(ctx, SpaceEventsRequest{
		Space:  w.space,
		Types:  req.Types,
		Filter: req.Filter,
		Since:  w.since,
	}) {
		if err != nil {
			return newest, err, true
		}
		if !yield(event, nil) {
			return newest, nil, false
		}
		if at, parseErr := time.Parse(time.RFC3339Nano, event.EventTime); parseErr == nil && at.After(newest) {
			newest = at
		}
	}
	return newest, nil, true
}

// permanent reports whether polling this space again could ever answer
// differently.
//
// A space that was deleted and one this account was never allowed to read both
// answer the same way forever, and asking again spends quota to be told so.
// Everything else is treated as temporary, because the cost of being wrong runs
// the other way: dropping a space over a blip loses events silently, and
// keeping one that is truly gone costs one request per rotation and says so
// every time.
func permanent(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrPermission)
}
