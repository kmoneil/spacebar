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
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/config"
)

// The credentials a test webhook URL carries. Long enough to be scrubbed, and
// distinctive enough that a test can grep for them: the whole question these
// tests ask is whether either string ever comes back out of this package.
const (
	testKey   = "AIzaSyTestKeyNotARealOne0123456789"
	testToken = "sQ7testTokenNotARealOne0123456789"
)

// harness is a client pointed at a server that counts what it was asked for.
type harness struct {
	client *Client
	server *httptest.Server
	log    *recordingLogger

	mu       sync.Mutex
	requests int
	bodies   []string

	// delays are what the retry loop asked to sleep for. Recorded rather than
	// slept, so that the policy is tested as a policy: a test that proves five
	// attempts by taking fifteen seconds is a test somebody deletes.
	delays []time.Duration
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

// newHarness stands up a server and a webhook client that talks to it.
//
// The base URL is the shape a real incoming webhook has: the full messages
// endpoint for one space, with the credential in the query. Confirmed against
// the live API in the recon for this card, which is also where the error
// envelope these fixtures return came from.
func newHarness(t *testing.T, handler http.HandlerFunc) *harness {
	t.Helper()

	h := &harness{log: &recordingLogger{}}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		h.mu.Lock()
		h.requests++
		h.bodies = append(h.bodies, string(body))
		h.mu.Unlock()

		handler(w, r)
	}))
	t.Cleanup(h.server.Close)

	client, err := New(Options{
		BaseURL: fmt.Sprintf("%s/v1/spaces/AAAATestSpace/messages?key=%s&token=%s",
			h.server.URL, testKey, testToken),
		Transport: config.TransportWebhook,
		Profile:   "alerts",
		HTTP:      h.server.Client(),
		Log:       h.log,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Deterministic backoff. fullJitter is tested on its own; here the window
	// is what the policy computes and the test can assert it exactly.
	client.jitter = func(window time.Duration) time.Duration { return window }
	client.sleep = func(ctx context.Context, d time.Duration) error {
		h.mu.Lock()
		h.delays = append(h.delays, d)
		h.mu.Unlock()
		return ctx.Err()
	}

	h.client = client
	return h
}

func (h *harness) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requests
}

func (h *harness) waits() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Duration(nil), h.delays...)
}

// apiError writes the error envelope the Chat API actually returns. Copied from
// a live response rather than invented, down to the details array: a fixture
// that does not match the wire tests the fixture.
func apiErrorBody(code int, status, message, reason string) string {
	return fmt.Sprintf(`{
  "error": {
    "code": %d,
    "message": %q,
    "status": %q,
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
        "reason": %q,
        "domain": "googleapis.com",
        "metadata": {"service": "chat.googleapis.com"}
      }
    ]
  }
}`, code, message, status, reason)
}

func writeAPIError(w http.ResponseWriter, code int, status, message, reason string) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(code)
	_, _ = fmt.Fprint(w, apiErrorBody(code, status, message, reason))
}

func sendOne(h *harness) (*Message, error) {
	return h.client.SendMessage(context.Background(), SendRequest{Message: Message{Text: "hello"}})
}

// TestVerboseOutputCarriesNoCredential is the claim from the card, tested
// against the shapes a credential actually takes here.
//
// The card proposed grepping the output for 'key=', 'token=' and 'Bearer ',
// which the redacted form also matches: a correctly redacted URL still reads
// key=REDACTED. So the assertion is the stronger pair. The secret values never
// appear, and the placeholders do, because SPEC.md §15.1 wants a reader to be
// able to tell that a credential was sent rather than to wonder.
func TestVerboseOutputCarriesNoCredential(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB","text":"hello"}`)
	})
	h.client.auth = staticAuthorizer{header: "Bearer ya29.NotARealAccessTokenValue"}

	if _, err := sendOne(h); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	logged := h.log.text()
	for _, secret := range []string{testKey, testToken, "ya29.NotARealAccessTokenValue"} {
		if strings.Contains(logged, secret) {
			t.Errorf("the verbose log contains a credential:\n%s", logged)
		}
	}

	for _, want := range []string{"key=REDACTED", "token=REDACTED", "Authorization: REDACTED"} {
		if !strings.Contains(logged, want) {
			t.Errorf("the verbose log is missing %q, so a reader cannot tell what was sent:\n%s", want, logged)
		}
	}

	// The path survives redaction, because it is the part somebody reading a
	// log is checking.
	if !strings.Contains(logged, "/v1/spaces/AAAATestSpace/messages") {
		t.Errorf("the verbose log redacted the space away:\n%s", logged)
	}
}

