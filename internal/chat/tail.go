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
	"iter"
	"time"
)

// MinPollInterval is the floor on how often a space may be polled.
//
// Per-space quota is shared with every other app acting in that space, so a
// tight loop degrades the space for everybody in it and not just for the person
// running it. Two seconds is the number SPEC.md §13 gives.
//
// Enforced rather than defaulted, and a request below it is refused rather than
// clamped. A clamp is a value silently altered to make it acceptable, which is
// the thing this tool does not do anywhere else: invalid UTF-8 is refused rather
// than replaced, and a Chat wrapper character is refused rather than escaped.
// It is also the more dangerous of the two, because somebody who asked for
// 100ms and quietly got 2s has a script whose timing is wrong and no way to
// find out.
const MinPollInterval = 2 * time.Second

// DefaultPollInterval is what a caller who says nothing gets.
const DefaultPollInterval = 5 * time.Second

// MaxPollInterval bounds the adaptive backoff. A quiet space should cost almost
// nothing to watch, and a minute is long enough to be almost nothing without
// being long enough to feel broken.
const MaxPollInterval = 60 * time.Second

// quietPollsBeforeBackoff is how many empty polls in a row double the interval.
const quietPollsBeforeBackoff = 5

// TailRequest asks to follow a space.
type TailRequest struct {
	// Space is the resource name, already resolved and checked.
	Space string

	// Interval is how long to wait between polls. Zero means
	// DefaultPollInterval. Below MinPollInterval is refused.
	Interval time.Duration

	// Since is the watermark: only messages created strictly after it are
	// returned. Zero means start from now, which is what somebody running
	// `tail` expects, in the way `tail -f` does not replay a file.
	Since time.Time

	// Backfill is how many existing messages to print before following. Zero
	// prints none. It is a separate knob from Since because "show me the last
	// twenty and then follow" is the common request and expressing it as a
	// timestamp means guessing one.
	Backfill int
}

// CheckInterval refuses a poll interval below the floor.
//
// Exported because the CLI refuses before building anything, so that a bad
// interval costs no profile load and no network call, and because `watch` will
// use the same rule and must not grow a second copy of it.
func CheckInterval(d time.Duration) error {
	if d == 0 || d >= MinPollInterval {
		return nil
	}
	return clientErr("a poll interval of %s is below the %s floor, so it is refused rather than rounded up.\n"+
		"Per-space quota is shared with every other app acting in that space, so a tight loop "+
		"degrades the space for everybody in it. Ask for %s or more.",
		d, MinPollInterval, MinPollInterval)
}

// Tail follows a space, yielding each new message as it arrives.
//
// It ends when the context is cancelled, which is how a caller stops it and is
// not an error: a `tail` that exited non-zero on a deliberate interrupt would
// make every shell script wrapping it wrong.
//
// What it cannot see is a message that changed. A poll filtered on createTime
// returns new messages, an edit does not change createTime, and a delete
// removes a message rather than producing one. So this shows a conversation as
// it is written and never corrects itself. spaceEvents is what sees mutations,
// and it is a different command for that reason.
func (c *Client) Tail(ctx context.Context, req TailRequest) iter.Seq2[Message, error] {
	return func(yield func(Message, error) bool) {
		if err := c.checkTail(req); err != nil {
			yield(Message{}, err)
			return
		}

		since, ok := c.startOfTail(ctx, req, yield)
		if ok {
			c.follow(ctx, req, since, yield)
		}
	}
}

// CheckTailWindow refuses the two ways of saying where to start at once.
//
// Exported for the same reason CheckInterval is: the CLI refuses before it
// loads a profile, so a contradiction in what was typed costs no keyring read
// and no network call, and the MCP server must reach the same rule rather than
// grow a second copy of it. checkTail calls it again below, because a refusal
// that only exists in an adapter is a refusal one of the adapters will forget.
//
// Refused rather than ordered by precedence. startOfTail fetches the last N
// messages with no filter at all and then follows from the newest of them, so a
// Since given beside a Backfill is read and then overwritten. Either precedence
// rule leaves somebody watching a window they did not ask for, with nothing
// saying so.
func CheckTailWindow(since time.Time, backfill int) error {
	if since.IsZero() || backfill <= 0 {
		return nil
	}
	return clientErr("--since and --backfill both say where to start, and they disagree.\n"+
		"--since %s replays everything after that time; --backfill %d replays the last %d messages, "+
		"whenever they were.\nAsk for one of them.",
		since.UTC().Format(time.RFC3339), backfill, backfill)
}

// checkTail refuses what cannot be polled, before anything is fetched.
func (c *Client) checkTail(req TailRequest) error {
	if err := CheckSpaceName(req.Space); err != nil {
		return err
	}
	if err := CheckTailWindow(req.Since, req.Backfill); err != nil {
		return err
	}
	return CheckInterval(req.Interval)
}

// follow is the poll loop, split from Tail for the complexity ceiling.
//
// The gate was right to ask: the validation, the backfill, and the loop were
// three separate concerns in one closure, and only the third one is the part
// anybody comes back to read.
func (c *Client) follow(ctx context.Context, req TailRequest, since time.Time, yield func(Message, error) bool) {
	follow(ctx, c, req.Interval, since, yield,
		func(ctx context.Context, since time.Time, yield func(Message, error) bool) (time.Time, int, bool) {
			return c.pollOnce(ctx, req.Space, since, yield)
		})
}

