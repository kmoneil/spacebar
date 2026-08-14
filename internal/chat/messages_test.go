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
	"regexp"
	"strings"
	"testing"
)

// TestASendPostsWhereTheProfileSaysAndNowhereElse.
//
// A webhook URL is the whole configuration: it is the messages endpoint for one
// space, and it cannot post anywhere else. So an empty space is not an omission
// to be filled in with a guess, it is the only correct value, and the request
// goes to exactly the URL the profile holds.
func TestASendPostsWhereTheProfileSaysAndNowhereElse(t *testing.T) {
	var method, path string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB","text":"hello","createTime":"2026-08-14T18:42:20Z"}`)
	})

	sent, err := sendOne(h)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if method != http.MethodPost {
		t.Errorf("method = %s, want POST", method)
	}
	if path != "/v1/spaces/AAAATestSpace/messages" {
		t.Errorf("path = %s", path)
	}
	if sent.Name != "spaces/AAAATestSpace/messages/BBB" || sent.Text != "hello" {
		t.Errorf("the response was not decoded: %+v", sent)
	}
	if sent.CreateTime != "2026-08-14T18:42:20Z" {
		t.Errorf("createTime = %q, and a timestamp is passed through rather than reformatted", sent.CreateTime)
	}
}

// TestASendCarriesItsQueryParameters, all four of them, under the names the API
// uses.
func TestASendCarriesItsQueryParameters(t *testing.T) {
	var got map[string]string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		got = map[string]string{}
		for name := range r.URL.Query() {
			got[name] = r.URL.Query().Get(name)
		}
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	if _, err := h.client.SendMessage(context.Background(), SendRequest{
		Message:            Message{Text: "hello"},
		ThreadKey:          "deploys",
		MessageReplyOption: "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD",
		MessageID:          "client-abc",
		RequestID:          "req-1",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	for name, want := range map[string]string{
		"threadKey":          "deploys",
		"messageReplyOption": "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD",
		"messageId":          "client-abc",
		"requestId":          "req-1",
	} {
		if got[name] != want {
			t.Errorf("query parameter %s = %q, want %q", name, got[name], want)
		}
	}

	// An empty field is left out rather than sent empty. threadKey="" is not
	// the same request as no threadKey at all.
	h2 := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["threadKey"]; ok {
			t.Error("an empty threadKey was sent as a parameter")
		}
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})
	if _, err := sendOne(h2); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

// TestASpaceIsJoinedOntoTheBase for the user-OAuth shape, where the base is the
// API root and the space comes from the caller.
func TestASpaceIsJoinedOntoTheBase(t *testing.T) {
	var path string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = fmt.Fprint(w, `{"name":"spaces/BBBBOther/messages/CCC"}`)
	})

	client, err := New(Options{BaseURL: h.server.URL + "/v1", HTTP: h.server.Client()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.SendMessage(context.Background(), SendRequest{
		Space:   "spaces/BBBBOther",
		Message: Message{Text: "hello"},
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if path != "/v1/spaces/BBBBOther/messages" {
		t.Errorf("path = %s", path)
	}
}

// TestASpaceCannotRedirectTheRequest. The space argument is the value most
// likely to arrive from somewhere else: an alias, a resource name in a response,
// something a person was sent. The strict ^spaces/[A-Za-z0-9_-]+$ check lands
// with Milestone 3; what holds until then is that none of these reach a
// network.
func TestASpaceCannotRedirectTheRequest(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a request was made for a space that should have been refused")
	})

	for _, space := range []string{
		"https://evil.invalid/v1/spaces/AAA",
		"//evil.invalid/spaces/AAA",
		"/spaces/AAA",
		"../../../spaces/AAA",
		"spaces/AAA?key=stolen",
	} {
		if _, err := h.client.SendMessage(context.Background(), SendRequest{
			Space:   space,
			Message: Message{Text: "hello"},
		}); err == nil {
			t.Errorf("space %q was accepted", space)
		}
	}
	if h.count() != 0 {
		t.Errorf("%d requests were made, and a refusal after the request is not a refusal", h.count())
	}
}

// TestDeriveMessageIDIsStableAndUnambiguous.
//
// Stable because that is the whole point: a retrying agent that computes the
// same ID cannot double-post. Unambiguous because the obvious derivation is
// not. SPEC.md §7.6 writes it as sha256(space + body + threadKey), and plain
// concatenation makes ("ab","c") and ("a","bc") the same input, which for an
// idempotency key means two different messages sharing an ID and the second one
// being dropped by the API as a duplicate of the first.
func TestDeriveMessageIDIsStableAndUnambiguous(t *testing.T) {
	first := DeriveMessageID("spaces/AAA", "hello", "deploys")
	if second := DeriveMessageID("spaces/AAA", "hello", "deploys"); first != second {
		t.Errorf("the same send derived two IDs: %q and %q", first, second)
	}

	// The prefix is required by the API, and an ID without it is refused with a
	// message that does not mention the prefix.
	if !strings.HasPrefix(first, MessageIDPrefix) {
		t.Errorf("%q does not begin with %q", first, MessageIDPrefix)
	}
	if !regexp.MustCompile(`^client-[0-9a-f]{32}$`).MatchString(first) {
		t.Errorf("%q is not the shape the API accepts", first)
	}

	shifted := DeriveMessageID("spaces/AA", "Ahello", "deploys")
	if shifted == first {
		t.Errorf("two different sends derived the same ID, so one of them would be silently dropped")
	}

	for _, other := range []struct{ space, text, key string }{
		{"spaces/BBB", "hello", "deploys"},
		{"spaces/AAA", "hello there", "deploys"},
		{"spaces/AAA", "hello", "releases"},
		{"spaces/AAA", "hello", ""},
	} {
		if got := DeriveMessageID(other.space, other.text, other.key); got == first {
			t.Errorf("%+v derived the same ID as the original", other)
		}
	}
}

// TestAnUndecodableSuccessIsNotReportedAsASend. The message may well have been
// posted, so the failure says so rather than claiming the send did not happen.
func TestAnUndecodableSuccessIsNotReportedAsASend(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"name": `)
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("an undecodable response was reported as a successful send")
	}
	if !strings.Contains(err.Error(), "accepted") {
		t.Errorf("the failure does not say the message may have been sent:\n%v", err)
	}
	if h.count() != 1 {
		t.Errorf("made %d requests, want 1: a response that would not decode is not a reason to send again", h.count())
	}
}
