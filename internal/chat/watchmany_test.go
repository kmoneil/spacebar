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
	"maps"
	"net/http"
	"slices"
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

	// The clock advances by whatever the loop waited, so a test can read the
	// simulated time of a request by calling client.now() inside its handler.
	//
	// It used to be frozen, which was enough while one wait preceded one
	// request and the two lists zipped. A rotation where a quiet space gives up
	// its turn breaks that: there are more waits than requests, and pairing them
	// by index understates every gap, which is the direction that hides the bug
	// a schedule test is looking for.
	var elapsed time.Duration
	r.client.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return tailAt.Add(elapsed)
	}

	// Stopping between polls rather than during one, for the reason tailer
	// does it: cancelling mid-response sends the retry machinery through this
	// same seam and mixes backoff waits into the recorded schedule.
	r.client.sleep = func(ctx context.Context, d time.Duration) error {
		recorded.add(d)
		mu.Lock()
		elapsed += d
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

// schedule is which space was polled and when, on the simulated clock.
type schedule struct {
	mu   sync.Mutex
	now  func() time.Time
	when map[string][]time.Duration
}

func newSchedule() *schedule { return &schedule{when: map[string][]time.Duration{}} }

// driveWith gives the recorder the client's clock, which cannot be passed to
// newSchedule because the handler that stamps requests is built before the
// client that answers them exists.
//
// Under the mutex rather than by plain assignment, so that the handler
// goroutine reading it and the test goroutine writing it are ordered by
// something the race detector can see, rather than by an argument about
// net/http's internals.
func (s *schedule) driveWith(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// record stamps one request on the simulated clock, so a gap is what the loop
// believes it waited rather than how long the test took to run.
func (s *schedule) record(r *http.Request) string {
	space := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), "/spaceEvents")

	s.mu.Lock()
	defer s.mu.Unlock()
	s.when[space] = append(s.when[space], s.now().Sub(tailAt))
	return space
}

// gaps is how long each space waited between one poll and the next.
func (s *schedule) gaps(space string) []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	at := s.when[space]
	out := make([]time.Duration, 0, max(len(at)-1, 0))
	for i := 1; i < len(at); i++ {
		out = append(out, at[i]-at[i-1])
	}
	return out
}

// spaces is every space that was polled at least once.
func (s *schedule) spaces() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(maps.Keys(s.when))
}

