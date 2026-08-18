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
	"iter"
	"maps"
	"slices"
	"strings"
	"time"
)

// The event groups a caller names, and what each one asks the API for.
//
// Groups rather than the reverse-domain names themselves, because
// "google.workspace.chat.message.v1.created" is not something to type three of
// on a command line, and because the useful unit is the subject rather than the
// verb: somebody following a conversation wants edits and deletions along with
// new messages, not one of the three.
var eventGroups = map[string][]string{
	"message":    {EventMessageCreated, EventMessageUpdated, EventMessageDeleted},
	"reaction":   {EventReactionCreated, EventReactionDeleted},
	"membership": {EventMembershipCreated, EventMembershipUpdated, EventMembershipDeleted},
	"space":      {EventSpaceUpdated},
}

// DefaultEventGroups is what `watch` asks for when nobody says.
//
// The conversation, and not the administration. Messages and reactions are what
// changes minute to minute and are what tail cannot show; memberships and space
// updates arrive on a different rhythm and would be noise in the common case.
// A default of everything makes the output unreadable, and a default of new
// messages only makes this a worse tail.
const DefaultEventGroups = "message,reaction"

// EventGroups lists the group names, sorted, for a message that has to name
// them.
func EventGroups() []string {
	return slices.Sorted(maps.Keys(eventGroups))
}

// EventTypesFor turns a comma-separated list of groups into the event types the
// filter asks for.
//
// Refused rather than passed through, for the reason --order is: an event type
// this tool does not know would reach the API inside a filter it built, and
// come back as a 400 about a filter the caller never wrote.
func EventTypesFor(groups string) ([]string, error) {
	var types []string
	for _, name := range strings.Split(groups, ",") {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" {
			continue
		}
		kinds, ok := eventGroups[name]
		if !ok {
			return nil, clientErr("%q is not a kind of event.\nThe kinds are: %s.",
				name, strings.Join(EventGroups(), ", "))
		}
		types = append(types, kinds...)
	}
	if len(types) == 0 {
		return nil, clientErr("no events were asked for.\nThe kinds are: %s.",
			strings.Join(EventGroups(), ", "))
	}

	slices.Sort(types)
	return slices.Compact(types), nil
}

// WatchRequest asks to follow what happens in a space.
type WatchRequest struct {
	Space string

	// Types is the event types to ask for, as EventTypesFor produces them.
	Types []string

	// Interval is how long to wait between polls. Zero means
	// DefaultPollInterval. Below MinPollInterval is refused.
	Interval time.Duration

	// Since is the watermark. Zero means start from now, which is what somebody
	// running a follow expects.
	Since time.Time

	// Filter replaces the built one entirely.
	Filter string
}

// Watch follows a space's events, yielding each one as it arrives.
//
// The same shape as Tail and the same loop, which is shared rather than copied:
// the interval floor, the adaptive backoff, and the rule that a cancelled
// context is how this ends rather than a failure, all live in one place.
//
// What it sees that Tail cannot is the point of it. A poll on createTime
// returns new messages, so an edit, a deletion and a reaction are all invisible
// to Tail: an edit does not change createTime, a deletion removes a message
// rather than producing one, and a reaction is not a message at all. Each of
// those is an event here.
func (c *Client) Watch(ctx context.Context, req WatchRequest) iter.Seq2[SpaceEvent, error] {
	return func(yield func(SpaceEvent, error) bool) {
		if err := c.checkWatch(req); err != nil {
			yield(SpaceEvent{}, err)
			return
		}

		since := req.Since
		if since.IsZero() {
			since = c.now()
		}

		// Carried across polls rather than rebuilt inside one, because what it
		// remembers is what the previous poll handed over. See seenAt.
		var seen seenAt

		follow(ctx, c, req.Interval, since, yield,
			func(ctx context.Context, since time.Time, yield func(SpaceEvent, error) bool) (time.Time, int, bool) {
				return c.pollEvents(ctx, req, since, &seen, yield)
			})
	}
}

// checkWatch refuses what cannot be polled, before anything is fetched.
//
// The same three questions tail's own check asks, plus the one this endpoint
// adds: a filter with no event types in it is refused here rather than by the
// API, whose answer is a 400 naming neither the parameter nor its values.
func (c *Client) checkWatch(req WatchRequest) error {
	if err := CheckSpaceName(req.Space); err != nil {
		return err
	}
	if err := CheckInterval(req.Interval); err != nil {
		return err
	}
	_, err := eventFilter(SpaceEventsRequest{Types: req.Types, Filter: req.Filter})
	return err
}

