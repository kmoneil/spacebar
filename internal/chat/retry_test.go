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
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/output"
)

// TestAPostIsNotReplayedAfterAnUpstreamError is the rule this whole card exists
// to hold, and it is asserted by counting requests rather than by reading the
// error.
//
// The distinction is the point. A refusal that arrives after the POST carries
// the same error and the same exit code as one that arrives before it, and the
// difference between them is a second message that somebody's colleagues can
// see. A 503 means the request may well have been processed and the
// acknowledgement lost; nothing in the response can tell us which, so the send
// is not repeated.
func TestAPostIsNotReplayedAfterAnUpstreamError(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "The service is currently unavailable.", "SERVICE_UNAVAILABLE")
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a 503 was reported as a successful send")
	}
	if h.count() != 1 {
		t.Fatalf("made %d requests, want exactly 1; a replayed POST is how one send becomes two messages", h.count())
	}
	if got := output.ExitCodeOf(err); got != output.ExitAPI {
		t.Errorf("exit code = %d, want %d", got, output.ExitAPI)
	}
	if len(h.waits()) != 0 {
		t.Errorf("the loop waited %v before giving up, so it intended to retry", h.waits())
	}
}

// TestAPostWithAMessageIDIsReplayed is the other half of the same rule. An ID
// makes the request one the API will not carry out twice, so a 503 on it can be
// retried without risking a duplicate.
func TestAPostWithAMessageIDIsReplayed(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("messageId") == "" {
			t.Error("the message ID was dropped from the retry")
		}
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "The service is currently unavailable.", "SERVICE_UNAVAILABLE")
	})

	_, err := h.client.SendMessage(context.Background(), SendRequest{
		Message:   Message{Text: "hello"},
		MessageID: DeriveMessageID("spaces/AAAATestSpace", "hello", ""),
	})
	if err == nil {
		t.Fatal("five 503s were reported as a successful send")
	}
	if h.count() != maxAttempts {
		t.Fatalf("made %d requests, want %d", h.count(), maxAttempts)
	}

	// Exponential, base 1s, factor 2, and the jitter is the identity here so
	// the window is what the policy computed.
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if got := h.waits(); !equalDurations(got, want) {
		t.Errorf("backoff = %v, want %v", got, want)
	}
}

// TestA429IsRetriedEvenOnAPost. A 429 is a refusal issued before the request
// was processed, so replaying it cannot double-post. This is the one status
// where that is true of a bare POST.
func TestA429IsRetriedEvenOnAPost(t *testing.T) {
	var attempts atomic.Int32
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writeAPIError(w, http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "Quota exceeded.", "RATE_LIMIT_EXCEEDED")
			return
		}
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB","text":"hello"}`)
	})

	sent, err := sendOne(h)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if h.count() != 2 {
		t.Errorf("made %d requests, want 2", h.count())
	}
	if sent.Name != "spaces/AAAATestSpace/messages/BBB" {
		t.Errorf("the response was not decoded: %+v", sent)
	}
}

// TestRetryAfterIsHonouredExactly. The server named a time, and adding jitter to
// it is a client second-guessing a rate limit it caused.
func TestRetryAfterIsHonouredExactly(t *testing.T) {
	var attempts atomic.Int32
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			writeAPIError(w, http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "Quota exceeded.", "RATE_LIMIT_EXCEEDED")
			return
		}
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	if _, err := sendOne(h); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := h.waits(); !equalDurations(got, []time.Duration{3 * time.Second}) {
		t.Errorf("waited %v, want exactly the 3s the server asked for", got)
	}
}