// TestDescribeTransportRedactsTheURL holds the first of the two layers on its
// own.
//
// TestATransportFailureDoesNotQuoteTheURL above passes even with this
// redaction removed, because scrub strikes the profile's own secrets out of
// whatever is about to be said. That is the backstop working, and it was worth
// finding out that it was the only thing being tested: scrub only knows the
// values from this client's base URL, so a URL from anywhere else would go
// straight through it. The credential here is deliberately not one the client
// holds.
func TestDescribeTransportRedactsTheURL(t *testing.T) {
	failure := &url.Error{
		Op:  "Post",
		URL: "https://chat.example/v1/spaces/AAAATestSpace/messages?key=someOtherProfilesKey&token=someOtherProfilesToken",
		Err: errors.New("dial tcp 203.0.113.1:443: connect: connection refused"),
	}

	got := describeTransport(failure)
	for _, secret := range []string{"someOtherProfilesKey", "someOtherProfilesToken"} {
		if strings.Contains(got, secret) {
			t.Errorf("describeTransport quotes a credential:\n%s", got)
		}
	}
	if !strings.Contains(got, "key=REDACTED") || !strings.Contains(got, "token=REDACTED") {
		t.Errorf("describeTransport did not redact the query:\n%s", got)
	}
	// The space and the reason both survive, because they are what somebody
	// reading the failure is trying to find out.
	if !strings.Contains(got, "spaces/AAAATestSpace") || !strings.Contains(got, "connection refused") {
		t.Errorf("describeTransport threw away what the reader needs:\n%s", got)
	}
}

// staticAuthorizer is a bearer credential that never changes, standing in for
// the user-OAuth transport that Milestone 3 brings.
type staticAuthorizer struct {
	header    string
	refreshed *int
	renews    bool
}

func (s staticAuthorizer) Authorization(context.Context) (string, error) { return s.header, nil }

func (s staticAuthorizer) Refresh(context.Context) (bool, error) {
	if s.refreshed != nil {
		*s.refreshed++
	}
	return s.renews, nil
}

// TestATransportFailureDoesNotQuoteTheURL is the leak this package is arranged
// to prevent, and it is not hypothetical.
//
// net/http wraps every failure in a *url.Error whose Error method quotes the
// URL it failed on, query string and all. For a webhook profile that string is
// the entire authentication for a space, so the default rendering of "the
// server is down" publishes a credential into whatever the operator's stderr is
// piped to.
func TestATransportFailureDoesNotQuoteTheURL(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {})
	h.server.Close() // Nothing is listening now, so the dial fails.

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a send to a closed server succeeded")
	}

	message := err.Error()
	for _, secret := range []string{testKey, testToken} {
		if strings.Contains(message, secret) {
			t.Errorf("the failure message contains a credential:\n%s", message)
		}
	}
	if !strings.Contains(message, "REDACTED") {
		t.Errorf("the failure message does not show that a URL was redacted:\n%s", message)
	}
	if strings.Contains(h.log.text(), testToken) {
		t.Errorf("the verbose log contains a credential:\n%s", h.log.text())
	}
}

// TestARedirectIsNotFollowed holds the rule that a server cannot move a request
// somewhere else.
//
// net/http strips the Authorization header across a cross-origin redirect, so a
// bearer token is protected by the standard library. A webhook credential is
// not: it lives in the query string and travels with the URL, so following one
// redirect would hand the credential to whoever answered it. No redirect is
// followed at all, and the 3xx is reported as the failure it is.
func TestARedirectIsNotFollowed(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the client followed a redirect and delivered the credential to another host")
	}))
	t.Cleanup(elsewhere.Close)

	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", elsewhere.URL+"/collect")
		w.WriteHeader(http.StatusFound)
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a redirect was treated as a successful send")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("the failure does not explain that a redirect was refused:\n%v", err)
	}
	if h.count() != 1 {
		t.Errorf("made %d requests, want 1: a 3xx is not retried", h.count())
	}
}

