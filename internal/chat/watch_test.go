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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestEveryEventRequestCarriesItsEventTypes.
//
// The claim this card owns. spaceEvents.list answers 400 "Missing event types"
// without a filter, and the 400 names neither the parameter nor the values it
// takes, so the filter is built here and an empty set is refused before the
// request rather than after it.
//
// Asserted on the outgoing request, which is the only place it can be: a test
// on the response would pass against a server that ignored the filter, and this
// has to fail when somebody removes the filter rather than when Google changes
// its mind.
func TestEveryEventRequestCarriesItsEventTypes(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"spaceEvents": []}`)
	})

	if _, err := collect(r.client.SpaceEvents(context.Background(), SpaceEventsRequest{
		Space: "spaces/AAA",
		Types: []string{EventMessageCreated, EventReactionCreated},
	})); err != nil {
		t.Fatalf("SpaceEvents: %v", err)
	}

	filter := filterOf(t, r.paths()[0])
	for _, want := range []string{
		`event_types:"google.workspace.chat.message.v1.created"`,
		`event_types:"google.workspace.chat.reaction.v1.created"`,
		" OR ",
	} {
		if !strings.Contains(filter, want) {
			t.Errorf("filter = %s, want it to contain %s", filter, want)
		}
	}

	// And no filter at all is refused before anything is sent, because the API's
	// own answer to that is the terse 400 this exists to avoid.
	_, err := collect(r.client.SpaceEvents(context.Background(), SpaceEventsRequest{Space: "spaces/AAA"}))
	if err == nil {
		t.Fatal("a request with no event types was allowed")
	}
	if r.count() != 1 {
		t.Errorf("the refusal cost a request: %d in total", r.count())
	}
}

// TestTheTimeClauseIsTheOneTheEndpointTakes.
//
// Measured on 2026-08-16, and both halves are surprising enough to be worth
// pinning. start_time takes an equals and means "from here onwards", where
// messages.list takes > and < on createTime and refuses >=. And the OR group
// has to be parenthesized when a time clause is anded onto it: the same filter
// without the brackets is a 400 "Error parsing the filter".
func TestTheTimeClauseIsTheOneTheEndpointTakes(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"spaceEvents": []}`)
	})

	if _, err := collect(r.client.SpaceEvents(context.Background(), SpaceEventsRequest{
		Space: "spaces/AAA",
		Types: []string{EventMessageCreated, EventMessageDeleted},
		Since: time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC),
	})); err != nil {
		t.Fatalf("SpaceEvents: %v", err)
	}

	filter := filterOf(t, r.paths()[0])
	if !strings.Contains(filter, `AND start_time = "2026-08-16T09:00:00Z"`) {
		t.Errorf("filter = %s, want an equals: start_time > is a 400 here", filter)
	}
	if !strings.HasPrefix(filter, "(") {
		t.Errorf("filter = %s, want the event types parenthesized: without the brackets it is a 400", filter)
	}
}

