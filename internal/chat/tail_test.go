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

	"github.com/kmoneil/spacebar/internal/output"
)

// tailAt is the clock every test here runs on. Nothing waits: the sleep seam
// records what it was asked for and returns immediately, so a test that asserts
// a sixty-second backoff takes no longer than one that asserts two seconds.
var tailAt = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

// waits records what a tail asked to sleep for, which is how the interval floor
// and the backoff are asserted: by the spacing that was requested, rather than
// by a stopwatch.
type waits struct {
	mu   sync.Mutex
	list []time.Duration
}

func (w *waits) add(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.list = append(w.list, d)
}

func (w *waits) all() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.list...)
}

// tailer builds a client whose polls the test answers and whose waits it
// records. The handler is given the poll number, starting at one.
func tailer(t *testing.T, stop int, handler func(poll int, w http.ResponseWriter, r *http.Request)) (*Client, *waits, context.Context) {
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

	// Stopping happens here rather than in the handler, and the difference
	// matters. Cancelling while a response is still being read fails the
	// request, and the retry machinery then sleeps through this same seam, so
	// the recorded waits are a mixture of poll intervals and retry backoff and
	// an assertion about the floor reads a 1s retry as a floor violation. It
	// cost a diagnostic to find, and stopping between polls is also what a
	// Ctrl-C usually does.
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

func empty(w http.ResponseWriter) { _, _ = fmt.Fprint(w, `{"messages":[]}`) }

// TestAnIntervalBelowTheFloorIsRefusedAndNotRounded.
//
// SPEC.md §13's floor, and the decision the card asked for: refused rather than
// clamped. Somebody who asked for 100ms and quietly got 2s has a script whose
// timing assumptions are wrong and no way to find out, and this tool does not
// silently alter a value to make it acceptable anywhere else.
func TestAnIntervalBelowTheFloorIsRefusedAndNotRounded(t *testing.T) {
	for _, d := range []time.Duration{time.Nanosecond, 100 * time.Millisecond, MinPollInterval - time.Nanosecond} {
		t.Run(d.String(), func(t *testing.T) {
			err := CheckInterval(d)
			if err == nil {
				t.Fatalf("CheckInterval(%s) was accepted", d)
			}
			if got := output.ExitCodeOf(err); got != output.ExitUsage {
				t.Errorf("exit = %d, want %d", got, output.ExitUsage)
			}
			// The floor and the reason, because "invalid interval" would send
			// somebody looking for a syntax mistake.
			for _, want := range []string{"2s", "quota", "refused rather than rounded up"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not mention %q:\n%v", want, err)
				}
			}
		})
	}

	// The floor itself, and zero, which means "use the default", are both fine.
	for _, d := range []time.Duration{0, MinPollInterval, time.Hour} {
		if err := CheckInterval(d); err != nil {
			t.Errorf("CheckInterval(%s) = %v", d, err)
		}
	}
}

// TestATailNeverPollsFasterThanTheFloor, asserted by the spacing it asked for
// rather than by timing it.
func TestATailNeverPollsFasterThanTheFloor(t *testing.T) {
	client, recorded, ctx := tailer(t, 3, func(_ int, w http.ResponseWriter, _ *http.Request) {
		empty(w)
	})

	for range client.Tail(ctx, TailRequest{Space: "spaces/AAAATestSpace", Interval: MinPollInterval}) {
		t.Error("an empty space yielded a message")
	}

	for i, d := range recorded.all() {
		if d < MinPollInterval {
			t.Errorf("wait %d was %s, below the %s floor", i, d, MinPollInterval)
		}
	}
}

// TestFiveQuietPollsDoubleTheIntervalAndAMessageResetsIt, which is the card's
// second falsifiable claim.
func TestFiveQuietPollsDoubleTheIntervalAndAMessageResetsIt(t *testing.T) {
	client, recorded, ctx := tailer(t, 9, func(poll int, w http.ResponseWriter, _ *http.Request) {
		// Quiet until the seventh poll, which is far enough in for the backoff
		// to have started, then one message.
		if poll == 7 {
			_, _ = fmt.Fprintf(w, `{"messages":[{"name":"spaces/AAA/messages/BBB","createTime":%q,"text":"hello"}]}`,
				tailAt.Add(time.Minute).Format(time.RFC3339Nano))
			return
		}
		empty(w)
	})

	var seen int
	for message, err := range client.Tail(ctx, TailRequest{Space: "spaces/AAAATestSpace", Interval: MinPollInterval}) {
		if err != nil {
			t.Fatalf("Tail: %v", err)
		}
		if message.Text != "hello" {
			t.Errorf("message = %+v", message)
		}
		seen++
	}
	if seen != 1 {
		t.Fatalf("saw %d messages, want 1", seen)
	}

	all := recorded.all()
	if len(all) < 8 {
		t.Fatalf("only %d waits recorded", len(all))
	}

	// The first five are the base interval: the backoff starts after five
	// consecutive quiet polls, not on the fifth.
	for i := range 5 {
		if all[i] != MinPollInterval {
			t.Errorf("wait %d = %s, want the base %s", i, all[i], MinPollInterval)
		}
	}
	if all[5] <= MinPollInterval {
		t.Errorf("wait 5 = %s, want the interval to have grown", all[5])
	}

	// The message on poll 7 resets it, so the wait before poll 8 is the base
	// again. That is the half somebody would forget, and it is the half that
	// makes a busy space responsive after a quiet night.
	if all[7] != MinPollInterval {
		t.Errorf("wait 7 = %s, want the base %s after a message", all[7], MinPollInterval)
	}
}