// TestAPathCannotLeaveTheBase holds the relative-path rule.
//
// The rule matters because a path can come from a value the far end chose: a
// next-page token, a resource name in a response, an alias somebody was sent.
// An absolute or scheme-relative path substituted for the base would move the
// request, and for a webhook the credential is part of what would move.
func TestAPathCannotLeaveTheBase(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a request was made for a path that should have been refused")
	})

	for _, tc := range []struct{ name, path string }{
		{"absolute", "/v1/spaces/BBB/messages"},
		{"scheme relative", "//evil.invalid/v1/messages"},
		{"a whole URL", "https://evil.invalid/v1/messages"},
		{"walking up", "../../../v1/spaces/BBB/messages"},
		{"a query", "messages?key=stolen"},
		{"a fragment", "messages#fragment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.client.do(context.Background(), Request{Method: http.MethodGet, Path: tc.path})
			if err == nil {
				t.Fatalf("path %q was accepted", tc.path)
			}
			if h.count() != 0 {
				t.Fatalf("path %q reached the network, and a refusal after the request is not a refusal", tc.path)
			}
		})
	}
}

// TestSameOriginIsCheckedAfterTheJoin holds the second layer on its own.
//
// checkRelative decides what a path may look like, and a parser that accepts
// something it should not is a bug with a long history. sameOrigin asks the
// built URL the question that actually matters, so it is tested without going
// through the parser that is supposed to make it unnecessary.
func TestSameOriginIsCheckedAfterTheJoin(t *testing.T) {
	client, err := New(Options{BaseURL: "https://chat.example/v1/spaces/AAA/messages?key=" + testKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, raw := range []string{
		"https://evil.invalid/v1/spaces/AAA/messages",
		"http://chat.example/v1/spaces/AAA/messages",
		"https://chat.example/other/spaces/AAA/messages",
	} {
		target, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatalf("url.Parse(%q): %v", raw, parseErr)
		}
		if err := client.sameOrigin(target); err == nil {
			t.Errorf("sameOrigin accepted %q", raw)
		}
	}
}

// TestARequestCannotSetACredentialParameter keeps one list of secret parameter
// names doing one job.
func TestARequestCannotSetACredentialParameter(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a request that tried to set a credential parameter reached the network")
	})

	_, err := h.client.do(context.Background(), Request{
		Method: http.MethodPost,
		Query:  url.Values{"key": []string{"stolen"}},
	})
	if err == nil {
		t.Fatal("a request set the key parameter")
	}
	if !strings.Contains(err.Error(), "credential") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// TestTheBaseQuerySurvivesARequestQuery is the other half of the merge, and it
// is what makes threading work: a webhook's credential has to still be there
// after threadKey is added.
func TestTheBaseQuerySurvivesARequestQuery(t *testing.T) {
	var got url.Values
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	if _, err := h.client.SendMessage(context.Background(), SendRequest{
		Message:   Message{Text: "hello"},
		ThreadKey: "deploys",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	for name, want := range map[string]string{
		"key":                testKey,
		"token":              testToken,
		"messageReplyOption": ReplyFallbackToNewThread,
	} {
		if got.Get(name) != want {
			t.Errorf("query parameter %s = %q, want %q", name, got.Get(name), want)
		}
	}
}

// TestAPlaintextBaseIsRefused holds the rule that a credential does not travel
// in the clear. The loopback exception is what a test server needs, and it is
// an IP literal rather than a name because a name is resolved by whatever the
// machine's resolver says.
func TestAPlaintextBaseIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		base    string
		wantErr bool
	}{
		{"https", "https://chat.example/v1", false},
		{"loopback", "http://127.0.0.1:8080/v1", false},
		{"plaintext", "http://chat.example/v1", true},
		{"localhost by name", "http://localhost:8080/v1", true},
		{"no scheme", "chat.example/v1", true},
		{"empty", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Options{BaseURL: tc.base})
			if (err != nil) != tc.wantErr {
				t.Fatalf("New(%q) error = %v, want error: %v", tc.base, err, tc.wantErr)
			}
		})
	}
}

