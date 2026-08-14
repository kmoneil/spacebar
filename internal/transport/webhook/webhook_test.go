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

package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
)

const (
	testKey   = "AIzaSyTestKeyNotARealOne0123456789"
	testToken = "sQ7testTokenNotARealOne0123456789"
)

// liveWebhook is the URL shape confirmed against the current webhook guide:
// the messages endpoint for one space, with the credential in the query.
func liveWebhook(base, space string) string {
	return fmt.Sprintf("%s/v1/%s/messages?key=%s&token=%s", base, space, testKey, testToken)
}

// recordingLogger stands in for the renderer at --verbose.
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *recordingLogger) Logf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, a...))
}

func (l *recordingLogger) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

type harness struct {
	transport *Transport
	log       *recordingLogger

	mu       sync.Mutex
	requests int
	bodies   []string
}

func newHarness(t *testing.T, handler http.HandlerFunc) *harness {
	t.Helper()

	h := &harness{log: &recordingLogger{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		h.mu.Lock()
		h.requests++
		h.bodies = append(h.bodies, string(body))
		h.mu.Unlock()

		handler(w, r)
	}))
	t.Cleanup(server.Close)

	// http on 127.0.0.1, which is the one exception to "a credential travels
	// over https" and exists so that this rule can be tested at all.
	built, err := New(Options{
		Profile: "alerts",
		URL:     liveWebhook(server.URL, "spaces/AAAATestSpace"),
		Log:     h.log,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h.transport = built
	return h
}

func (h *harness) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requests
}

func writeAPIError(w http.ResponseWriter, code int, status, message, reason string) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"message":%q,"status":%q,"details":[
		{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":%q,"domain":"googleapis.com"}]}}`,
		code, message, status, reason)
}

// TestASendNeedsNoOAuthAtAll is the point of the whole milestone.
//
// No token, no client ID, no consent screen, no administrator. The URL is the
// entire credential and it travels in the query string, which is why every
// other rule in this package exists.
func TestASendNeedsNoOAuthAtAll(t *testing.T) {
	var gotAuth, gotPath string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB","text":"hello"}`)
	})

	sent, err := h.transport.Send(context.Background(), chat.SendRequest{Message: chat.Message{Text: "hello"}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("an Authorization header was sent: %q", gotAuth)
	}
	if gotPath != "/v1/spaces/AAAATestSpace/messages" {
		t.Errorf("path = %q", gotPath)
	}
	if sent.Name != "spaces/AAAATestSpace/messages/BBB" {
		t.Errorf("the response was not decoded: %+v", sent)
	}
}

// TestTheSpaceComesFromTheURL, rather than being configured beside it, because
// two copies of the same fact can disagree and only one of them is what the
// request will reach.
func TestTheSpaceComesFromTheURL(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {})
	if got := h.transport.Space(); got != "spaces/AAAATestSpace" {
		t.Errorf("Space = %q", got)
	}
	if got := h.transport.Profile(); got != "alerts" {
		t.Errorf("Profile = %q", got)
	}
	if got := h.transport.Kind(); got != config.TransportWebhook {
		t.Errorf("Kind = %q", got)
	}
}

// TestAnotherSpaceIsRefusedBeforeTheRequest.
//
// A webhook is issued for one space and is the only thing that authenticates
// the request, so there is no version of this that reaches another one. Sending
// anyway would mean somebody who typed the wrong target watched their message
// arrive somewhere, in a space full of people, with a success code saying it
// went where they asked.
func TestAnotherSpaceIsRefusedBeforeTheRequest(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a send to another space reached the network")
	})

	_, err := h.transport.Send(context.Background(), chat.SendRequest{
		Space:   "spaces/BBBBSomewhereElse",
		Message: chat.Message{Text: "hello"},
	})
	if err == nil {
		t.Fatal("a send to another space was allowed")
	}
	if got := output.ExitCodeOf(err); got != output.ExitUsage {
		t.Errorf("exit code = %d, want %d", got, output.ExitUsage)
	}
	if h.count() != 0 {
		t.Errorf("%d requests were made, and a refusal after the request is not a refusal", h.count())
	}

	for _, want := range []string{"alerts", "spaces/AAAATestSpace", "spaces/BBBBSomewhereElse"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%v", want, err)
		}
	}
}