// TestTheBackoffIsBoundedSoAQuietSpaceStaysCheapAndResponsive.
//
// Several bases rather than one. This asserted the floor and the ceiling
// against MinPollInterval alone, which is the one base where the ceiling
// question cannot come up, and the bug below lived underneath it for four
// milestones.
func TestTheBackoffIsBoundedSoAQuietSpaceStaysCheapAndResponsive(t *testing.T) {
	client := &Client{}
	for _, base := range []time.Duration{MinPollInterval, DefaultPollInterval, 30 * time.Second, MaxPollInterval, 90 * time.Second, 5 * time.Minute} {
		ceiling := max(MaxPollInterval, base)
		for quiet := range 100 {
			got := client.backoff(base, quiet)
			if got < base {
				t.Fatalf("backoff(%s, %d) = %s, below the base it was asked for", base, quiet, got)
			}
			if got > ceiling {
				t.Fatalf("backoff(%s, %d) = %s, above the %s ceiling", base, quiet, got, ceiling)
			}
		}
	}
}

// TestGoingQuietNeverPollsFasterThanWasAskedFor.
//
// The ceiling was MaxPollInterval flat, so a base above it was cut down to it
// rather than left alone. `tail --interval 5m` polled every five minutes until
// a space went quiet for five polls and then polled every minute for the rest
// of the run: five times the requests, arrived at by the space going quiet,
// which is the one thing that was supposed to make it poll less.
//
// It is reachable without anybody typing a large interval. IntervalForSpaces
// returns more than a minute above six hundred spaces, so `watch --all` on a
// large account walks into it on its own.
//
// This is the same harm CheckInterval refuses a small interval to prevent,
// coming from the other side, and it is worse in one way: a refused interval
// says so, and this one just quietly spends somebody else's quota.
func TestGoingQuietNeverPollsFasterThanWasAskedFor(t *testing.T) {
	client := &Client{}
	for _, base := range []time.Duration{MinPollInterval, 90 * time.Second, 5 * time.Minute, time.Hour} {
		for quiet := range 40 {
			if got := client.backoff(base, quiet); got < base {
				t.Fatalf("a space quiet for %d polls at --interval %s is polled every %s.\n"+
					"Backing off may only ever make the wait longer.", quiet, base, got)
			}
		}
	}
}