// follow is the poll loop, shared by tail and watch.
//
// A free function rather than a method because Go has no generic methods, and
// generic because the alternative is two copies of a loop whose backoff, whose
// quiet counter, and whose cancellation rule all have to stay identical. They
// would not: the second copy is the one that stops being adjusted.
//
// What differs between the two callers is exactly one thing, which is the
// parameter: how to fetch everything since a watermark and say what the new
// watermark is. tail asks messages.list about createTime and watch asks
// spaceEvents.list about eventTime.
func follow[T any](ctx context.Context, c *Client, interval time.Duration, since time.Time,
	yield func(T, error) bool,
	poll func(context.Context, time.Time, func(T, error) bool) (time.Time, int, bool),
) {
	if interval == 0 {
		interval = DefaultPollInterval
	}

	quiet := 0
	for {
		// The existing retry seam, so a test drives this with the same injected
		// clock it uses for backoff and never actually waits. A cancelled wait
		// is how a caller stops this, and it is not an error: a command that
		// exited non-zero on a deliberate interrupt would make every shell
		// script wrapping it wrong.
		if err := c.sleep(ctx, c.backoff(interval, quiet)); err != nil {
			return
		}

		newest, found, ok := poll(ctx, since, yield)
		if !ok {
			return
		}
		if found == 0 {
			quiet++
			continue
		}

		quiet = 0
		since = newest
	}
}

// startOfTail prints any requested backfill and returns the watermark to follow
// from.
func (c *Client) startOfTail(ctx context.Context, req TailRequest, yield func(Message, error) bool) (time.Time, bool) {
	since := req.Since
	if since.IsZero() {
		since = c.now()
	}

	if req.Backfill <= 0 {
		return since, true
	}

	// Fetched newest-first because that is the end a limit has to cut from, and
	// then reversed, because a conversation is read in the order it happened.
	// This is the one place `tail` buffers, it is bounded by --backfill, and it
	// is why the streaming claim elsewhere is about lists and not about this.
	var recent []Message
	for message, err := range c.Messages(ctx, ListMessagesRequest{
		Space:   req.Space,
		OrderBy: OrderNewestFirst,
		Limit:   req.Backfill,
	}) {
		if err != nil {
			yield(Message{}, err)
			return since, false
		}
		recent = append(recent, message)
	}

	for i := len(recent) - 1; i >= 0; i-- {
		if !yield(recent[i], nil) {
			return since, false
		}
	}

	// Follow from the newest message actually printed, so that nothing between
	// the backfill and the first poll is missed or repeated.
	if len(recent) > 0 {
		if at, err := time.Parse(time.RFC3339Nano, recent[0].CreateTime); err == nil {
			return at, true
		}
	}
	return since, true
}

// pollOnce fetches everything created since the watermark, oldest first, and
// returns the new watermark and how many arrived.
func (c *Client) pollOnce(ctx context.Context, space string, since time.Time, yield func(Message, error) bool) (time.Time, int, bool) {
	found := 0
	newest := since

	// Oldest first, because these are printed as they happened and a poll
	// returns few enough that the ascending under-fill costs one extra request
	// at most. Strictly greater than the watermark, which is the API's own
	// comparison and means a message sharing a createTime with the watermark to
	// the microsecond is not repeated. It is also not shown, which is the
	// narrow window this cannot close and which the help says out loud.
	for message, err := range c.Messages(ctx, ListMessagesRequest{
		Space:   space,
		OrderBy: OrderOldestFirst,
		Filter:  fmt.Sprintf("createTime > %q", since.UTC().Format(time.RFC3339Nano)),
	}) {
		if err != nil {
			report(ctx, err, yield)
			return newest, found, false
		}
		if !yield(message, nil) {
			return newest, found, false
		}
		found++
		if at, parseErr := time.Parse(time.RFC3339Nano, message.CreateTime); parseErr == nil && at.After(newest) {
			newest = at
		}
	}
	return newest, found, true
}

// report passes a failure to the caller unless the context was cancelled, in
// which case there is nothing to report.
//
// A cancelled context is how a tail is stopped, and a request in flight when it
// happens fails with "context canceled". Yielding that would make Ctrl-C exit
// non-zero, which is the card's own claim and which every shell script wrapping
// this command would then have to work around.
//
// The walk is over either way. The only question this answers is whether
// anybody hears about it, which is why it returns nothing: an earlier version
// returned a bool that was always false, and unparam was right that a result
// carrying no information is a result somebody will eventually branch on.
func report[T any](ctx context.Context, err error, yield func(T, error) bool) {
	if ctx.Err() != nil {
		return
	}
	var zero T
	yield(zero, err)
}

// backoff is the interval to wait before the next poll.
//
// A space that has said nothing for five polls is probably going to say nothing
// for a while, so the interval doubles up to the maximum and any message resets
// it. The floor still applies: backoff only ever makes the wait longer.
func (c *Client) backoff(base time.Duration, quiet int) time.Duration {
	if quiet < quietPollsBeforeBackoff {
		return base
	}

	wait := base
	for range quiet - quietPollsBeforeBackoff + 1 {
		wait *= 2
		if wait >= MaxPollInterval {
			return MaxPollInterval
		}
	}
	return wait
}
