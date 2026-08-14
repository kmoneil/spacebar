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
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/spacebar/internal/output"
)

// The retry policy from SPEC.md §7.4, which the spec calls non-negotiable.
//
// Exponential backoff with full jitter, and the jitter is the part that is
// easy to leave out. Per-space Chat quota is shared with every other app acting
// in that space, so a room with several bots in it can have all of them
// rate-limited by the same burst; if they all back off by the same computed
// delay they retry together and cause the next one. Full jitter spreads them.
const (
	maxAttempts   = 5
	backoffBase   = 1 * time.Second
	backoffFactor = 2
	backoffCap    = 32 * time.Second
)

// retryKind is what the loop does after a failed attempt.
type retryKind int

const (
	// retryStop returns the error. Either it cannot be retried, or retrying it
	// could post the message twice.
	retryStop retryKind = iota

	// retryBackoff waits and tries again.
	retryBackoff

	// retryRefresh renews the credential and tries again immediately. No
	// backoff: nothing was overloaded, we simply arrived with an expired token.
	retryRefresh
)

// plan decides what to do after a failed attempt.
//
// The rule this function exists for is the no-replay rule, and it is worth
// stating in full because getting it wrong is invisible in testing and loud in
// production. A POST that received a 503 may well have been processed: the
// message was posted and the acknowledgement was lost. Retrying it is how one
// send becomes two messages in a space full of people, and nothing in the
// response distinguishes the two cases. So a POST is replayed only when
// repeating it cannot produce a second message.
//
// Three conditions permit it:
//
//   - 429, which is a refusal issued before the request was processed.
//   - 401, likewise: the credential was rejected before anything was done with
//     the body.
//   - a failure at the dial stage, where no request bytes reached the server.
//
// Everything else is left to the caller, who at least knows whether a duplicate
// matters.
func (c *Client) plan(err error, req Request, refreshed bool) retryKind {
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		return retryStop
	}

	switch {
	case apiErr.StatusCode == http.StatusUnauthorized:
		if refreshed || c.auth == nil {
			return retryStop
		}
		return retryRefresh

	case apiErr.StatusCode == http.StatusTooManyRequests:
		return retryBackoff

	case apiErr.StatusCode >= 500:
		if req.safeToReplay() {
			return retryBackoff
		}
		return retryStop

	case apiErr.StatusCode == 0:
		// No response at all. Retryable is false for a local failure such as a
		// body that would not read, where trying again would fail the same way.
		if !apiErr.Retryable {
			return retryStop
		}
		if req.safeToReplay() || preProcessing(err) {
			return retryBackoff
		}
		return retryStop
	}
	return retryStop
}

// preProcessing reports whether a transport failure happened before any request
// byte could have been acted on.
//
// Only the dial stage qualifies, and the test is deliberately narrow. A DNS
// failure or a refused connection means the server never saw the request, so
// replaying a POST is safe. A timeout or a reset partway through does not mean
// that: the request may have been written, processed, and the answer lost. The
// conservative reading is the only safe one, because the cost of being wrong is
// a duplicate message that somebody else has to explain.
func preProcessing(err error) bool {
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return true
	}
	if e, ok := errors.AsType[*net.OpError](err); ok {
		return e.Op == "dial"
	}
	return false
}

// refresh renews the credential after a 401.
//
// Once per operation, per SPEC.md §7.4. A refresh that reports no new
// credential, or fails, means the authorization is genuinely gone, which is
// exit 4 rather than exit 3: the caller's next step is to log in again, not to
// investigate a network.
func (c *Client) refresh(ctx context.Context, cause error) error {
	c.logf("refreshing the credential after %s", http.StatusText(http.StatusUnauthorized))

	renewed, err := c.auth.Refresh(ctx)
	if err != nil {
		return output.Errorf("UNAUTHENTICATED", output.ExitAuthRequired,
			"the authorization for this profile expired and could not be renewed: %v", c.scrub(err.Error()))
	}
	if !renewed {
		return cause
	}
	return nil
}

// wait sleeps before the next attempt, or reports that there will not be one.
func (c *Client) wait(ctx context.Context, attempt int, cause error) error {
	delay, ok := c.delay(attempt, cause)
	if !ok {
		return cause
	}

	c.logf("retrying in %s (attempt %d of %d)", delay.Round(time.Millisecond), attempt+1, maxAttempts)
	if err := c.sleep(ctx, delay); err != nil {
		return c.interrupted(err, cause)
	}
	return nil
}

// delay is how long to wait before the next attempt, and whether to make one.
//
// Retry-After wins when the server sent it, exactly, with no jitter added: the
// server named a time and second-guessing it is how a client makes a rate limit
// worse. But it is honoured only up to the backoff cap. Beyond that the loop
// stops and reports the failure, which is not ignoring the header but declining
// to hold the process open on the strength of it. A caller told to wait five
// minutes is better served by exit 6 and the freedom to decide, and an agent
// that has been handed a five-minute sleep by a server it does not control has
// been handed a way to be stalled.
func (c *Client) delay(attempt int, cause error) (time.Duration, bool) {
	if e, ok := errors.AsType[*APIError](cause); ok && e.RetryAfter > 0 {
		if e.RetryAfter > backoffCap {
			return 0, false
		}
		return e.RetryAfter, true
	}

	window := backoffBase
	for range attempt - 1 {
		window *= backoffFactor
		if window >= backoffCap {
			return c.jitter(backoffCap), true
		}
	}
	return c.jitter(window), true
}

// interrupted reports a wait that the caller's context ended.
//
// The original failure is kept in the message, because "context deadline
// exceeded" on its own tells somebody nothing about the 503s that led there.
func (c *Client) interrupted(err error, cause error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return output.Errorf("DEADLINE_EXCEEDED", output.ExitAPI,
			"this ran out of time while waiting to retry: %v", cause)
	}
	return output.Errorf("CANCELLED", output.ExitAPI,
		"this was cancelled while waiting to retry: %v", cause)
}

// fullJitter picks a delay uniformly from zero up to window.
//
// Full jitter rather than the equal-jitter or decorrelated variants, per
// SPEC.md §7.4. The property that matters is that two clients backing off from
// the same burst do not come back at the same moment, and choosing from the
// whole window is what gives that.
func fullJitter(window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(window) + 1))
}

// sleepFor waits, unless the caller gives up first.
//
// The context is honoured during the wait rather than only around it. A caller
// that cancelled expects to stop, and a retry loop that finishes its nap before
// noticing is the difference between a command that responds to ^C and one that
// appears to have hung.
func sleepFor(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// parseRetryAfter reads the header in both of the forms RFC 9110 allows.
//
// Google sends delta-seconds. The HTTP-date form is accepted anyway because it
// is legal, it costs four lines, and the failure mode of not accepting it is
// that we ignore an instruction from the server and retry too early.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}

	if when, err := http.ParseTime(value); err == nil {
		if d := when.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