// TestItsOwnSpaceIsAcceptedEitherWay. Naming the space this webhook is for is
// correct, and so is leaving it off, because the URL already says.
func TestItsOwnSpaceIsAcceptedEitherWay(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/spaces/AAAATestSpace/messages" {
			t.Errorf("the space was appended to a URL that already had one: %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	for _, space := range []string{"", "spaces/AAAATestSpace"} {
		if _, err := h.transport.Send(context.Background(), chat.SendRequest{
			Space:   space,
			Message: chat.Message{Text: "hello"},
		}); err != nil {
			t.Errorf("Send with space %q: %v", space, err)
		}
	}
	if h.count() != 2 {
		t.Errorf("made %d requests, want 2", h.count())
	}
}

// TestARubbishTargetIsRefusedAsOne. A target that is not a space name at all
// gets the message about space names rather than the one about webhooks, since
// the two are different mistakes.
func TestARubbishTargetIsRefusedAsOne(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a request was made for a target that should have been refused")
	})

	for _, space := range []string{"https://evil.invalid/spaces/AAAA", "../../spaces/AAAA", "spaces/AAAA/messages", "AAAA"} {
		_, err := h.transport.Send(context.Background(), chat.SendRequest{
			Space:   space,
			Message: chat.Message{Text: "hello"},
		})
		if err == nil {
			t.Errorf("target %q was accepted", space)
		}
	}
	if h.count() != 0 {
		t.Errorf("%d requests were made", h.count())
	}
}

// TestTheCapabilitiesComeFromTheOneMatrix, rather than being restated here
// where they could drift out of agreement with SPEC.md §8.1.
func TestTheCapabilitiesComeFromTheOneMatrix(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {})
	caps := h.transport.Capabilities()

	if !caps.Has(transport.CanSend) || !caps.Has(transport.CanSendCards) || !caps.Has(transport.CanThread) {
		t.Errorf("a webhook cannot do what it can do: %+v", caps)
	}
	for _, cannot := range []transport.Capability{
		transport.CanRead, transport.CanEdit, transport.CanDelete,
		transport.CanReact, transport.CanUpload, transport.CanListSpaces, transport.CanResolveDM,
	} {
		if caps.Has(cannot) {
			t.Errorf("a webhook claims it can do something it cannot: %+v", caps)
		}
	}
}

// TestEveryCapabilityItLacksRefusesWithoutARequest.
//
// The Transport interface has one method at Milestone 2, so there are no read
// methods to return ErrUnsupported from yet. What holds the rule today is the
// gate, run against a transport wired to a real server: nothing is dialled, and
// the one capability this profile does have still goes through, so the test
// cannot pass by refusing everything.
func TestEveryCapabilityItLacksRefusesWithoutARequest(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	for _, want := range []transport.Capability{
		transport.CanRead, transport.CanEdit, transport.CanDelete,
		transport.CanReact, transport.CanUpload, transport.CanListSpaces, transport.CanResolveDM,
	} {
		err := transport.Require(h.transport, "tail", want)
		if err == nil {
			t.Errorf("a webhook was allowed %s", want)
			continue
		}
		if got := output.ExitCodeOf(err); got != output.ExitUnsupported {
			t.Errorf("exit code = %d, want %d", got, output.ExitUnsupported)
		}
		if !errors.Is(err, transport.ErrUnsupported) {
			t.Errorf("the refusal is not ErrUnsupported: %v", err)
		}
	}
	if h.count() != 0 {
		t.Errorf("%d requests were made by refused operations", h.count())
	}

	if err := transport.Require(h.transport, "send", transport.CanSend); err != nil {
		t.Fatalf("a webhook was refused a send: %v", err)
	}
	if _, err := h.transport.Send(context.Background(), chat.SendRequest{Message: chat.Message{Text: "hello"}}); err != nil {
		t.Fatalf("the send it is allowed failed: %v", err)
	}
	if h.count() != 1 {
		t.Errorf("the transport never dialled, so this test would pass with the gate removed")
	}
}

