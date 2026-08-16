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