// TestAMalformedBaseIsNotQuotedBack. The field that holds a base URL holds a
// webhook URL, so a parse failure that echoes it is a credential in an error
// message.
func TestAMalformedBaseIsNotQuotedBack(t *testing.T) {
	base := "https://chat.example/v1/spaces/AAA/messages?key=" + testKey + "\x7f"

	_, err := New(Options{BaseURL: base})
	if err == nil {
		t.Fatal("a URL with a control character in it was accepted")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("the parse failure quotes the credential back:\n%v", err)
	}
}

// TestAnOversizeResponseIsRefusedRatherThanTruncated.
//
// A body parsed short is a wrong answer reported as a right one, and this tool
// does not report a truncated result as a complete one anywhere. It is also the
// cheapest way for a hostile server to make a send expensive, so the failure is
// permanent rather than retried.
func TestAnOversizeResponseIsRefusedRatherThanTruncated(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := strings.Repeat("x", 1<<20)
		for range 9 {
			_, _ = fmt.Fprint(w, chunk)
		}
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("an oversize response was accepted")
	}
	if !strings.Contains(err.Error(), "more than this tool will hold") {
		t.Errorf("the failure does not explain itself: %v", err)
	}
	if h.count() != 1 {
		t.Errorf("made %d requests, want 1: a body that is too big will be too big again", h.count())
	}
}

// TestEveryRequestIdentifiesThisBuild. SPEC.md §7.4: somebody looking at Chat
// API traffic they did not expect has to be able to find out what is making it.
func TestEveryRequestIdentifiesThisBuild(t *testing.T) {
	var agent, accept, contentType string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		agent, accept, contentType = r.UserAgent(), r.Header.Get("Accept"), r.Header.Get("Content-Type")
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	if _, err := sendOne(h); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if !regexp.MustCompile(`^\S+/\S+ \(\+https://\S+\)$`).MatchString(agent) {
		t.Errorf("User-Agent = %q, want name/version (+repository)", agent)
	}
	if accept != "application/json" {
		t.Errorf("Accept = %q", accept)
	}
	if !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type = %q", contentType)
	}
}

// TestTheTimeoutBoundsAnAttemptRatherThanTheOperation is the decision this card
// was asked to make, held by a test rather than left in a comment.
//
// A budget spent by the first attempt leaves nothing for the other four, which
// turns the retry policy into decoration. So each attempt gets the whole
// budget, five attempts are made, and the flag help says so, because a caller
// who waits several times the number they typed is entitled to have been told.
func TestTheTimeoutBoundsAnAttemptRatherThanTheOperation(t *testing.T) {
	h := newHarness(t, func(_ http.ResponseWriter, r *http.Request) {
		// Held open until this attempt's own context expires, which is what
		// proves each attempt had a budget of its own: a shared one would have
		// been gone after the first.
		<-r.Context().Done()
	})
	h.client.timeout = 50 * time.Millisecond

	_, err := h.client.do(context.Background(), Request{Method: http.MethodGet})
	if err == nil {
		t.Fatal("a request that never answered was reported as success")
	}
	if h.count() != maxAttempts {
		t.Errorf("made %d attempts, want %d: each one gets the timeout, not a share of it", h.count(), maxAttempts)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("the failure does not say it timed out:\n%v", err)
	}
	for _, secret := range []string{testKey, testToken} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("a timeout message carries a credential:\n%v", err)
		}
	}
}

// TestADeadlineOnTheCallerStopsTheRetries is the mechanism a caller has for
// bounding the whole operation, which is what --timeout deliberately does not
// do. The 503s are kept in the message, because "context deadline exceeded" on
// its own explains nothing about what was being retried.
func TestADeadlineOnTheCallerStopsTheRetries(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "Try again.", "SERVICE_UNAVAILABLE")
	})
	h.client.sleep = func(context.Context, time.Duration) error { return context.DeadlineExceeded }

	_, err := h.client.do(context.Background(), Request{Method: http.MethodGet})
	if err == nil {
		t.Fatal("an expired deadline reported success")
	}
	if h.count() != 1 {
		t.Errorf("made %d requests after the deadline passed, want 1", h.count())
	}
	if !strings.Contains(err.Error(), "ran out of time") || !strings.Contains(err.Error(), "UNAVAILABLE") {
		t.Errorf("the failure does not say what ran out of time or what it was retrying:\n%v", err)
	}
}