// TestA403NamesTheLikelyCause, which is Appendix A.10 and the reason this
// mapping exists.
//
// Somebody reading PERMISSION_DENIED concludes their URL is wrong and copies it
// again, which changes nothing, because what blocked them is an administrator
// setting they cannot see.
func TestA403NamesTheLikelyCause(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusForbidden, "PERMISSION_DENIED",
			"The caller does not have permission", "IAM_PERMISSION_DENIED")
	})

	_, err := h.transport.Send(context.Background(), chat.SendRequest{Message: chat.Message{Text: "hello"}})
	if err == nil {
		t.Fatal("a 403 was reported as a successful send")
	}
	for _, want := range []string{"Chat apps", "organizational unit", "alerts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the 403 message is missing %q:\n%v", want, err)
		}
	}
	if h.count() != 1 {
		t.Errorf("made %d requests, want 1: a 403 is not retried", h.count())
	}
}

// TestABadKeyIsExplainedAsABadURL. A live probe of a webhook-shaped URL with a
// bad key answers 400 INVALID_ARGUMENT with reason API_KEY_INVALID, not 401 or
// 403, and it is the most likely thing to go wrong with a webhook profile.
func TestABadKeyIsExplainedAsABadURL(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"API key not valid. Please pass a valid API key.", "API_KEY_INVALID")
	})

	_, err := h.transport.Send(context.Background(), chat.SendRequest{Message: chat.Message{Text: "hello"}})
	if err == nil {
		t.Fatal("a 400 was reported as a successful send")
	}
	if !strings.Contains(err.Error(), "Copy it again") {
		t.Errorf("the message does not say what to do:\n%v", err)
	}
}

// TestTheURLReachesNoLog. It is the credential, and it is the most likely thing
// this tool leaks, because every instinct says a URL is safe to write down.
func TestTheURLReachesNoLog(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusForbidden, "PERMISSION_DENIED", "Denied", "IAM_PERMISSION_DENIED")
	})

	_, err := h.transport.Send(context.Background(), chat.SendRequest{Message: chat.Message{Text: "hello"}})
	if err == nil {
		t.Fatal("a 403 was reported as a successful send")
	}

	for what, text := range map[string]string{"the verbose log": h.log.text(), "the failure": err.Error()} {
		for _, secret := range []string{testKey, testToken} {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds a credential:\n%s", what, text)
			}
		}
	}
	// Redacted rather than omitted, and the space survives, because that is the
	// part somebody reading a log is checking.
	if !strings.Contains(h.log.text(), "key=REDACTED") {
		t.Errorf("the log does not show that a credential was sent:\n%s", h.log.text())
	}
	if !strings.Contains(h.log.text(), "spaces/AAAATestSpace") {
		t.Errorf("the log redacted the space away:\n%s", h.log.text())
	}
}

// TestABadURLIsRefusedAtConstruction, so that a credential which reached the
// keyring by some route other than the command that validates one still cannot
// produce a transport.
func TestABadURLIsRefusedAtConstruction(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"empty", ""},
		{"plaintext to a real host", "http://chat.googleapis.com/v1/spaces/AAAA/messages?key=k&token=t"},
		{"no token", "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=" + testKey},
		{"no spaces segment", "https://chat.googleapis.com/v1/messages?key=" + testKey + "&token=" + testToken},
		{"a space name the API would not issue", "https://chat.googleapis.com/v1/spaces/has.a.dot/messages?key=" + testKey + "&token=" + testToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, err := New(Options{Profile: "alerts", URL: tc.url})
			if err == nil {
				t.Fatalf("a bad URL built a transport for %s", built.Space())
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), testToken) {
				t.Errorf("the refusal quotes the credential back:\n%v", err)
			}
		})
	}
}

// TestThreadingWorksOverAWebhook, which is the one thing beyond plain text that
// this transport can do, and the one where the API's default would silently do
// something else.
func TestThreadingWorksOverAWebhook(t *testing.T) {
	var body, replyOption string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		replyOption = r.URL.Query().Get("messageReplyOption")
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB","thread":{"threadKey":"deploys"}}`)
	})

	sent, err := h.transport.Send(context.Background(), chat.SendRequest{
		Message:   chat.Message{Text: "deployed"},
		ThreadKey: "deploys",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	h.mu.Lock()
	body = h.bodies[0]
	h.mu.Unlock()

	if !strings.Contains(body, `"threadKey":"deploys"`) {
		t.Errorf("the thread key did not travel in the body: %s", body)
	}
	if replyOption != chat.ReplyFallbackToNewThread {
		t.Errorf("messageReplyOption = %q; without it the API silently ignores the thread key", replyOption)
	}
	if sent.Thread == nil || sent.Thread.ThreadKey != "deploys" {
		t.Errorf("the response was not decoded: %+v", sent)
	}
}