// TestA429WithoutRetryAfterUsesTheBackoff, and exhausting it is exit 6 rather
// than exit 3, because the caller's next move is to wait rather than to
// investigate.
func TestA429WithoutRetryAfterUsesTheBackoff(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "Quota exceeded.", "RATE_LIMIT_EXCEEDED")
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("an exhausted rate limit was reported as a successful send")
	}
	if h.count() != maxAttempts {
		t.Errorf("made %d requests, want %d", h.count(), maxAttempts)
	}
	if got := output.ExitCodeOf(err); got != output.ExitRateLimited {
		t.Errorf("exit code = %d, want %d", got, output.ExitRateLimited)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("the error is not ErrRateLimited: %v", err)
	}

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if got := h.waits(); !equalDurations(got, want) {
		t.Errorf("backoff = %v, want %v", got, want)
	}
	if !strings.Contains(err.Error(), "5 attempts") {
		t.Errorf("the failure does not say how many attempts were made: %v", err)
	}
}

// TestALongRetryAfterEndsTheLoopRatherThanSleeping.
//
// Honouring the header is not the same as holding the process open on the
// strength of it. A server that asks for five minutes has handed us a way to be
// stalled, and exit 6 gives the caller back the decision: the message says how
// long was asked for, and an agent can wait or do something else.
func TestALongRetryAfterEndsTheLoopRatherThanSleeping(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "300")
		writeAPIError(w, http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "Quota exceeded.", "RATE_LIMIT_EXCEEDED")
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("the send was reported as successful")
	}
	if h.count() != 1 {
		t.Errorf("made %d requests, want 1", h.count())
	}
	if len(h.waits()) != 0 {
		t.Errorf("the loop slept for %v rather than reporting", h.waits())
	}
	if got := output.ExitCodeOf(err); got != output.ExitRateLimited {
		t.Errorf("exit code = %d, want %d", got, output.ExitRateLimited)
	}
}

// TestA403IsNotRetried. A permission denial does not become a permission grant
// by being asked again, and four more requests are four more entries in
// somebody's audit log.
func TestA403IsNotRetried(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusForbidden, "PERMISSION_DENIED",
			"The caller does not have permission", "IAM_PERMISSION_DENIED")
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a 403 was reported as a successful send")
	}
	if h.count() != 1 {
		t.Errorf("made %d requests, want 1", h.count())
	}
	if !errors.Is(err, ErrPermission) {
		t.Errorf("the error is not ErrPermission: %v", err)
	}
	if got := output.ExitCodeOf(err); got != output.ExitAPI {
		t.Errorf("exit code = %d, want %d", got, output.ExitAPI)
	}
}

// TestA401RefreshesOnceAndRetriesOnce, then gives up at exit 4 rather than
// exit 3, because the fix is to authorize again rather than to look at a
// network.
func TestA401RefreshesOnceAndRetriesOnce(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHENTICATED",
			"Request is missing required authentication credential.", "CREDENTIALS_MISSING")
	})

	refreshes := 0
	h.client.auth = staticAuthorizer{header: "Bearer expired", refreshed: &refreshes, renews: true}

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a 401 was reported as a successful send")
	}
	if refreshes != 1 {
		t.Errorf("refreshed %d times, want exactly 1", refreshes)
	}
	if h.count() != 2 {
		t.Errorf("made %d requests, want 2: one, a refresh, and one more", h.count())
	}
	if len(h.waits()) != 0 {
		t.Errorf("the loop backed off before retrying a refresh, and nothing was overloaded: %v", h.waits())
	}
	if got := output.ExitCodeOf(err); got != output.ExitAuthRequired {
		t.Errorf("exit code = %d, want %d", got, output.ExitAuthRequired)
	}
	if !errors.Is(err, ErrAuthExpired) {
		t.Errorf("the error is not ErrAuthExpired: %v", err)
	}
}

// TestA401OnAWebhookIsNotRetried. There is nothing to refresh: the credential is
// the URL, and asking again with the same URL asks the same question.
func TestA401OnAWebhookIsNotRetried(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHENTICATED",
			"Request is missing required authentication credential.", "CREDENTIALS_MISSING")
	})

	if _, err := sendOne(h); err == nil {
		t.Fatal("a 401 was reported as a successful send")
	}
	if h.count() != 1 {
		t.Errorf("made %d requests, want 1", h.count())
	}
}