// TestADNSFailureIsTreatedAsPreProcessing. A name that does not resolve means
// no request byte reached anybody, so replaying a POST cannot post twice. It is
// the one class of transport failure where that is provable.
func TestADNSFailureIsTreatedAsPreProcessing(t *testing.T) {
	if !preProcessing(&net.DNSError{Err: "no such host", Name: "chat.invalid", IsNotFound: true}) {
		t.Error("a DNS failure was not treated as pre-processing")
	}
	if !preProcessing(&net.OpError{Op: "dial", Err: errors.New("connection refused")}) {
		t.Error("a refused dial was not treated as pre-processing")
	}

	// A failure partway through is not provable, and the conservative reading is
	// the only safe one: the request may have been written, processed, and the
	// answer lost.
	if preProcessing(&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}) {
		t.Error("a mid-request reset was treated as pre-processing")
	}
	if preProcessing(errors.New("something else")) {
		t.Error("an unclassified failure was treated as pre-processing")
	}
}

// FuzzAPathStaysOnTheBase is the relative-path rule stated as a property rather
// than as a list of cases.
//
// The list is what somebody thought of. This says the thing that has to be true
// for every input there is: a path either fails, or produces a URL on the same
// scheme, the same host, and under the same path prefix the profile's credential
// was issued for.
func FuzzAPathStaysOnTheBase(f *testing.F) {
	for _, seed := range []string{
		"", "messages", "spaces/AAA/messages", "/absolute", "//evil.invalid/x",
		"https://evil.invalid/x", "../../up", "a/../b", "%2e%2e/%2e%2e", "\\evil",
		"messages?key=stolen", "messages#f", ".", "./x", "a//b", "\x00", "café/messages",
	} {
		f.Add(seed)
	}

	client, err := New(Options{BaseURL: "https://chat.example/v1/spaces/AAA/messages?key=" + testKey})
	if err != nil {
		f.Fatalf("New: %v", err)
	}

	f.Fuzz(func(t *testing.T, path string) {
		target, err := client.resolve(Request{Method: http.MethodGet, Path: path})
		if err != nil {
			return
		}

		if target.Scheme != "https" || target.Host != "chat.example" {
			t.Fatalf("path %q reached %s://%s", path, target.Scheme, target.Host)
		}
		if !strings.HasPrefix(target.EscapedPath(), "/v1/spaces/AAA/messages") {
			t.Fatalf("path %q left the base path: %s", path, target.EscapedPath())
		}
		// The credential is still the profile's own, whatever the path did.
		if target.Query().Get("key") != testKey {
			t.Fatalf("path %q changed the credential to %q", path, target.Query().Get("key"))
		}
	})
}

// TestTheBodyKeepsChatMarkupUnescaped. Chat markup is full of angle brackets
// and pipes, and Go's JSON encoder escapes them by default. Both forms decode
// to the same string, so this is about what a dry run and a verbose log are
// readable as, and about not altering a value on its way out.
func TestTheBodyKeepsChatMarkupUnescaped(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	})

	if _, err := h.client.SendMessage(context.Background(), SendRequest{
		Message: Message{Text: "<https://example.test|the release> is out"},
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if !strings.Contains(h.bodies[0], `<https://example.test|the release>`) {
		t.Errorf("the request body escaped the markup: %s", h.bodies[0])
	}
}

// TestADryRunShowsTheRequestAndSendsNothing.
//
// The preview is built from the request the client actually built, at the last
// line before the send, so what is printed is what would have gone rather than
// a second description of it. A parallel description can drift, and it drifts
// silently.
func TestADryRunShowsTheRequestAndSendsNothing(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a dry run reached the network")
	})
	h.client.dryRun = true
	h.client.auth = staticAuthorizer{header: "Bearer ya29.NotARealAccessTokenValue"}

	_, err := h.client.SendMessage(context.Background(), SendRequest{Message: Message{Text: "deploy done"}})
	dry, ok := errors.AsType[*DryRun](err)
	if !ok {
		t.Fatalf("a dry run returned %v, want a *DryRun", err)
	}
	if h.count() != 0 {
		t.Fatalf("a dry run made %d requests", h.count())
	}

	req := dry.Request
	if !req.DryRun {
		t.Error("the preview is not marked as one")
	}
	if req.Method != http.MethodPost {
		t.Errorf("method = %q", req.Method)
	}
	if !strings.Contains(req.URL, "/v1/spaces/AAAATestSpace/messages") {
		t.Errorf("url = %q", req.URL)
	}
	if string(req.Body) != `{"text":"deploy done"}` {
		t.Errorf("body = %s, want the exact bytes with no trailing newline", req.Body)
	}
}