// TestAnEventCarriesItsSubjectAndItsPayload.
//
// The body is a transcription of a live response. The payload's field is named
// for the event, so it is found by shape rather than by a table of ten names,
// and the subject is lifted out of it for the column a person reads.
func TestAnEventCarriesItsSubjectAndItsPayload(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"spaceEvents": [
			{"name": "spaces/AAA/spaceEvents/MTc4Ng",
			 "eventTime": "2026-08-16T21:36:48.376447Z",
			 "eventType": "google.workspace.chat.message.v1.created",
			 "messageCreatedEventData": {"message": {
				"name": "spaces/AAA/messages/BBB",
				"createTime": "2026-08-16T21:36:48.376447Z",
				"text": "spacebar live check"}}},
			{"name": "spaces/AAA/spaceEvents/MTc4Nw",
			 "eventTime": "2026-08-16T21:45:33.281839Z",
			 "eventType": "google.workspace.chat.reaction.v1.created",
			 "reactionCreatedEventData": {"reaction": {
				"name": "spaces/AAA/messages/BBB/reactions/CCC"}}},
			{"name": "spaces/AAA/spaceEvents/MTc4OA",
			 "eventTime": "2026-08-16T21:46:04.689711Z",
			 "eventType": "google.workspace.chat.space.v1.updated",
			 "somethingNobodyHasSeenYet": {"whatever": {}}}]}`)
	})

	events, err := collect(r.client.SpaceEvents(context.Background(), SpaceEventsRequest{
		Space: "spaces/AAA",
		Types: []string{EventMessageCreated},
	}))
	if err != nil {
		t.Fatalf("SpaceEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}

	if events[0].Subject != "spaces/AAA/messages/BBB" {
		t.Errorf("subject = %q", events[0].Subject)
	}
	if !strings.Contains(string(events[0].Payload), "spacebar live check") {
		t.Errorf("payload = %s, and it is the only place a deleted message's tombstone survives", events[0].Payload)
	}
	if events[1].Subject != "spaces/AAA/messages/BBB/reactions/CCC" {
		t.Errorf("a reaction's subject = %q", events[1].Subject)
	}

	// An event shaped in a way nobody here has seen keeps its type, its time,
	// and its bytes, and loses only the convenience of a subject. A guess about
	// somebody else's schema should be wrong in that direction.
	if events[2].EventType == "" || events[2].EventTime == "" {
		t.Errorf("an unrecognised payload cost the fields that were understood: %+v", events[2])
	}
	if events[2].Subject != "" {
		t.Errorf("an unrecognised payload produced a subject anyway: %q", events[2].Subject)
	}
}

// TestEventTypesForTakesGroupsAndRefusesTheRest, because an unknown type would
// otherwise reach the API inside a filter this tool built, and come back as a
// 400 about a filter the caller never wrote.
func TestEventTypesForTakesGroupsAndRefusesTheRest(t *testing.T) {
	types, err := EventTypesFor(DefaultEventGroups)
	if err != nil {
		t.Fatalf("EventTypesFor(%q): %v", DefaultEventGroups, err)
	}
	want := []string{
		EventMessageCreated, EventMessageDeleted, EventMessageUpdated,
		EventReactionCreated, EventReactionDeleted,
	}
	slices.Sort(want)
	if !slices.Equal(types, want) {
		t.Errorf("the default asked for %v, want %v", types, want)
	}

	// A group named twice asks once. The filter is a list of alternatives and a
	// repeated one is a longer request that means the same thing.
	twice, err := EventTypesFor("message,message")
	if err != nil {
		t.Fatalf("EventTypesFor: %v", err)
	}
	if len(twice) != 3 {
		t.Errorf("message,message asked for %v", twice)
	}

	for _, bad := range []string{"", " ", "messages", "everything", "message,nope"} {
		if _, err := EventTypesFor(bad); err == nil {
			t.Errorf("EventTypesFor(%q) was accepted", bad)
		}
	}
}

// TestAWatchNeverPollsFasterThanTheFloor, which is the same floor tail has and
// is now literally the same loop: per-space quota is shared with every other app
// acting in that space, and two commands polling it means two chances to get
// that wrong.
func TestAWatchNeverPollsFasterThanTheFloor(t *testing.T) {
	types := []string{EventMessageCreated}

	if err := (&Client{}).checkWatch(WatchRequest{
		Space: "spaces/AAA", Types: types, Interval: time.Second,
	}); err == nil {
		t.Error("a one-second interval was accepted")
	}
	if err := (&Client{}).checkWatch(WatchRequest{
		Space: "spaces/AAA", Types: types, Interval: MinPollInterval,
	}); err != nil {
		t.Errorf("the floor itself was refused: %v", err)
	}
}

// filterOf pulls the filter back out of a recorded request URI.
func filterOf(t *testing.T, uri string) string {
	t.Helper()

	_, query, _ := strings.Cut(uri, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parsing %q: %v", uri, err)
	}
	return values.Get("filter")
}

// TestAnAmbiguousPayloadDecodesTheSameWayEveryTime.
//
// Ranging a map is unordered, and three places took the first match out of one:
// the field ending in EventData, the key inside it carrying a name, and the key
// carrying a text. The API sends exactly one of each, measured across three
// event types, so today there is nothing to order.
//
// The day it sends two, an unsorted walk answers differently for the same bytes
// on the same build. Before this was sorted, 200 decodes of the event below
// produced 162 of one subject and 38 of the other. That is the worst kind of
// wrong to hand somebody, because re-running it disagrees with the report and
// there is nothing to blame.
//
// It has to decode repeatedly. **A test that decodes once passes on the broken
// build**, roughly four times in five, which is how a map-ordering bug survives
// a test suite.
func TestAnAmbiguousPayloadDecodesTheSameWayEveryTime(t *testing.T) {
	raw := []byte(`{"name":"spaces/AAAATestSpace/spaceEvents/1",
		"eventType":"google.workspace.chat.message.v1.created",
		"messageCreatedEventData":{"message":{"name":"spaces/AAAATestSpace/messages/AAA","text":"first"}},
		"batchMessageCreatedEventData":{"messages":{"name":"spaces/AAAATestSpace/messages/BBB","text":"second"}}}`)

	subjects := map[string]int{}
	payloads := map[string]int{}
	for range 200 {
		var e SpaceEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		subjects[e.Subject]++
		payloads[string(e.Payload)]++
	}

	if len(subjects) != 1 {
		t.Errorf("200 decodes of one event produced %d different subjects: %v", len(subjects), subjects)
	}
	if len(payloads) != 1 {
		t.Errorf("200 decodes of one event produced %d different payloads", len(payloads))
	}
}

// startTimeOf pulls the start_time back out of a recorded event filter, which
// is the value a fake endpoint has to honour to be worth testing against.
func startTimeOf(t *testing.T, uri string) (time.Time, bool) {
	t.Helper()

	_, clause, ok := strings.Cut(filterOf(t, uri), "start_time = ")
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, strings.Trim(clause, `"`))
	if err != nil {
		t.Fatalf("parsing start_time out of %q: %v", uri, err)
	}
	return at, true
}