// TestARefreshThatRenewsNothingStops. An authorizer that cannot produce a new
// credential has nothing to retry with, and a second attempt with the same
// expired token is a request made to learn something already known.
func TestARefreshThatRenewsNothingStops(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Expired.", "CREDENTIALS_MISSING")
	})

	refreshes := 0
	h.client.auth = staticAuthorizer{header: "Bearer expired", refreshed: &refreshes, renews: false}

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a 401 was reported as a successful send")
	}
	if h.count() != 1 {
		t.Errorf("made %d requests, want 1", h.count())
	}
	if refreshes != 1 {
		t.Errorf("refreshed %d times, want 1", refreshes)
	}
}

// TestAGetIsRetriedAfterA503, which is the case the no-replay rule deliberately
// does not cover: repeating a read leaves the same state behind.
func TestAGetIsRetriedAfterA503(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "Try again.", "SERVICE_UNAVAILABLE")
	})

	if _, err := h.client.do(context.Background(), Request{Method: http.MethodGet}); err == nil {
		t.Fatal("five 503s were reported as success")
	}
	if h.count() != maxAttempts {
		t.Errorf("made %d requests, want %d", h.count(), maxAttempts)
	}
}

// TestCancellingStopsTheWait. A retry loop that finishes its nap before noticing
// the caller gave up is the difference between a command that answers ^C and one
// that appears to have hung.
func TestCancellingStopsTheWait(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "Try again.", "SERVICE_UNAVAILABLE")
	})

	ctx, cancel := context.WithCancel(context.Background())
	h.client.sleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}

	_, err := h.client.do(ctx, Request{Method: http.MethodGet})
	if err == nil {
		t.Fatal("a cancelled operation reported success")
	}
	if h.count() != 1 {
		t.Errorf("made %d requests after cancelling, want 1", h.count())
	}
	if got := output.ExitCodeOf(err); got != output.ExitAPI {
		t.Errorf("exit code = %d, want %d", got, output.ExitAPI)
	}
	// The 503s are the story; "context canceled" on its own explains nothing.
	if !strings.Contains(err.Error(), "UNAVAILABLE") {
		t.Errorf("the failure lost what was being retried: %v", err)
	}
}

// TestTheRealSleeperHonoursTheContext covers the default that the other tests
// replace. A hook nothing exercises is a hook that can be wrong.
func TestTheRealSleeperHonoursTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	if err := sleepFor(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepFor on a cancelled context = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("sleepFor took %v to notice a cancelled context", elapsed)
	}

	if err := sleepFor(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleepFor(1ms) = %v", err)
	}
}

// TestFullJitterStaysInsideItsWindow. The identity jitter the other tests use
// says nothing about the real one, and a jitter that can exceed its window
// turns a 32s cap into something else.
func TestFullJitterStaysInsideItsWindow(t *testing.T) {
	for _, window := range []time.Duration{0, time.Millisecond, time.Second, backoffCap} {
		for range 500 {
			got := fullJitter(window)
			if got < 0 || got > window {
				t.Fatalf("fullJitter(%v) = %v, outside the window", window, got)
			}
		}
	}
}

