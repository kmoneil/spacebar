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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/output"
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

// TestASpaceNameIsCheckedAgainstWhatWouldChangeTheRequest.
//
// The table the recon for m3-05 proved and no test held. Percent encoding is
// the interesting half: `%2f` is refused because `%` is outside the character
// class, not because anything decodes it, so `%2F` and `%25` and every other
// spelling of an encoded slash fail for one reason rather than three. A rule
// that decoded first would need to decode exactly as many times as the far end
// does, and nothing tells it how many that is.
func TestASpaceNameIsCheckedAgainstWhatWouldChangeTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		space string
		ok    bool
	}{
		{"a real one", "spaces/AAAAExampleOne", true},
		{"a direct message", "spaces/AAAAExampleDM", true},
		{"underscore and hyphen", "spaces/A_a-1", true},

		{"empty", "", false},
		{"no prefix", "AAA", false},
		{"a traversal", "spaces/../../etc", false},
		{"a second segment", "spaces/AAA/messages", false},
		{"an encoded slash lower", "spaces/AAA%2f..", false},
		{"an encoded slash upper", "spaces/AAA%2F..", false},
		{"an encoded percent", "spaces/AAA%252f", false},
		{"a query", "spaces/AAA?key=stolen", false},
		{"a fragment", "spaces/AAA#f", false},
		{"an absolute URL", "https://elsewhere.invalid/v1/spaces/AAA", false},
		{"a scheme-relative host", "//elsewhere.invalid/spaces/AAA", false},
		{"a backslash", `spaces\AAA`, false},
		{"a newline", "spaces/AAA\nevil", false},
		{"a tab", "spaces/AAA\tevil", false},
		{"a NUL", "spaces/AAA\x00evil", false},
		{"a trailing space", "spaces/AAA ", false},
		{"a dot, which a message name allows and this does not", "spaces/AAA.BBB", false},
		{"non-ASCII", "spaces/café", false},
		{"the prefix alone", "spaces/", false},
		{"only the prefix without a slash", "spaces", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSpaceName(tc.space)
			if tc.ok && err != nil {
				t.Errorf("CheckSpaceName(%q) = %v, want it accepted", tc.space, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("CheckSpaceName(%q) was accepted", tc.space)
			}
		})
	}
}

// FuzzASpaceNameThatIsAcceptedIsSafeUnescaped states the property the validator
// exists for, which nothing else in the tree says.
//
// FuzzAPathStaysOnTheBase is a different claim. It says no path escapes the
// base, which is true whatever the pattern accepts, because the join defends
// itself. This says the thing that makes escaping the second layer rather than
// the only one: a name the pattern accepts needs no escaping at all, so a code
// path that forgot to escape it would still request the space that was checked.
//
// Stated as a byte comparison rather than as a list of dangerous characters. A
// list is what somebody thought of; this fails for any character that survives
// the pattern and then means something to a URL.
func FuzzASpaceNameThatIsAcceptedIsSafeUnescaped(f *testing.F) {
	for _, seed := range []string{
		"spaces/AAAAExampleOne", "spaces/A_a-1", "spaces/AAA", "spaces/",
		"spaces/AAA%2f..", "spaces/../../etc", "spaces/AAA?key=stolen",
		"spaces/AAA#f", "spaces/AAA\x00evil", "spaces/café", "AAA", "",
		"spaces/AAA/messages", "//elsewhere.invalid/spaces/AAA", `spaces\AAA`,
	} {
		f.Add(seed)
	}

	client, err := New(Options{BaseURL: "https://chat.example/v1?key=" + testKey})
	if err != nil {
		f.Fatalf("New: %v", err)
	}

	f.Fuzz(func(t *testing.T, space string) {
		if CheckSpaceName(space) != nil {
			return
		}

		// The two shapes an accepted name takes on the way to a request: the
		// space alone, as spaces get reads it, and the space with a collection
		// under it, as messages list and spaces members build it.
		for _, path := range []string{space, space + "/messages", space + "/members"} {
			target, err := client.resolve(Request{Method: http.MethodGet, Path: path})
			if err != nil {
				t.Fatalf("a name that passed the check was refused as a path: %q: %v", path, err)
			}

			// Byte-identical, unescaped. If the accepted name had needed
			// escaping, EscapedPath would differ from the concatenation, and
			// the request would name something other than what was checked.
			want := "/v1/" + path
			if target.EscapedPath() != want {
				t.Fatalf("accepted name %q was altered on the way to a request:\n got %q\nwant %q",
					space, target.EscapedPath(), want)
			}
			if target.Path != want {
				t.Fatalf("accepted name %q decodes to a different path:\n got %q\nwant %q",
					space, target.Path, want)
			}

			// Nothing the name contained became a query, a fragment, or a
			// different host, and the credential is still the profile's own.
			if target.Host != "chat.example" || target.Scheme != "https" {
				t.Fatalf("accepted name %q reached %s://%s", space, target.Scheme, target.Host)
			}
			if target.Fragment != "" || target.RawFragment != "" {
				t.Fatalf("accepted name %q produced a fragment %q", space, target.Fragment)
			}
			if target.Query().Get("key") != testKey {
				t.Fatalf("accepted name %q changed the credential to %q", space, target.Query().Get("key"))
			}
			if len(target.Query()) != 1 {
				t.Fatalf("accepted name %q added a query parameter: %v", space, target.Query())
			}
		}
	})
}

