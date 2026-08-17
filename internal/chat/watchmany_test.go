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
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// watchTypes is the default event selection, which every request needs: the
// endpoint answers 400 without one and the client refuses before it asks.
func watchTypes() []string {
	types, err := EventTypesFor(DefaultEventGroups)
	if err != nil {
		panic(err)
	}
	return types
}

// spaceList builds n distinct space names.
func spaceList(n int) []string {
	names := make([]string, 0, n)
	for i := range n {
		names = append(names, fmt.Sprintf("spaces/AAAATestSpace%03d", i))
	}
	return names
}

// watcher builds a client whose polls the test answers and whose waits it
// records, stopping after the given number of requests.
//
// The same shape as tailer, and separate from it because the thing under test
// here is the schedule across spaces rather than the backoff within one. The
// handler is given the requested path so a test can answer per space.
func watcher(t *testing.T, stop int, handler func(poll int, w http.ResponseWriter, r *http.Request)) (*Client, *waits, context.Context) {
	t.Helper()

	var polls int
	var mu sync.Mutex
	recorded := &waits{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	r := newReader(t, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		polls++
		n := polls
		mu.Unlock()
		handler(n, w, req)
	})
	r.client.now = func() time.Time { return tailAt }

	// Stopping between polls rather than during one, for the reason tailer
	// does it: cancelling mid-response sends the retry machinery through this
	// same seam and mixes backoff waits into the recorded schedule.
	r.client.sleep = func(ctx context.Context, d time.Duration) error {
		recorded.add(d)
		mu.Lock()
		done := polls >= stop
		mu.Unlock()
		if done {
			cancel()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	return r.client, recorded, ctx
}

func noEvents(w http.ResponseWriter) { _, _ = fmt.Fprint(w, `{"spaceEvents":[]}`) }

// TestTheRequestRateNeverExceedsTheBudgetWhateverTheSpaceCount, which is the
// card's first falsifiable claim, asserted by the schedule the loop asked for
// rather than by timing it.
//
// The rate is what matters and not the interval, so the assertion is the one
// the quota page implies: requests in any one second, worked out from the waits
// the loop asked between them. PollBudget is a fifth of the fifty a second the
// project allows, and every count below is against that fifth.
func TestTheRequestRateNeverExceedsTheBudgetWhateverTheSpaceCount(t *testing.T) {
	for _, spaces := range []int{1, 2, 30, 99, 100, 250, 1000} {
		t.Run(fmt.Sprintf("%d spaces", spaces), func(t *testing.T) {
			client, recorded, ctx := watcher(t, 40, func(_ int, w http.ResponseWriter, _ *http.Request) {
				noEvents(w)
			})

			for range client.WatchMany(ctx, WatchManyRequest{
				Types:    watchTypes(),
				Spaces:   spaceList(spaces),
				Interval: MinPollInterval,
			}) {
				t.Error("an empty space yielded an event")
			}

			waited := recorded.all()
			if len(waited) < 2 {
				t.Fatalf("the loop made %d waits, so there is no schedule to check", len(waited))
			}

			// Every gap is the same by construction, so the rate is one request
			// per gap. A gap of zero would be an unbounded loop and is the
			// failure this is really watching for.
			for i, gap := range waited {
				if gap <= 0 {
					t.Fatalf("wait %d was %s, which is a poll loop with no pause in it", i, gap)
				}
				rate := float64(time.Second) / float64(gap)
				if rate > PollBudget+0.001 {
					t.Errorf("wait %d was %s, which is %.2f requests a second against a budget of %d",
						i, gap, rate, PollBudget)
				}
			}
		})
	}
}

// TestEachSpaceComesRoundOnceAnInterval.
//
// The budget bounds the rate, and this is the other half: the rate is spread
// across the spaces rather than spent on the first one. A rotation that polled
// space zero forty times would satisfy the rate assertion above and be useless.
func TestEachSpaceComesRoundOnceAnInterval(t *testing.T) {
	const spaces = 8

	var mu sync.Mutex
	order := []string{}
	client, _, ctx := watcher(t, spaces*2, func(_ int, w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		order = append(order, strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/"), "/spaceEvents"))
		mu.Unlock()
		noEvents(w)
	})

	for range client.WatchMany(ctx, WatchManyRequest{
		Types:    watchTypes(),
		Spaces:   spaceList(spaces),
		Interval: MinPollInterval,
	}) {
		t.Error("an empty space yielded an event")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) < spaces {
		t.Fatalf("only %d requests were made across %d spaces", len(order), spaces)
	}

	// The first pass round has to touch every space exactly once. Anything else
	// is a rotation that favours one.
	seen := map[string]int{}
	for _, space := range order[:spaces] {
		seen[space]++
	}
	if len(seen) != spaces {
		t.Errorf("the first %d requests reached %d distinct spaces, not %d:\n%v",
			spaces, len(seen), spaces, order[:spaces])
	}
}

// TestTheIntervalIsRaisedByTheBudgetAndNeverLowered.
//
// The arithmetic on its own, because the loop above cannot show the boundary
// and the boundary is the whole claim: twenty spaces is where the budget starts
// deciding the pace, and below it the 2s floor does.
func TestTheIntervalIsRaisedByTheBudgetAndNeverLowered(t *testing.T) {
	cases := []struct {
		requested time.Duration
		spaces    int
		want      time.Duration
	}{
		// Below the crossover the floor wins and nothing is slowed down, which
		// is the case the card expected to be dangerous.
		{MinPollInterval, 1, MinPollInterval},
		{MinPollInterval, 19, MinPollInterval},

		// Twenty spaces at ten a second is exactly the 2s floor, so this is the
		// crossover: below it the floor decides the pace and the budget is not
		// reached, and above it the budget decides and the floor is moot.
		{MinPollInterval, 20, MinPollInterval},
		{MinPollInterval, 30, 3 * time.Second},
		{MinPollInterval, 100, 10 * time.Second},
		{MinPollInterval, 250, 25 * time.Second},

		// An interval somebody asked for is never shortened to spend the
		// budget. A budget is a ceiling on the rate, not an instruction.
		{time.Hour, 1, time.Hour},
		{time.Hour, 1000, time.Hour},

		// Zero means the default, and the default is still subject to both.
		{0, 1, DefaultPollInterval},
	}

	for _, tc := range cases {
		got := IntervalForSpaces(tc.requested, tc.spaces)
		if got != tc.want {
			t.Errorf("IntervalForSpaces(%s, %d) = %s, want %s", tc.requested, tc.spaces, got, tc.want)
		}
		if got < tc.requested {
			t.Errorf("IntervalForSpaces(%s, %d) = %s, which is faster than was asked for",
				tc.requested, tc.spaces, got)
		}
	}
}

// TestASpaceThatCannotBeReadDoesNotStopTheOthers, the card's third falsifiable
// claim.
//
// A 403 on one space is a fact about that space. Ending the walk would mean one
// room somebody was removed from silently stops a watch over thirty-nine
// others, and the events that were missed are gone.
func TestASpaceThatCannotBeReadDoesNotStopTheOthers(t *testing.T) {
	const forbidden = "spaces/AAAATestSpace001"

	client, _, ctx := watcher(t, 12, func(_ int, w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, forbidden) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"no"}}`)
			return
		}
		noEvents(w)
	})

	var dropped []string
	for _, err := range client.WatchMany(ctx, WatchManyRequest{
		Types:    watchTypes(),
		Spaces:   spaceList(4),
		Interval: MinPollInterval,
		OnDropped: func(space string, _ error) {
			dropped = append(dropped, space)
		},
	}) {
		if err != nil {
			t.Fatalf("one space failing ended the whole walk: %v", err)
		}
	}

	if len(dropped) != 1 || dropped[0] != forbidden {
		t.Errorf("dropped = %v, want exactly %s", dropped, forbidden)
	}
}

// TestAWatchWithNothingLeftToWatchSaysSo.
//
// Every space went away, so there is nothing being watched and the process is
// sitting in a loop that will never yield again. Reporting that as a clean run
// is the truncation rule wearing another costume: a caller checking the exit
// code must never be told a walk finished when it was abandoned.
func TestAWatchWithNothingLeftToWatchSaysSo(t *testing.T) {
	client, _, ctx := watcher(t, 20, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"gone"}}`)
	})

	var reported error
	for _, err := range client.WatchMany(ctx, WatchManyRequest{
		Types:    watchTypes(),
		Spaces:   spaceList(3),
		Interval: MinPollInterval,
	}) {
		if err != nil {
			reported = err
		}
	}

	if reported == nil {
		t.Fatal("every space was dropped and the walk ended without saying so")
	}
	if !strings.Contains(reported.Error(), "incomplete") {
		t.Errorf("the failure was %v, and it should say the result is incomplete", reported)
	}
}