// TestADryRunShowsTheHeaderAsRedactedRatherThanOmittingIt.
//
// A missing line reads as "no header was sent", which is a different and wrong
// answer to the question somebody is using a dry run to ask. They want to know
// whether a credential would go, not to be left inferring it from an absence.
func TestADryRunShowsTheHeaderAsRedactedRatherThanOmittingIt(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a dry run reached the network")
	})
	h.client.dryRun = true
	h.client.auth = staticAuthorizer{header: "Bearer ya29.NotARealAccessTokenValue"}

	_, err := h.client.SendMessage(context.Background(), SendRequest{Message: Message{Text: "hello"}})
	dry, ok := errors.AsType[*DryRun](err)
	if !ok {
		t.Fatalf("a dry run returned %v", err)
	}

	if got, ok := dry.Request.Headers["Authorization"]; !ok {
		t.Error("the Authorization header was omitted, which reads as one not being sent")
	} else if got != auth.Redacted {
		t.Errorf("Authorization = %q, want %q", got, auth.Redacted)
	}

	rendered := dry.Request.Text()
	for _, secret := range []string{testKey, testToken, "ya29.NotARealAccessTokenValue"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("the rendered request carries a credential:\n%s", rendered)
		}
	}
	for _, want := range []string{"Authorization: REDACTED", "key=REDACTED", "token=REDACTED"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered request is missing %q:\n%s", want, rendered)
		}
	}
	// The space survives, because it is what somebody running a dry run is
	// checking.
	if !strings.Contains(rendered, "spaces/AAAATestSpace") {
		t.Errorf("the rendered request redacted the space away:\n%s", rendered)
	}
}

// TestADryRunIsNotRetried. There is nothing to retry: no request was made, and
// the answer will be the same every time.
func TestADryRunIsNotRetried(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a dry run reached the network")
	})
	h.client.dryRun = true

	if _, err := h.client.do(context.Background(), Request{Method: http.MethodGet}); err == nil {
		t.Fatal("a dry run reported success")
	}
	if len(h.waits()) != 0 {
		t.Errorf("the retry loop backed off after a dry run: %v", h.waits())
	}
}