// TestALongIntervalSurvivesAQuietSpace is the same claim through the loop
// rather than through the arithmetic, because backoff has no caller that would
// notice if the two disagreed.
func TestALongIntervalSurvivesAQuietSpace(t *testing.T) {
	const asked = 5 * time.Minute

	client, recorded, ctx := tailer(t, 12, func(_ int, w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"messages":[]}`)
	})

	for _, err := range client.Tail(ctx, TailRequest{
		Space:    "spaces/AAAATestSpace",
		Interval: asked,
	}) {
		if err != nil {
			t.Fatalf("Tail: %v", err)
		}
	}

	all := recorded.all()
	if len(all) < 10 {
		t.Fatalf("only %d waits recorded, which is too few to reach the backoff", len(all))
	}
	for i, wait := range all {
		if wait < asked {
			t.Errorf("wait %d was %s against an --interval of %s.\n"+
				"Twelve polls of a quiet space made it poll faster than it was asked to.",
				i, wait, asked)
		}
	}
}

// TestEachPollAsksOnlyForWhatIsNewerThanTheLastOne.
//
// The watermark, and the reason this does not track message IDs. Measured
// against the live API on 2026-08-16: messages.list accepts
// filter=createTime > "...", so the server does the work and there is no
// growing seen-set to bound.
func TestEachPollAsksOnlyForWhatIsNewerThanTheLastOne(t *testing.T) {
	var mu sync.Mutex
	var filters []string

	client, _, ctx := tailer(t, 3, func(poll int, w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		filters = append(filters, r.URL.Query().Get("filter"))
		mu.Unlock()

		if poll == 1 {
			_, _ = fmt.Fprintf(w, `{"messages":[{"name":"spaces/AAA/messages/BBB","createTime":%q,"text":"one"}]}`,
				tailAt.Add(time.Hour).Format(time.RFC3339Nano))
			return
		}
		empty(w)
	})

	for _, err := range client.Tail(ctx, TailRequest{Space: "spaces/AAAATestSpace", Interval: MinPollInterval}) {
		if err != nil {
			t.Fatalf("Tail: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(filters) < 2 {
		t.Fatalf("only %d polls", len(filters))
	}

	for i, f := range filters {
		if !strings.HasPrefix(f, "createTime > ") {
			t.Errorf("poll %d filtered on %q, want a createTime watermark", i, f)
		}
	}

	// The message on the first poll moves the watermark, so the second poll
	// cannot ask for it again. Without this, every poll re-reports everything.
	if filters[0] == filters[1] {
		t.Errorf("the watermark did not advance after a message: both polls asked %q", filters[0])
	}
	if !strings.Contains(filters[1], tailAt.Add(time.Hour).Format("2006-01-02T15:04:05")) {
		t.Errorf("the second poll did not follow from the message it saw: %q", filters[1])
	}
}

// TestBackfillPrintsOldestFirstThenFollowsFromTheNewest.
//
// The one place tail buffers, bounded by --backfill. A conversation is read in
// the order it happened, and the limit has to cut from the recent end, so the
// fetch is newest-first and the printing is not.
func TestBackfillPrintsOldestFirstThenFollowsFromTheNewest(t *testing.T) {
	newest := tailAt.Add(-time.Minute)
	oldest := tailAt.Add(-time.Hour)

	var mu sync.Mutex
	var filters []string

	client, _, ctx := tailer(t, 2, func(poll int, w http.ResponseWriter, r *http.Request) {
		if poll == 1 {
			// The backfill fetch, newest first as the API returns it.
			_, _ = fmt.Fprintf(w, `{"messages":[
				{"name":"spaces/AAA/messages/new","createTime":%q,"text":"second"},
				{"name":"spaces/AAA/messages/old","createTime":%q,"text":"first"}]}`,
				newest.Format(time.RFC3339Nano), oldest.Format(time.RFC3339Nano))
			return
		}
		mu.Lock()
		filters = append(filters, r.URL.Query().Get("filter"))
		mu.Unlock()
		empty(w)
	})

	var texts []string
	for message, err := range client.Tail(ctx, TailRequest{
		Space:    "spaces/AAAATestSpace",
		Interval: MinPollInterval,
		Backfill: 2,
	}) {
		if err != nil {
			t.Fatalf("Tail: %v", err)
		}
		texts = append(texts, message.Text)
	}

	if len(texts) != 2 || texts[0] != "first" || texts[1] != "second" {
		t.Errorf("backfill printed %v, want oldest first", texts)
	}

	// Following from the newest message printed, so nothing between the
	// backfill and the first poll is missed or repeated.
	mu.Lock()
	defer mu.Unlock()
	if len(filters) == 0 {
		t.Fatal("no poll followed the backfill")
	}
	if !strings.Contains(filters[0], newest.UTC().Format("2006-01-02T15:04:05")) {
		t.Errorf("the first poll followed from %q, want the newest backfilled message", filters[0])
	}
}

// TestABadSpaceOrIntervalIsRefusedWithoutAsking, because a tail that validated
// after its first request would spend one against a shared quota to find out.
func TestABadSpaceOrIntervalIsRefusedWithoutAsking(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  TailRequest
	}{
		{"a bad space", TailRequest{Space: "spaces/../etc", Interval: MinPollInterval}},
		{"a sub-floor interval", TailRequest{Space: "spaces/AAAATestSpace", Interval: time.Millisecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReader(t, func(http.ResponseWriter, *http.Request) {
				t.Error("a refused tail reached the network")
			})

			var got error
			for _, err := range r.client.Tail(context.Background(), tc.req) {
				got = err
				break
			}
			if got == nil {
				t.Fatal("the tail was accepted")
			}
			if r.count() != 0 {
				t.Errorf("made %d requests before refusing", r.count())
			}
		})
	}
}

// TestACancelledTailEndsWithoutAnError.
//
// Ctrl-C is how this command is meant to end, so it is not a failure. A tail
// that yielded context.Canceled would exit non-zero and make every shell script
// wrapping it wrong.
func TestACancelledTailEndsWithoutAnError(t *testing.T) {
	client, _, ctx := tailer(t, 1, func(_ int, w http.ResponseWriter, _ *http.Request) {
		empty(w)
	})

	for _, err := range client.Tail(ctx, TailRequest{Space: "spaces/AAAATestSpace", Interval: MinPollInterval}) {
		if err != nil {
			t.Fatalf("a cancelled tail reported %v", err)
		}
	}
}