// FuzzAMessageNameThatIsAcceptedIsSafeUnescaped, the same property for the
// other pattern, which is worth stating separately because it admits a dot and
// the space rule does not. A pattern that admits more characters is a pattern
// with more ways to be wrong.
func FuzzAMessageNameThatIsAcceptedIsSafeUnescaped(f *testing.F) {
	for _, seed := range []string{
		"spaces/AAA/messages/nMs6.nMs6", "spaces/AAA/messages/client-0123abcd",
		"spaces/AAA/messages/BBB", "spaces/AAA/messages/", "spaces/AAA",
		"spaces/AAA/messages/../../../etc", "spaces/AAA/messages/B%2fC",
		"spaces/AAA/messages/B.C.D", "spaces/AAA/messages/..", "",
	} {
		f.Add(seed)
	}

	client, err := New(Options{BaseURL: "https://chat.example/v1?key=" + testKey})
	if err != nil {
		f.Fatalf("New: %v", err)
	}

	f.Fuzz(func(t *testing.T, message string) {
		if CheckMessageName(message) != nil {
			return
		}

		target, err := client.resolve(Request{Method: http.MethodGet, Path: message})
		if err != nil {
			t.Fatalf("a name that passed the check was refused as a path: %q: %v", message, err)
		}

		want := "/v1/" + message
		if target.EscapedPath() != want || target.Path != want {
			t.Fatalf("accepted name %q was altered on the way to a request:\n got %q (raw %q)\nwant %q",
				message, target.Path, target.EscapedPath(), want)
		}
		if target.Host != "chat.example" || target.Scheme != "https" {
			t.Fatalf("accepted name %q reached %s://%s", message, target.Scheme, target.Host)
		}
		if target.Query().Get("key") != testKey || len(target.Query()) != 1 {
			t.Fatalf("accepted name %q changed the query to %v", message, target.Query())
		}

		// A dot is admitted, and "." and ".." are the two that would move a
		// path. The pattern has to keep them out even though it allows the
		// character they are made of.
		if strings.HasSuffix(message, "/.") || strings.HasSuffix(message, "/..") {
			t.Fatalf("accepted name %q ends in a path element that would move the request", message)
		}
	})
}