// seenAt remembers which events were handed to the consumer at exactly one
// instant, so that a poll asking again from that instant does not hand them
// over a second time.
//
// spaceEvents.list bounds a query with start_time = "...", and the watermark a
// watch polls from is the time of the last event it saw. If the endpoint
// included that instant, the event would come back on every poll: the consumer
// would see it again every interval for as long as the command ran, and found
// would never be zero, so the adaptive backoff in follow would never engage and
// a space where nothing is happening would be polled at the base interval
// forever.
//
// Measured on 2026-08-18, it does not: the boundary is exclusive, so nothing
// here fires against Chat as it behaves today. See eventFilter, which carries
// the measurement.
//
// This is kept anyway, and that is a decision rather than an oversight. The
// measurement is a fact about somebody else's API on one day, and the whole
// point of remembering was that the answer would not be load bearing: a defence
// that has to be right about Google's boundary rule is one more thing to be
// wrong about later, and what it costs to keep is an empty map and a comparison
// per event. What it would cost to be wrong is a watch that repeats itself
// every interval on a per-space quota shared with every other app in the space.
// The tests take the boundary rule as a parameter and run both ways for that
// reason, and disabling this fails the inclusive half of each, so it is a
// defence with a test that can still fail rather than dead code wearing one.
//
// The obvious alternative is to move the watermark on by a nanosecond, and a
// comment here claimed that was being done for four milestones while nothing
// did it. It would also have been wrong. The first poll's watermark is the
// caller's own --since, so nudging it drops an event at exactly the instant
// the caller asked to start from, silently, and a value altered to make the
// loop convenient is the thing this project refuses everywhere else.
//
// An event at the caller's own --since still arrives, because nothing has been
// yielded when the first poll starts.
//
// The set is bounded by the events sharing a single nanosecond, because moving
// the instant empties it. That is the difference from the seen-set the
// watermark was chosen over, which grows for as long as the watch runs.
type seenAt struct {
	at    time.Time
	names map[string]bool
}

// holds reports whether this event has already been handed to the consumer.
//
// Only an event at exactly the remembered instant can have been: anything
// earlier is behind a bound the endpoint applies itself, and anything later
// has not been seen.
func (s *seenAt) holds(e SpaceEvent) bool {
	if len(s.names) == 0 || !s.names[e.Name] {
		return false
	}
	at, ok := eventTime(e)
	return ok && at.Equal(s.at)
}

// record notes an event that has been yielded, emptying the set when the
// instant moves on.
//
// An event older than the instant is not recorded and does not rewind it. That
// case should not arise, because the endpoint answers in ascending order, and
// if it ever does the cost of ignoring it is one event that could be yielded
// twice rather than a watch that forgets everything it just saw.
func (s *seenAt) record(e SpaceEvent) {
	at, ok := eventTime(e)
	if !ok || at.Before(s.at) {
		return
	}
	if at.After(s.at) || s.names == nil {
		s.at = at
		s.names = map[string]bool{}
	}
	s.names[e.Name] = true
}

// eventTime reads an event's own timestamp.
//
// An event whose time will not parse is still yielded and still does not move
// the watermark, which is what this loop always did: losing the ordering of one
// malformed event is better than dropping it, and better than rewinding a watch
// to whatever a zero time would mean.
func eventTime(e SpaceEvent) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339Nano, e.EventTime)
	return at, err == nil
}

// pollEvents fetches everything since the watermark and returns the new one.
//
// The watermark is the event time rather than the count, for the reason tail's
// is a createTime: a count cannot survive a page boundary. What it costs is
// that the answer depends on how the endpoint treats the instant itself, which
// is what seen is for.
//
// found counts what was yielded rather than what arrived, so a poll whose only
// answer is the event that set the watermark counts as a quiet one and lets the
// backoff engage.
func (c *Client) pollEvents(ctx context.Context, req WatchRequest, since time.Time, seen *seenAt,
	yield func(SpaceEvent, error) bool,
) (time.Time, int, bool) {
	found := 0
	newest := since

	for event, err := range c.SpaceEvents(ctx, SpaceEventsRequest{
		Space:  req.Space,
		Types:  req.Types,
		Filter: req.Filter,
		Since:  since,
	}) {
		if err != nil {
			report(ctx, err, yield)
			return newest, found, false
		}
		if seen.holds(event) {
			continue
		}
		if !yield(event, nil) {
			return newest, found, false
		}
		found++
		seen.record(event)
		if at, ok := eventTime(event); ok && at.After(newest) {
			newest = at
		}
	}
	return newest, found, true
}