// boundary is what a fake spaceEvents.list does with an event sitting exactly
// on the start_time it was given.
//
// It is a parameter rather than a constant because the real answer has not been
// measured, and the point of these tests is that the watch is right either way.
// See seenAt.
type boundary bool

const (
	inclusive boundary = true
	exclusive boundary = false
)

func (b boundary) String() string {
	if b {
		return "a start_time that includes the instant"
	}
	return "a start_time that excludes it"
}

// eventsAt is the fake endpoint: a fixed set of events, answered according to
// the start_time the caller asked for and the boundary rule under test.
func eventsAt(t *testing.T, b boundary, at ...time.Time) func(int, http.ResponseWriter, *http.Request) {
	t.Helper()

	return func(_ int, w http.ResponseWriter, r *http.Request) {
		since, bounded := startTimeOf(t, r.URL.String())

		rows := make([]string, 0, len(at))
		for i, when := range at {
			switch {
			case !bounded, when.After(since):
			case bool(b) && when.Equal(since):
			default:
				continue
			}
			rows = append(rows, fmt.Sprintf(
				`{"name": "spaces/AAA/spaceEvents/%d",
				  "eventTime": %q,
				  "eventType": %q,
				  "messageCreatedEventData": {"message": {"name": "spaces/AAA/messages/M%d"}}}`,
				i, wireTime(when), EventMessageCreated, i))
		}
		_, _ = fmt.Fprintf(w, `{"spaceEvents": [%s]}`, strings.Join(rows, ","))
	}
}