// TestAMessageIDMayBeginWithAnythingButDots.
//
// The first fix for the ".." hole required a leading alphanumeric, which would
// have refused a message that exists: the API's identifiers look base64url and
// that alphabet contains - and _. A validator that is too narrow presents as
// this tool being unable to open somebody's message, and it gets found by a
// user rather than by a test. What is refused is only what is dangerous.
func TestAMessageIDMayBeginWithAnythingButDots(t *testing.T) {
	for _, tc := range []struct {
		id string
		ok bool
	}{
		// The shapes the API and this tool actually produce.
		{"nMs6.nMs6", true},
		{"XdvpVBoTxdM.XdvpVBoTxdM", true},
		{"client-0123abcd", true},

		// base64url, which is what the identifiers look like. Refusing these
		// would be a bug reported as "spacebar cannot open this message".
		{"-leadingHyphen", true},
		{"_leadingUnderscore", true},
		{"-_-", true},
		{".leadingDot", true},
		{"..twoDotsThenText", true},

		// The two that are path elements rather than names, and the general
		// case of them. These are what the pattern exists to keep out.
		{".", false},
		{"..", false},
		{"...", false},
		{"", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			name := "spaces/AAA/messages/" + tc.id
			err := CheckMessageName(name)
			if tc.ok && err != nil {
				t.Errorf("CheckMessageName(%q) = %v, want it accepted", name, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("CheckMessageName(%q) was accepted", name)
			}
		})
	}
}