// requestsIn is the most requests that landed in any window of the given
// length, across every space, which is the process-wide rate the budget bounds.
func (s *schedule) requestsIn(window time.Duration) int {
	s.mu.Lock()
	all := make([]time.Duration, 0, len(s.when)*4)
	for _, at := range s.when {
		all = append(all, at...)
	}
	s.mu.Unlock()

	slices.Sort(all)
	most := 0
	for i, start := range all {
		count := 0
		for j := i; j < len(all) && all[j]-start < window; j++ {
			count++
		}
		most = max(most, count)
	}
	return most
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

			// One request per gap at most, which since a resting space gives
			// up its turn is an upper bound rather than the rate itself: the
			// waits are every turn and only some turns make a request.
			// Bounding from above is the safe direction for a budget, and
			// TestARestingRotationStaysUnderTheBudget measures the real rate
			// off the requests themselves.
			//
			// A gap of zero would be an unbounded loop and is the failure this
			// is really watching for.
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

// TestDroppingASpaceDoesNotSpeedUpTheRotation.
//
// The gap between one request and the next is the interval over the space
// count, so each space still comes round once an interval. That arithmetic is
// only true while the count is what it was: every space that leaves the
// rotation makes the cycle shorter, and a gap sized for the original count then
// polls the survivors faster than anybody asked for.
//
// The floor is the part that matters. Per-space quota is shared with every
// other app acting in that space, so a watch that has lost most of its spaces
// degrades the one it has left for everybody in it, and MinPollInterval exists
// to make that impossible.
//
// Measured off the recorded schedule rather than by timing anything, like every
// other assertion here. One wait precedes one request, which is what the
// harness is arranged for, so the two lists zip.
func TestDroppingASpaceDoesNotSpeedUpTheRotation(t *testing.T) {
	const spaces = 8
	const survivor = "spaces/AAAATestSpace000"
	const asked = 4 * time.Second

	var mu sync.Mutex
	var polled []string
	client, recorded, ctx := watcher(t, 60, func(_ int, w http.ResponseWriter, req *http.Request) {
		space := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/"), "/spaceEvents")

		mu.Lock()
		polled = append(polled, space)
		mu.Unlock()

		if space != survivor {
			// Gone for good, so the rotation drops it. Seven of the eight.
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"gone"}}`)
			return
		}
		noEvents(w)
	})

	for range client.WatchMany(ctx, WatchManyRequest{
		Types:    watchTypes(),
		Spaces:   spaceList(spaces),
		Interval: asked,
	}) {
	}

	mu.Lock()
	defer mu.Unlock()

	waited := recorded.all()
	if len(waited) < len(polled) {
		t.Fatalf("%d waits for %d requests, so the two do not pair", len(waited), len(polled))
	}

	// The simulated clock at each request, which is every wait up to and
	// including the one before it.
	at := make([]time.Duration, len(polled))
	var elapsed time.Duration
	for i := range polled {
		elapsed += waited[i]
		at[i] = elapsed
	}

	var last time.Duration = -1
	gaps := 0
	for i, space := range polled {
		if space != survivor {
			continue
		}
		if last >= 0 {
			gaps++
			gap := at[i] - last
			if gap < MinPollInterval {
				t.Errorf("the surviving space was polled again after %s, under the %s floor.\n"+
					"Seven of eight spaces were dropped and the gap between requests was never "+
					"recomputed, so the rotation kept the pace it had when it was eight.",
					gap, MinPollInterval)
			}
			if gap < asked {
				t.Errorf("the surviving space was polled again after %s, and %s was asked for",
					gap, asked)
			}
		}
		last = at[i]
	}

	if gaps < 2 {
		t.Fatalf("the surviving space came round %d times, so there is no interval to check", gaps+1)
	}

	// The process-wide rate, which is the other thing the budget bounds and
	// which a shrinking rotation could break in the same way. Recomputing can
	// only lengthen a gap, so this should never fire; it is here because that
	// is an argument and this is a measurement.
	fastest := time.Second / PollBudget
	for i, gap := range waited {
		if gap < fastest {
			t.Errorf("wait %d was %s, which is more than %d requests a second", i, gap, PollBudget)
		}
	}

	// And it ends up at the pace a watch of one space would have had all along.
	// A rotation that shrank to one and kept any other pace is one that
	// remembers a count it no longer has.
	if want := tickFor(asked, 1); waited[len(waited)-1] != want {
		t.Errorf("the last wait was %s, and a watch of one space waits %s", waited[len(waited)-1], want)
	}
}

// TestEachSpaceInARotationYieldsItsEventOnce.
//
// The single-space path's boundary defence, asserted again on the path that has
// its own copy of the poll loop. It is a separate copy for a good reason, that a
// failure in one space of many must not end the run, and a separate copy is
// exactly where a fix applied once goes missing.
//
// Run under the inclusive rule only, because that is the one that breaks
// anything: sec-12 could not measure which rule the endpoint follows, and the
// single-space tests cover both.
//
// Each space's set is its own, and the three events are deliberately at three
// different instants, which is what makes that half assertable. One set shared
// across the rotation passes a test where every space's event lands on the same
// instant, and fails this one: it is emptied by whichever space last had an
// event, so the next space round finds its own boundary event unrecognised.
func TestEachSpaceInARotationYieldsItsEventOnce(t *testing.T) {
	const spaces = 3

	when := func(space string) time.Time {
		return tailAt.Add(time.Duration(len(space)+int(space[len(space)-1])) * time.Second)
	}

	client, _, ctx := watcher(t, spaces*4, func(_ int, w http.ResponseWriter, req *http.Request) {
		space := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/"), "/spaceEvents")
		at := when(space)

		since, bounded := startTimeOf(t, req.URL.String())
		if bounded && at.Before(since) {
			noEvents(w)
			return
		}
		_, _ = fmt.Fprintf(w, `{"spaceEvents": [
			{"name": %q, "eventTime": %q, "eventType": %q,
			 "messageCreatedEventData": {"message": {"name": %q}}}]}`,
			space+"/spaceEvents/E", wireTime(at), EventMessageCreated, space+"/messages/M")
	})

	seen := map[string]int{}
	for event, err := range client.WatchMany(ctx, WatchManyRequest{
		Types:    watchTypes(),
		Spaces:   spaceList(spaces),
		Interval: MinPollInterval,
	}) {
		if err != nil {
			t.Fatalf("WatchMany: %v", err)
		}
		seen[event.Name]++
	}

	if len(seen) != spaces {
		t.Fatalf("%d spaces yielded %d distinct events: %v", spaces, len(seen), seen)
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("%s was yielded %d times across four passes, want 1", name, count)
		}
	}
}

// TestAQuietSpaceInARotationBacksOffTheWayASingleWatchDoes, which is what this
// whole change is for.
//
// `tail` and `watch SPACE` share follow, which doubles the interval after five
// polls with nothing new. `watch --all` is a second copy of the poll loop and
// had none of it, so three quiet spaces at the default were polled every five
// seconds each, forever, where watching any one of them alone settles at one
// request a minute. Twelve times the requests, on a per-space quota that is
// shared with every other app acting in that space.
//
// The assertion is against what follow would have done from the same interval,
// rather than against a sequence written out here, because the claim is that
// the two paths treat a quiet space the same way and not that this one happens
// to produce some numbers.
//
// Six seconds over three spaces so that every gap is exact. Five over three is
// 1.666s a turn and the arithmetic would be off by nanoseconds, which says
// nothing about the schedule and everything about integer division.
func TestAQuietSpaceInARotationBacksOffTheWayASingleWatchDoes(t *testing.T) {
	const spaces = 3
	const asked = 6 * time.Second

	stamps := newSchedule()
	client, _, ctx := watcher(t, 30, func(_ int, w http.ResponseWriter, req *http.Request) {
		stamps.record(req)
		noEvents(w)
	})
	stamps.driveWith(client.now)

	for range client.WatchMany(ctx, WatchManyRequest{
		Types:    watchTypes(),
		Spaces:   spaceList(spaces),
		Interval: asked,
	}) {
		t.Error("an empty space yielded an event")
	}

	// The nth gap follows the nth quiet poll, so it is backoff at that count.
	want := make([]time.Duration, 0, 8)
	for quiet := 1; len(want) < cap(want); quiet++ {
		want = append(want, client.backoff(asked, quiet))
	}

	for _, space := range stamps.spaces() {
		got := stamps.gaps(space)
		if len(got) < len(want) {
			t.Fatalf("%s was polled %d times, too few to read a backoff off", space, len(got)+1)
		}
		if !slices.Equal(got[:len(want)], want) {
			t.Errorf("%s was polled with gaps %v.\nWant %v, which is what a watch of that space "+
				"on its own would have done.", space, got[:len(want)], want)
		}
	}
}

// TestABusySpaceKeepsItsPaceWhileTheQuietOnesRest.
//
// The other half, and the one a backoff breaks if it is written as a property
// of the rotation rather than of each space. A space that is saying something
// must go on being polled at the interval that was asked for while its
// neighbours are resting, and no space may be polled faster than that whatever
// the others are doing.
func TestABusySpaceKeepsItsPaceWhileTheQuietOnesRest(t *testing.T) {
	const spaces = 4
	const asked = 8 * time.Second
	busy := spaceList(spaces)[0]

	var mu sync.Mutex
	events := 0

	stamps := newSchedule()
	client, _, ctx := watcher(t, 40, func(_ int, w http.ResponseWriter, req *http.Request) {
		if stamps.record(req) != busy {
			noEvents(w)
			return
		}

		// A new event on every poll, later than the last, so this space is
		// never a quiet one.
		mu.Lock()
		events++
		n := events
		mu.Unlock()

		_, _ = fmt.Fprintf(w, `{"spaceEvents": [
			{"name": %q, "eventTime": %q, "eventType": %q,
			 "messageCreatedEventData": {"message": {"name": %q}}}]}`,
			fmt.Sprintf("%s/spaceEvents/E%d", busy, n), wireTime(tailAt.Add(time.Duration(n)*time.Second)),
			EventMessageCreated, fmt.Sprintf("%s/messages/M%d", busy, n))
	})
	stamps.driveWith(client.now)

	for _, err := range client.WatchMany(ctx, WatchManyRequest{
		Types:    watchTypes(),
		Spaces:   spaceList(spaces),
		Interval: asked,
	}) {
		if err != nil {
			t.Fatalf("WatchMany: %v", err)
		}
	}

	floor := IntervalForSpaces(asked, spaces)
	for _, space := range stamps.spaces() {
		for i, gap := range stamps.gaps(space) {
			if gap < floor {
				t.Errorf("%s was polled again after %s, under the %s its interval allows (gap %d).",
					space, gap, floor, i)
			}
		}
	}

	for i, gap := range stamps.gaps(busy) {
		if gap != floor {
			t.Errorf("the busy space waited %s before poll %d, want the %s it asked for.\n"+
				"A space with something to say must not be slowed down by its neighbours resting.",
				gap, i+2, floor)
		}
	}
}

// TestARestingRotationStaysUnderTheBudget.
//
// Skipping a turn can only remove requests, so this cannot fail by arithmetic.
// It can fail by design, which is what it is here for: a rotation that gave a
// skipped turn to the next space, or that slept until the earliest space was
// due, would make the same number of requests in a shorter window and arrive at
// Google as a burst.
//
// Counted in a one-second window across every space, which is the unit
// PollBudget is expressed in.
func TestARestingRotationStaysUnderTheBudget(t *testing.T) {
	for _, spaces := range []int{3, 30, 250} {
		t.Run(fmt.Sprintf("%d spaces", spaces), func(t *testing.T) {
			stamps := newSchedule()
			client, _, ctx := watcher(t, 60, func(_ int, w http.ResponseWriter, req *http.Request) {
				stamps.record(req)
				noEvents(w)
			})
			stamps.driveWith(client.now)

			for range client.WatchMany(ctx, WatchManyRequest{
				Types:    watchTypes(),
				Spaces:   spaceList(spaces),
				Interval: MinPollInterval,
			}) {
				t.Error("an empty space yielded an event")
			}

			if most := stamps.requestsIn(time.Second); most > PollBudget {
				t.Errorf("%d requests landed inside one second, over the budget of %d.",
					most, PollBudget)
			}
		})
	}
}

// TestASpaceThatSpeaksAgainGoesBackToTheRequestedPace.
//
// The half somebody would forget, and the half that makes a watch worth
// running: a space that has been quiet all night must be responsive the moment
// it says something, not an hour later. tail's own backoff test asserts the
// same reset for the same reason.
func TestASpaceThatSpeaksAgainGoesBackToTheRequestedPace(t *testing.T) {
	const spaces = 3
	const asked = 6 * time.Second
	const speaksOn = 8

	talker := spaceList(spaces)[0]

	var mu sync.Mutex
	polls := 0

	stamps := newSchedule()
	client, _, ctx := watcher(t, 40, func(_ int, w http.ResponseWriter, req *http.Request) {
		if stamps.record(req) != talker {
			noEvents(w)
			return
		}

		mu.Lock()
		polls++
		n := polls
		mu.Unlock()

		// Quiet long enough to be resting, and then one event.
		if n != speaksOn {
			noEvents(w)
			return
		}
		_, _ = fmt.Fprintf(w, `{"spaceEvents": [
			{"name": %q, "eventTime": %q, "eventType": %q,
			 "messageCreatedEventData": {"message": {"name": %q}}}]}`,
			talker+"/spaceEvents/E", wireTime(tailAt.Add(time.Minute)),
			EventMessageCreated, talker+"/messages/M")
	})
	stamps.driveWith(client.now)

	for _, err := range client.WatchMany(ctx, WatchManyRequest{
		Types:    watchTypes(),
		Spaces:   spaceList(spaces),
		Interval: asked,
	}) {
		if err != nil {
			t.Fatalf("WatchMany: %v", err)
		}
	}

	gaps := stamps.gaps(talker)
	if len(gaps) < speaksOn {
		t.Fatalf("the talking space was polled %d times, too few to see it reset", len(gaps)+1)
	}

	// The gap into the poll that spoke is a long one, because the space had
	// been quiet for seven polls by then.
	if before := gaps[speaksOn-2]; before <= asked {
		t.Fatalf("the gap before the event was %s, so the space was never resting "+
			"and this proves nothing about the reset", before)
	}
	if after := gaps[speaksOn-1]; after != asked {
		t.Errorf("the space waited %s after saying something, want the %s that was asked for.",
			after, asked)
	}
}

// rotationEventsAt is eventsAt for a rotation: one event per space, answered
// according to the start_time asked for and the boundary rule under test.
//
// Separate from eventsAt because that one names a fixed space, and here the
// point is that each space has its own watermark and its own remembered set.
func rotationEventsAt(t *testing.T, b boundary, at time.Time) func(int, http.ResponseWriter, *http.Request) {
	t.Helper()

	return func(_ int, w http.ResponseWriter, r *http.Request) {
		space := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), "/spaceEvents")
		since, bounded := startTimeOf(t, r.URL.String())

		switch {
		case !bounded, at.After(since):
		case bool(b) && at.Equal(since):
		default:
			noEvents(w)
			return
		}
		_, _ = fmt.Fprintf(w, `{"spaceEvents": [
			{"name": %q, "eventTime": %q, "eventType": %q,
			 "messageCreatedEventData": {"message": {"name": %q}}}]}`,
			space+"/spaceEvents/E", wireTime(at), EventMessageCreated, space+"/messages/M")
	}
}

// TestARotationWithNothingNewGoesQuietWhicheverWayTheBoundaryGoes, which is
// TestAWatchWithNothingNewGoesQuietWhicheverWayTheBoundaryGoes for the other
// copy of the loop.
//
// It is the test that holds pollSpace's found to counting what was yielded
// rather than what arrived. Under a start_time that includes the instant, the
// event that set the watermark comes back on every poll and is recognised by
// the space's own seen set, so it is not yielded. Counting it anyway would hold
// quiet at zero, the backoff would never engage, and a space where nobody has
// spoken since yesterday would be polled at the base interval for as long as
// the command ran.
//
// Both rules, because the real one is a measurement of somebody else's API on
// one day, and a watch that is only correct under the reading that was measured
// is one Google can break without telling anybody.
func TestARotationWithNothingNewGoesQuietWhicheverWayTheBoundaryGoes(t *testing.T) {
	const spaces = 3
	const asked = 6 * time.Second

	for _, b := range []boundary{inclusive, exclusive} {
		t.Run(b.String(), func(t *testing.T) {
			stamps := newSchedule()
			answer := rotationEventsAt(t, b, tailAt.Add(time.Minute))
			client, _, ctx := watcher(t, spaces*9, func(poll int, w http.ResponseWriter, req *http.Request) {
				stamps.record(req)
				answer(poll, w, req)
			})
			stamps.driveWith(client.now)

			for _, err := range client.WatchMany(ctx, WatchManyRequest{
				Types:    watchTypes(),
				Spaces:   spaceList(spaces),
				Interval: asked,
			}) {
				if err != nil {
					t.Fatalf("WatchMany: %v", err)
				}
			}

			for _, space := range stamps.spaces() {
				gaps := stamps.gaps(space)
				if len(gaps) < 7 {
					t.Fatalf("%s was polled %d times, too few to show a backoff", space, len(gaps)+1)
				}
				if last := gaps[len(gaps)-1]; last <= asked {
					t.Errorf("%s waited %s before its last poll, so nine polls of a space "+
						"holding one old event never backed off.", space, last)
				}
			}
		})
	}
}
