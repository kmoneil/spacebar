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

// TestASendCarriesItsQueryParameters, under the names the API uses.
//
// threadKey is deliberately not among them. The query parameter of that name is
// marked deprecated in the API reference in favour of thread.thread_key, and
// the webhook guide's own examples put it in the body, so SPEC.md §7.3 is out
// of date rather than wrong about intent. TestAThreadKeyTravelsInTheBody holds
// where it goes instead.
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
		MessageReplyOption: ReplyOrFail,
		MessageID:          "client-abc",
		RequestID:          "req-1",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	for name, want := range map[string]string{
		"messageReplyOption": ReplyOrFail,
		"messageId":          "client-abc",
		"requestId":          "req-1",
	} {
		if got[name] != want {
			t.Errorf("query parameter %s = %q, want %q", name, got[name], want)
		}
	}
	if _, ok := got["threadKey"]; ok {
		t.Errorf("threadKey was sent as a query parameter, which the API deprecated")
	}

	// An empty field is left out rather than sent empty. A message with no
	// thread key is not the same request as one with an empty thread key.
	h2 := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"threadKey", "messageReplyOption", "messageId", "requestId"} {
			if _, ok := r.URL.Query()[name]; ok {
				t.Errorf("an empty %s was sent as a parameter", name)
			}
		}
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})
	if _, err := sendOne(h2); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

// TestAThreadKeyTravelsInTheBody, as message.thread.threadKey.
func TestAThreadKeyTravelsInTheBody(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	if _, err := h.client.SendMessage(context.Background(), SendRequest{
		Message:   Message{Text: "hello"},
		ThreadKey: "deploys",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	var sent Message
	h.mu.Lock()
	body := h.bodies[0]
	h.mu.Unlock()
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("the request body is not JSON: %v\n%s", err, body)
	}
	if sent.Thread == nil || sent.Thread.ThreadKey != "deploys" {
		t.Errorf("thread.threadKey did not reach the body: %s", body)
	}
}

// TestAThreadKeyAloneStillThreads is the most important assertion in this file.
//
// The API's default, MESSAGE_REPLY_OPTION_UNSPECIFIED, is documented as "Starts
// a new thread. Using this option ignores any thread ID or threadKey that's
// included". So a caller who asks to group a message into a thread and says
// nothing else gets a new thread every time, silently, with a 200 and no
// indication that what they asked for did not happen.
//
// That is exactly the failure this tool exists not to have, so supplying a
// thread key is read as a request to thread, and the option that threads is
// what gets sent.
func TestAThreadKeyAloneStillThreads(t *testing.T) {
	for _, tc := range []struct {
		name      string
		req       SendRequest
		wantParam string
	}{
		{"a key and nothing else", SendRequest{ThreadKey: "deploys"}, ReplyFallbackToNewThread},
		{"an explicit option wins", SendRequest{ThreadKey: "deploys", MessageReplyOption: ReplyOrFail}, ReplyOrFail},
		{"no key, no option", SendRequest{}, ""},

		// An option with no key is passed through rather than dropped. It is
		// the caller's to get wrong, and silently removing it would be this
		// package deciding what they meant.
		{"an option with no key", SendRequest{MessageReplyOption: ReplyOrFail}, ReplyOrFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			var present bool
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("messageReplyOption")
				_, present = r.URL.Query()["messageReplyOption"]
				_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
			})

			tc.req.Message = Message{Text: "hello"}
			if _, err := h.client.SendMessage(context.Background(), tc.req); err != nil {
				t.Fatalf("SendMessage: %v", err)
			}
			if got != tc.wantParam {
				t.Errorf("messageReplyOption = %q, want %q", got, tc.wantParam)
			}
			if tc.wantParam == "" && present {
				t.Errorf("messageReplyOption was sent empty")
			}
		})
	}
}

// TestAThreadKeyDoesNotDiscardAThreadName. A caller who set both has said two
// things and neither is this package's to drop.
func TestAThreadKeyDoesNotDiscardAThreadName(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	if _, err := h.client.SendMessage(context.Background(), SendRequest{
		Message:   Message{Text: "hello", Thread: &Thread{Name: "spaces/AAAATestSpace/threads/CCC"}},
		ThreadKey: "deploys",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	var sent Message
	h.mu.Lock()
	body := h.bodies[0]
	h.mu.Unlock()
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("the request body is not JSON: %v", err)
	}
	if sent.Thread == nil || sent.Thread.Name != "spaces/AAAATestSpace/threads/CCC" || sent.Thread.ThreadKey != "deploys" {
		t.Errorf("the thread was overwritten rather than merged: %s", body)
	}
}

// TestACardIsCarriedThroughUnchanged.
//
// A card is a deep tree with its own schema, so it is passed through as raw
// JSON rather than modelled: every field of a struct written here would be a
// guess, and a field this tool had not heard of would be silently dropped from
// somebody's message.
func TestACardIsCarriedThroughUnchanged(t *testing.T) {
	card := `[{"cardId":"deploy","card":{"header":{"title":"Deployed"},"sections":[{"widgets":[{"textParagraph":{"text":"v1.2.3"}}]}]}}]`

	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	var cards []json.RawMessage
	if err := json.Unmarshal([]byte(card), &cards); err != nil {
		t.Fatalf("the fixture is not JSON: %v", err)
	}
	if _, err := h.client.SendMessage(context.Background(), SendRequest{
		Message: Message{CardsV2: cards},
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	h.mu.Lock()
	body := h.bodies[0]
	h.mu.Unlock()
	if !strings.Contains(body, `"cardsV2":`) {
		t.Errorf("the card was not sent under cardsV2: %s", body)
	}
	if !strings.Contains(body, `"textParagraph"`) || !strings.Contains(body, `"v1.2.3"`) {
		t.Errorf("a field this tool does not model was dropped: %s", body)
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
