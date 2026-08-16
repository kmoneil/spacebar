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

		follow(ctx, c, req.Interval, since, yield,
			func(ctx context.Context, since time.Time, yield func(SpaceEvent, error) bool) (time.Time, int, bool) {
				return c.pollEvents(ctx, req, since, yield)
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

// pollEvents fetches everything since the watermark and returns the new one.
//
// The watermark is the event time rather than the count, for the reason tail's
// is a createTime: a seen-set grows without bound and a count cannot survive a
// page boundary. start_time is the API's own comparison, and it is inclusive
// where messages.list's createTime is exclusive, so the watermark is nudged by
// a nanosecond to avoid replaying the event that set it.
func (c *Client) pollEvents(ctx context.Context, req WatchRequest, since time.Time,
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
		if !yield(event, nil) {
			return newest, found, false
		}
		found++
		if at, parseErr := time.Parse(time.RFC3339Nano, event.EventTime); parseErr == nil && at.After(newest) {
			newest = at
		}
	}
	return newest, found, true
}