// TestAWatchYieldsAnEventOnceWhicheverWayTheBoundaryGoes.
//
// The claim sec-12 exists for. The watermark a watch polls from is the time of
// the last event it saw, so if start_time includes that instant the endpoint
// hands the same event back on every poll, forever, and nothing downstream can
// tell it from a new one: the events are byte-identical.
//
// Run against both boundary rules because the real one has not been measured
// against a live space, and a watch that is only correct under the reading
// somebody guessed is not correct.
func TestAWatchYieldsAnEventOnceWhicheverWayTheBoundaryGoes(t *testing.T) {
	one := tailAt.Add(time.Minute)
	two := tailAt.Add(2 * time.Minute)

	for _, b := range []boundary{inclusive, exclusive} {
		t.Run(b.String(), func(t *testing.T) {
			client, _, ctx := tailer(t, 6, eventsAt(t, b, one, two))

			var seen []string
			for event, err := range client.Watch(ctx, WatchRequest{
				Space:    "spaces/AAAATestSpace",
				Types:    []string{EventMessageCreated},
				Interval: MinPollInterval,
			}) {
				if err != nil {
					t.Fatalf("Watch: %v", err)
				}
				seen = append(seen, event.Name)
			}

			want := []string{"spaces/AAA/spaceEvents/0", "spaces/AAA/spaceEvents/1"}
			if !slices.Equal(seen, want) {
				t.Errorf("six polls yielded %v, want each event exactly once: %v", seen, want)
			}
		})
	}
}

// TestAWatchWithNothingNewGoesQuietWhicheverWayTheBoundaryGoes.
//
// The second half of the same failure, and the more expensive one. follow
// counts what a poll found and backs off after five that found nothing, so a
// poll whose only answer is the event that set the watermark has to count as
// nothing found. Otherwise a space where nobody has said anything since
// yesterday is polled at the base interval for as long as the command runs, and
// the per-space quota that pays for it is shared with every other app in that
// space.
func TestAWatchWithNothingNewGoesQuietWhicheverWayTheBoundaryGoes(t *testing.T) {
	for _, b := range []boundary{inclusive, exclusive} {
		t.Run(b.String(), func(t *testing.T) {
			client, recorded, ctx := tailer(t, 9, eventsAt(t, b, tailAt.Add(time.Minute)))

			for _, err := range client.Watch(ctx, WatchRequest{
				Space:    "spaces/AAAATestSpace",
				Types:    []string{EventMessageCreated},
				Interval: MinPollInterval,
			}) {
				if err != nil {
					t.Fatalf("Watch: %v", err)
				}
			}

			all := recorded.all()
			if len(all) < 8 {
				t.Fatalf("only %d waits recorded, which is too few to show a backoff", len(all))
			}
			if last := all[len(all)-1]; last <= MinPollInterval {
				t.Errorf("the last wait was %s, so nine polls of a space with one old event never backed off", last)
			}
		})
	}
}

// TestAWatchStartingAtAnEventStillYieldsIt.
//
// The reason this is a remembered set rather than the nanosecond nudge the code
// claimed for four milestones. A nudge applies to the first poll too, where the
// watermark is the caller's own --since, so `watch --since <the eventTime of
// something>` would silently skip the event the caller named. Nothing has been
// yielded when the first poll runs, so nothing is suppressed.
//
// The count is asserted from both sides deliberately, because that is what
// separates the two designs: zero is the nudge, more than one is no defence at
// all, and one is this.
//
// Only assertable under the inclusive rule: under the other one the endpoint
// itself excludes the event and no client-side choice can bring it back.
func TestAWatchStartingAtAnEventStillYieldsIt(t *testing.T) {
	at := tailAt.Add(time.Minute)
	client, _, ctx := tailer(t, 3, eventsAt(t, inclusive, at))

	var seen int
	for event, err := range client.Watch(ctx, WatchRequest{
		Space:    "spaces/AAAATestSpace",
		Types:    []string{EventMessageCreated},
		Interval: MinPollInterval,
		Since:    at,
	}) {
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if event.Name != "spaces/AAA/spaceEvents/0" {
			t.Errorf("event = %q", event.Name)
		}
		seen++
	}
	if seen != 1 {
		t.Errorf("an event at exactly --since was yielded %d times, want 1", seen)
	}
}