// TestATransientFailureDoesNotCostASpaceForTheRestOfTheRun.
//
// The other side of the dropping rule. A 503 has already been retried by the
// request layer, and a far end that is briefly unwell must not cost somebody a
// space for a run that may last days.
func TestATransientFailureDoesNotCostASpaceForTheRestOfTheRun(t *testing.T) {
	const flaky = "spaces/AAAATestSpace001"

	var mu sync.Mutex
	reached := 0
	client, _, ctx := watcher(t, 30, func(_ int, w http.ResponseWriter, req *http.Request) {
		if !strings.Contains(req.URL.Path, flaky) {
			noEvents(w)
			return
		}
		mu.Lock()
		reached++
		first := reached <= 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"error":{"code":503,"status":"UNAVAILABLE","message":"later"}}`)
			return
		}
		noEvents(w)
	})

	var dropped []string
	for range client.WatchMany(ctx, WatchManyRequest{
		Types:    watchTypes(),
		Spaces:   spaceList(3),
		Interval: MinPollInterval,
		OnDropped: func(space string, _ error) {
			dropped = append(dropped, space)
		},
	}) {
	}

	if len(dropped) != 0 {
		t.Errorf("a temporary failure cost the space its place in the rotation: %v", dropped)
	}

	mu.Lock()
	defer mu.Unlock()
	if reached < 2 {
		t.Errorf("%s was polled %d times, so it never came round again", flaky, reached)
	}
}