// TestTheBackoffIsCapped. Five attempts is not enough to reach 32s from a base
// of one second, so the cap is asserted on the function rather than through the
// loop, where it would be unreachable.
func TestTheBackoffIsCapped(t *testing.T) {
	client, err := New(Options{BaseURL: "https://chat.example/v1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.jitter = func(window time.Duration) time.Duration { return window }

	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, backoffCap},
		{20, backoffCap},
	} {
		got, ok := client.delay(tc.attempt, errors.New("boom"))
		if !ok {
			t.Fatalf("delay(%d) declined to retry", tc.attempt)
		}
		if got != tc.want {
			t.Errorf("delay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"absent", "", 0},
		{"seconds", "5", 5 * time.Second},
		{"padded", "  5  ", 5 * time.Second},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"a date ahead", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{"a date behind", now.Add(-90 * time.Second).Format(http.TimeFormat), 0},
		{"nonsense", "soon", 0},

		// The overflow, and the number is not arbitrary. A time.Duration is an
		// int64 of nanoseconds, and 1e9 is 2^9 x 5^9, so the greatest common
		// divisor with 2^64 is 512 and a server can pick a delta-seconds whose
		// product wraps onto any multiple of 512ns it likes. This one was solved
		// for 512ns exactly, and before the bound it produced 512ns, which delay
		// honoured as sent, with no jitter, four times over.
		//
		// It saturates now, which delay declines rather than waits for.
		{"a value chosen to wrap to 512ns", "20211507185753197", math.MaxInt64},

		// The two ends of the bound, so that a later change to it cannot move
		// the boundary without this saying so.
		{"the largest that does not wrap", "9223372036", 9223372036 * time.Second},
		{"one past it", "9223372037", math.MaxInt64},
		{"the largest an int64 holds", "9223372036854775807", math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRetryAfter(tc.value, now); got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestSafeToReplay states the method table, because the whole no-replay rule
// reduces to it.
func TestSafeToReplay(t *testing.T) {
	for _, tc := range []struct {
		method     string
		idempotent bool
		want       bool
	}{
		{http.MethodGet, false, true},
		{http.MethodHead, false, true},
		{http.MethodPut, false, true},
		{http.MethodDelete, false, true},
		{http.MethodPost, false, false},
		{http.MethodPost, true, true},
		// PATCH is idempotent only because of what a particular patch says, not
		// because of the method, so it opts in like a POST.
		{http.MethodPatch, false, false},
		{http.MethodPatch, true, true},
	} {
		req := Request{Method: tc.method, Idempotent: tc.idempotent}
		if got := req.safeToReplay(); got != tc.want {
			t.Errorf("%s idempotent=%v safeToReplay = %v, want %v", tc.method, tc.idempotent, got, tc.want)
		}
	}
}

func equalDurations(got, want []time.Duration) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// FuzzRetryAfterIsAlwaysSaneOrIgnored states the property, because the cases
// somebody thinks of are `0`, `-1` and `abc`, and none of those is the one that
// bites.
//
// Two claims, and the first is the one the overflow broke. A delta-seconds
// header is read faithfully or saturated, never wrapped: a larger number can
// therefore never produce a smaller wait, which is exactly what wrapping did.
// `20211507185753197` seconds became 512 nanoseconds.
//
// The second is weaker and is still worth stating: whatever the bytes, the
// result is never negative. A negative duration would be a wait the loop treats
// as absent, which is survivable, and it is the same arithmetic fault wearing a
// different sign.
//
// What is deliberately not claimed is that every positive result is at least a
// second. It is not, and it should not be: an HTTP-date names an instant, `now`
// has sub-second precision, and a header saying 09:00:01 at 09:00:00.700 means
// 300ms and is honoured as such. The card for this asked for that claim; it was
// false before this change and after it, and the server asking for 300ms is not
// the same fault as arithmetic inventing 512ns.
func FuzzRetryAfterIsAlwaysSaneOrIgnored(f *testing.F) {
	for _, seed := range []string{
		"", "0", "-1", "5", "  5  ", "soon",
		"20211507185753197", "9223372036", "9223372037", "9223372036854775807",
		"Mon, 17 Aug 2026 09:00:01 GMT",
	} {
		f.Add(seed)
	}

	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	f.Fuzz(func(t *testing.T, value string) {
		got := parseRetryAfter(value, now)

		if got < 0 {
			t.Fatalf("parseRetryAfter(%q) = %v, which is a wait in the past", value, got)
		}

		// Where the header is delta-seconds, the answer is exact rather than
		// merely sane, and exactness is what makes wrapping impossible: there
		// is one right value and this is it.
		seconds, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return
		}
		want := time.Duration(0)
		switch {
		case seconds <= 0:
			want = 0
		case int64(seconds) > maxRetryAfterSeconds:
			want = math.MaxInt64
		default:
			want = time.Duration(seconds) * time.Second
		}
		if got != want {
			t.Fatalf("parseRetryAfter(%q) = %v, want %v", value, got, want)
		}
	})
}