// TestEveryMutationHonoursDryRunAndSendsNothing.
//
// The CLI's walk cannot reach these three. Its tests can configure exactly one
// kind of profile, a webhook, and a webhook has none of the three capabilities,
// so the gate refuses before a request is ever built. That refusal is worth
// asserting and is a different claim.
//
// So this one is held where it is implemented: the stop is on the line before
// the send inside the client, so no command can forget it and no transport can
// route around it. The server fails the test if it is reached.
func TestEveryMutationHonoursDryRunAndSendsNothing(t *testing.T) {
	const message = "spaces/AAA/messages/BBB"

	for _, tc := range []struct {
		name   string
		method string
		call   func(*Client) error
	}{
		{"edit", http.MethodPatch, func(c *Client) error {
			_, err := c.EditMessage(context.Background(), EditRequest{Message: message, Text: "the new text"})
			return err
		}},
		{"delete", http.MethodDelete, func(c *Client) error {
			return c.DeleteMessage(context.Background(), message)
		}},
		{"react", http.MethodPost, func(c *Client) error {
			_, err := c.React(context.Background(), ReactRequest{Message: message, Emoji: "👍"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReader(t, func(http.ResponseWriter, *http.Request) {
				t.Errorf("a dry run of %s reached the network", tc.name)
			})
			r.client.dryRun = true

			err := tc.call(r.client)
			dry, ok := errors.AsType[*DryRun](err)
			if !ok {
				t.Fatalf("a dry run returned %v, want a *DryRun", err)
			}
			if r.count() != 0 {
				t.Fatalf("a dry run made %d requests", r.count())
			}
			if dry.Request.Method != tc.method {
				t.Errorf("method = %q, want %q", dry.Request.Method, tc.method)
			}
			if !strings.Contains(dry.Request.URL, message) {
				t.Errorf("url = %q, want the message in it", dry.Request.URL)
			}
		})
	}
}

// TestAnEditAlwaysCarriesItsUpdateMask.
//
// The API takes a PATCH with no mask as a request to update nothing and answers
// 200, so a caller who omitted it would be told the edit worked and would find
// the old text still there. That is the worst shape a failure can have:
// successful, silent, and wrong. It is why the mask is not a parameter of this
// call.
func TestAnEditAlwaysCarriesItsUpdateMask(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"name": "spaces/AAA/messages/BBB", "text": "the new text"}`)
	})

	edited, err := r.client.EditMessage(context.Background(), EditRequest{
		Message: "spaces/AAA/messages/BBB",
		Text:    "the new text",
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if edited.Text != "the new text" {
		t.Errorf("the response was not decoded: %+v", edited)
	}

	paths := r.paths()
	if len(paths) != 1 || !strings.Contains(paths[0], "updateMask=text") {
		t.Errorf("paths = %q, want one carrying updateMask=text", paths)
	}
}

// TestAReactionIsAnObjectRatherThanAString.
//
// Measured before it could be assumed: {"emoji": ":thumbsup:"} is refused at
// the proto level with "Invalid value at 'reaction.emoji'
// (google.chat.v1.Emoji)", and {"emoji": {"unicode": "..."}} parses. So a
// shortcode cannot be passed through, and it is refused here rather than turned
// into a 400 quoting a proto type at somebody who typed what every chat client
// accepts.
func TestAReactionIsAnObjectRatherThanAString(t *testing.T) {
	var body string
	r := newReader(t, func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
		_, _ = fmt.Fprint(w, `{"name": "spaces/AAA/messages/BBB/reactions/CCC"}`)
	})

	if _, err := r.client.React(context.Background(), ReactRequest{
		Message: "spaces/AAA/messages/BBB",
		Emoji:   "👍",
	}); err != nil {
		t.Fatalf("React: %v", err)
	}
	// The encoder's trailing newline is part of what goes on the wire, and is
	// trimmed here rather than asserted away: what matters is the object, and
	// the send path has its own test for the exact bytes.
	if strings.TrimSpace(body) != `{"emoji":{"unicode":"👍"}}` {
		t.Errorf("body = %q", body)
	}
	if paths := r.paths(); len(paths) != 1 || !strings.Contains(paths[0], "/messages/BBB/reactions") {
		t.Errorf("paths = %q", paths)
	}
}

// TestAShortcodeIsRefusedBeforeTheRequest, because the alternative is a 400
// naming a protobuf type, for a value every chat client in the world accepts.
func TestAShortcodeIsRefusedBeforeTheRequest(t *testing.T) {
	for _, bad := range []string{":thumbsup:", ":+1:", ""} {
		if err := CheckEmoji(bad); err == nil {
			t.Errorf("CheckEmoji(%q) was accepted, and the API refuses it", bad)
		}
	}

	// Narrow on purpose: it fires on the shape somebody types by habit and not
	// on anything containing a colon.
	for _, good := range []string{"👍", "🎉", "a:b"} {
		if err := CheckEmoji(good); err != nil {
			t.Errorf("CheckEmoji(%q) = %v", good, err)
		}
	}
}

// FuzzTheSpaceOfAMessageIsAlwaysASpaceName states the guarantee as a property
// rather than as the list of cases somebody thought of.
//
// For any string, either SpaceOfMessage refuses it or what comes back is a
// space name that CheckSpaceName accepts and that the message actually begins
// with. The second half is what makes it safe to hand to an allowlist: a value
// that passed the check but named a different space would be an allowlist
// comparing against the wrong thing, which is worse than no allowlist because
// the operator believes it worked.
func FuzzTheSpaceOfAMessageIsAlwaysASpaceName(f *testing.F) {
	f.Add("spaces/AAA/messages/BBB")
	f.Add("spaces/AAA/messages/BBB.CCC")
	f.Add("spaces/AAA/messages/BBB/messages/DDD")
	f.Add("spaces/../messages/BBB")
	f.Add("spaces/AAA/messages/")
	f.Add("/messages/BBB")
	f.Add("spaces/AAA")
	f.Add("")

	f.Fuzz(func(t *testing.T, message string) {
		space, err := SpaceOfMessage(message)
		if err != nil {
			if space != "" {
				t.Errorf("a refusal still returned %q", space)
			}
			return
		}
		if err := CheckSpaceName(space); err != nil {
			t.Errorf("SpaceOfMessage(%q) returned %q, which is not a space name: %v", message, space, err)
		}
		if !strings.HasPrefix(message, space+"/messages/") {
			t.Errorf("SpaceOfMessage(%q) returned %q, which the message does not begin with", message, space)
		}
	})
}

// TestSendChecksItsOwnSpaceName.
//
// Every other write in this package checks the resource name it is about to put
// in a path. This one did not, and the comment saying why named a milestone
// that had shipped four milestones earlier. Nothing was reachable through the
// gap, because both transports check before calling, which is exactly the
// arrangement this repository refuses elsewhere: a first layer that needs the
// layer below it, and its callers remembering, is not a first layer.
//
// Empty stays allowed, and only here. That is the webhook, whose URL is already
// the messages endpoint for one space.
func TestSendChecksItsOwnSpaceName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		space string
		ok    bool
	}{
		{"a webhook, which names no space", "", true},
		{"a real space", "spaces/AAAATestSpace", true},

		{"a walk upwards", "spaces/../../etc", false},
		{"another host", "https://evil.example/v1/spaces/AAAA", false},
		{"a second segment", "spaces/AAAA/messages", false},
		{"not a space at all", "AAAATestSpace", false},
		{"a control character", "spaces/AAAA\nBBBB", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSendTarget(tc.space)
			if tc.ok && err != nil {
				t.Fatalf("CheckSendTarget(%q) = %v, want it accepted", tc.space, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("CheckSendTarget(%q) was accepted", tc.space)
			}
		})
	}
}

// TestASendRefusesABadSpaceWithoutAskingTheAPI holds the same claim through the
// method, against a server that fails the test if it is reached.
//
// The value matters, and the obvious one proves nothing. `spaces/../../etc` was
// already refused without this check, by checkRelative at the join, which is
// exactly the second layer the card said this was leaning on: the test passes on
// the broken build and says the wrong thing about why.
//
// This one is refused by the space rule and by nothing else. It has no `..`, no
// separator to add a segment and no scheme, so checkRelative allows it and
// sameOrigin allows it: it joins onto the base as /v1/AAAATestSpace/messages,
// which is a request to a path that is not an endpoint, carrying this profile's
// credential. That is what the first layer is for.
func TestASendRefusesABadSpaceWithoutAskingTheAPI(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a send with a bad space reached the network: %s %s", r.Method, r.URL.Path)
	})

	if _, err := h.client.SendMessage(context.Background(), SendRequest{
		Space:   "AAAATestSpace",
		Message: Message{Text: "hello"},
	}); err == nil {
		t.Fatal("a send with a bad space was not refused")
	}
	if h.count() != 0 {
		t.Errorf("the refusal arrived after %d requests", h.count())
	}
}

// TestAMessageIDTheAPIWillNotTakeIsRefusedHereRatherThanThere.
//
// The CLI refused an id without the prefix and the MCP tool did not, so one
// value was a usage error through one adapter and a 400 through the other.
// SPEC.md §4 says neither adapter is where a decision gets made.
//
// It matters more than a better error message. A message id is what marks a
// POST safe to replay, so an id the API will reject is a request marked
// replayable on the strength of a value that was never going to work.
func TestAMessageIDTheAPIWillNotTakeIsRefusedHereRatherThanThere(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
		ok   bool
	}{
		{"none, which is the ordinary send", "", true},
		{"a derived one", DeriveMessageID("spaces/AAAA", "hi", ""), true},
		{"one somebody chose with the prefix", MessageIDPrefix + "deploy-42", true},

		{"one without the prefix", "deploy-42", false},
		{"one that only contains the prefix elsewhere", "my-client-thing", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckMessageID(tc.id)
			if tc.ok != (err == nil) {
				t.Fatalf("CheckMessageID(%q) = %v, accepted=%v want accepted=%v", tc.id, err, err == nil, tc.ok)
			}
		})
	}

	// And through the method, so that no adapter can reach the network with one.
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a send with a bad message id reached the network: %s %s", r.Method, r.URL.Path)
	})
	if _, err := h.client.SendMessage(context.Background(), SendRequest{
		Message:   Message{Text: "hello"},
		MessageID: "deploy-42",
	}); err == nil {
		t.Fatal("a send with a bad message id was not refused")
	}
	if h.count() != 0 {
		t.Errorf("the refusal arrived after %d requests", h.count())
	}
}

// TestALengthIsCheckedAgainstWhatWasMeasuredAndNotARoundNumber.
//
// The two numbers are a hundred bytes apart and the real limit is somewhere
// between them, so the only question this check has to answer correctly is
// which side of it a body is definitely on. A body seen to be accepted has to
// pass, and one seen to be refused has to not, and everything between them is
// the API's to answer as it does today.
//
// The row that matters is the first. A check set at a round 32,000 would fail
// it, refuse a message somebody could have sent, and be discovered by whoever
// tried to send one.
func TestALengthIsCheckedAgainstWhatWasMeasuredAndNotARoundNumber(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes int
		ok    bool
	}{
		{"the largest body seen to be accepted", acceptedTextBytes, true},
		{"one byte under the smallest seen to be refused", tooLongTextBytes - 1, true},

		{"the smallest body seen to be refused", tooLongTextBytes, false},
		{"well past it, which is the case somebody will actually hit", tooLongTextBytes * 16, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckMessageText(strings.Repeat("a", tc.bytes))
			if tc.ok != (err == nil) {
				t.Fatalf("CheckMessageText(%d bytes) = %v, accepted=%v want accepted=%v",
					tc.bytes, err, err == nil, tc.ok)
			}
		})
	}
}

// TestABodyTheAPIWillRefuseNeverReachesIt.
//
// Counted rather than read off the error, for the reason every other refusal in
// this package is counted: a refusal that arrives after the POST carries the
// same message as one that arrives before it, and only one of them is a
// pre-flight check. It is a POST with no message ID, so a retry of it is a
// second message in a space, which is why the request not being made is the
// assertion rather than the exit code being right.
//
// The exit code is checked too, and it is exit 2 rather than exit 3, because
// nothing failed: the caller handed over a body that cannot be sent, and the
// fix is theirs.
func TestABodyTheAPIWillRefuseNeverReachesIt(t *testing.T) {
	h := newHarness(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("an over-long body reached the network: %s %s", r.Method, r.URL.Path)
	})

	_, err := h.client.SendMessage(context.Background(), SendRequest{
		Message: Message{Text: strings.Repeat("a", tooLongTextBytes)},
	})
	if err == nil {
		t.Fatal("an over-long body was not refused")
	}
	if h.count() != 0 {
		t.Errorf("the refusal arrived after %d requests", h.count())
	}
	if got := output.ExitCodeOf(err); got != output.ExitUsage {
		t.Errorf("exit = %d, want %d", got, output.ExitUsage)
	}

	// The number is in the message, because the whole value of failing here
	// rather than at the API is that somebody is told what the limit is.
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", tooLongTextBytes)) {
		t.Errorf("the failure does not name the limit it is enforcing:\n%v", err)
	}
}

// TestABodyTheAPIAcceptedStillSends is the other half, and the half a check
// like this one gets wrong.
//
// Nothing about a length limit is dangerous to get slightly too large. Getting
// it slightly too small refuses a message that would have arrived, from a
// person who has no way to tell that the tool and not the API decided it.
func TestABodyTheAPIAcceptedStillSends(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	if _, err := h.client.SendMessage(context.Background(), SendRequest{
		Message: Message{Text: strings.Repeat("a", acceptedTextBytes)},
	}); err != nil {
		t.Fatalf("a body measured as accepted by the API was refused here: %v", err)
	}
	if h.count() != 1 {
		t.Errorf("made %d requests, want 1", h.count())
	}
}

// TestAnEditIsHeldToTheSameLengthAsASend.
//
// The same field with the same cap, so a body that cannot be sent cannot be
// edited into place either. Held here rather than left to the API because the
// alternative is that the two paths disagree about the same value, which is the
// gap CheckMessageID was moved into this package to close.
func TestAnEditIsHeldToTheSameLengthAsASend(t *testing.T) {
	r := newReader(t, func(_ http.ResponseWriter, req *http.Request) {
		t.Errorf("an over-long edit reached the network: %s %s", req.Method, req.URL.Path)
	})

	if _, err := r.client.EditMessage(context.Background(), EditRequest{
		Message: "spaces/AAA/messages/BBB",
		Text:    strings.Repeat("a", tooLongTextBytes),
	}); err == nil {
		t.Fatal("an over-long edit was not refused")
	}
	if r.count() != 0 {
		t.Errorf("the refusal arrived after %d requests", r.count())
	}
}

// FuzzAMediaNameThatIsAcceptedIsSafeUnescaped is the third of these, and it
// exists because the pattern's own comment already makes the claim: "What the
// pattern accepts is safe in a path unescaped, which is the same promise
// CheckSpaceName and CheckMessageName make." Both of those promises have a
// property test holding them and this one did not, which is the shape of a
// comment that is true until somebody widens a regexp.
//
// Worth stating separately rather than folded into the other two, because this
// name is the one nobody chose. A space name is typed or resolved, a message
// name is typed or read out of a message, and an attachment resource name is
// whatever the API put in attachmentDataRef and is pasted onward by a script.
// It is also the only one of the three that is base64, so the alphabet
// question is live in a way it is not for the others: `+` and `/` are refused
// rather than escaped, and `/` is refused because it would add a path segment
// to a value the far end chose.
func FuzzAMediaNameThatIsAcceptedIsSafeUnescaped(f *testing.F) {
	for _, seed := range []string{
		"ClpzcGFjZXMvQUFBQUV4YW1wbGVPbmUvbWVzc2FnZXMv", "AAAA", "a-b_c",
		"AAAA=", "AAAA==", "=", "", "a/b", "a+b", "..", ".", "a.b",
		"../../etc/passwd", "a%2fb", "a?key=stolen", "a#f", "a\x00b",
		"café", "a b", "media/x",
	} {
		f.Add(seed)
	}

	client, err := New(Options{BaseURL: "https://chat.example/v1?key=" + testKey})
	if err != nil {
		f.Fatalf("New: %v", err)
	}

	f.Fuzz(func(t *testing.T, name string) {
		if CheckMediaName(name) != nil {
			return
		}

		// The one path this name ever takes, built the way Download builds it.
		query := url.Values{}
		query.Set("alt", "media")
		target, err := client.resolve(Request{
			Method: http.MethodGet,
			Path:   "media/" + name,
			Query:  query,
		})
		if err != nil {
			t.Fatalf("a name that passed the check was refused as a path: %q: %v", name, err)
		}

		want := "/v1/media/" + name
		if target.EscapedPath() != want || target.Path != want {
			t.Fatalf("accepted name %q was altered on the way to a request:\n got %q (raw %q)\nwant %q",
				name, target.Path, target.EscapedPath(), want)
		}
		if target.Host != "chat.example" || target.Scheme != "https" {
			t.Fatalf("accepted name %q reached %s://%s", name, target.Scheme, target.Host)
		}
		if target.Fragment != "" || target.RawFragment != "" {
			t.Fatalf("accepted name %q produced a fragment %q", name, target.Fragment)
		}

		// alt=media and the profile's own credential, and nothing the name
		// added. A `?` surviving the pattern would land here.
		got := target.Query()
		if got.Get("key") != testKey || got.Get("alt") != "media" || len(got) != 2 {
			t.Fatalf("accepted name %q changed the query to %v", name, got)
		}
	})
}