// TestPreviewBodyKeepsWhatWouldBeSent.
func TestPreviewBodyKeepsWhatWouldBeSent(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"json", `{"text":"hi"}`, `{"text":"hi"}`},

		// The encoder writes a trailing newline that is not part of the
		// document, and a golden recording it would be recording the encoder.
		{"trailing newline", "{\"text\":\"hi\"}\n", `{"text":"hi"}`},
		{"empty", "", ""},

		// A media upload, whose body is the file. Described with its exact size
		// rather than printed, because printing it is not showing a request, it
		// is copying a file to stdout, and the limit here is 200MB.
		//
		// This case used to expect the whole document quoted back, which was
		// the placeholder left by the comment that said the decision belonged
		// to "the card that adds it". Media upload landed in Milestone 4 and
		// nobody took it, and nobody noticed, because the command that produces
		// one exited before printing anything at all.
		//
		// The angle brackets are unescaped on purpose: the encoder writes < as
		// < by default, and this string exists to be read.
		{"a file", "--boundary\r\nContent-Type: image/png", `"<35 bytes, not shown: this request carries a file>"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := previewBody([]byte(tc.body))
			if string(got) != tc.want {
				t.Errorf("previewBody(%q) = %s, want %s", tc.body, got, tc.want)
			}
			if len(got) > 0 && !json.Valid(got) {
				t.Errorf("previewBody(%q) is not valid JSON: %s", tc.body, got)
			}
		})
	}
}

// FuzzAnAcceptedWebhookURLStillSendsTheCredentialItCarried joins the two halves
// of the webhook credential path, which are checked separately everywhere else
// and have to agree.
//
// auth.CheckWebhookURL decides whether a pasted URL is stored at all, and this
// package decides what a request built from it looks like and which values get
// struck out of anything printed. Each has its own tests and neither knows what
// the other did. The claim that matters to somebody using this tool is the one
// that spans them: the credential they pasted is the credential that gets sent,
// and it is the one that gets redacted.
//
// It is a real property with a real hole behind it. Both sides reach their
// query through url.URL.Query, which discards its parse error, so a semicolon
// anywhere in the query made both of them see no parameters: the check refused
// the URL as truncated, which is the right outcome by luck rather than by
// reason, and had it been accepted the request would have gone out with no
// credential and the scrubber would have had nothing to strike out. This states
// what must hold whichever way that check is written next.
//
// Nothing is dialled. resolve builds a URL and returns it, so a host the
// fuzzer invents is a string that gets parsed and compared, which is the same
// reason internal/lint allows a real host in a literal it can see is only ever
// parsed.
func FuzzAnAcceptedWebhookURLStillSendsTheCredentialItCarried(f *testing.F) {
	for _, seed := range []string{
		"https://chat.example/v1/spaces/AAA/messages?key=" + testKey + "&token=" + testToken,
		"https://chat.example/v1/spaces/AAA/messages?token=" + testToken + "&key=" + testKey,
		"https://chat.example/v1/spaces/AAA/messages?key=" + testKey + "&token=" + testToken + ";x=1",
		"https://chat.example/v1/spaces/AAA/messages?key=" + testKey + "&token=" + testToken + "#f",
		"https://chat.example/v1/spaces/AAA/messages?key=a&token=b",
		"https://chat.example/v1/spaces/AAA/messages?key=" + testKey,
		"https://chat.example/spaces/AAA?key=" + testKey + "&token=" + testToken,
		"http://127.0.0.1:8080/v1/spaces/AAA/messages?key=" + testKey + "&token=" + testToken,
		"http://chat.example/v1/spaces/AAA/messages?key=a&token=b",
		"https://chat.example/messages?key=a&token=b",
		"https://chat.example/v1/spaces/AAA/messages?key=&token=b",
		"", "  ", "not a url", "https://",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		if auth.CheckWebhookURL(raw) != nil {
			return
		}

		client, err := New(Options{BaseURL: strings.TrimSpace(raw)})
		if err != nil {
			// The two disagree about what is usable, which is its own bug:
			// CheckWebhookURL runs when the URL is pasted and this runs when a
			// message is sent, so the gap is a profile that stores cleanly and
			// fails at the first send.
			t.Fatalf("a webhook URL that was accepted cannot build a client: %v", err)
		}

		target, err := client.resolve(Request{Method: http.MethodPost})
		if err != nil {
			t.Fatalf("a webhook URL that was accepted cannot build a request: %v", err)
		}

		// Read from the raw string the same way the check read it, so this
		// compares what the operator pasted against what would be sent.
		pasted, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			t.Fatalf("a webhook URL that was accepted will not parse: %v", err)
		}
		sending := target.Query()

		for _, name := range []string{"key", "token"} {
			want := pasted.Query().Get(name)
			if want == "" {
				t.Fatalf("a webhook URL was accepted with no %s in it: the check and this test "+
					"disagree about what is there", name)
			}
			if got := sending.Get(name); got != want {
				t.Fatalf("the %s that would be sent is not the one that was pasted:\n"+
					" got %q\nwant %q", name, got, want)
			}
		}

		// And both are values scrub knows about, or the second redaction layer
		// is switched off for this profile without anybody saying so. Short
		// values are skipped on purpose, because a two-character token would
		// match all over an unrelated message.
		for _, name := range []string{"key", "token"} {
			value := pasted.Query().Get(name)
			if len(value) < 8 {
				continue
			}
			if !slices.Contains(client.secrets, value) {
				t.Fatalf("the %s of an accepted webhook URL is not one of the values scrub strikes out: %v",
					name, len(client.secrets))
			}
		}
	})
}
