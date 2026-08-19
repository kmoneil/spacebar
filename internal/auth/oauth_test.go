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

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/output"
)

// authServer stands in for Google.
//
// It answers the token endpoint the way the real one does and records what it
// was asked, so that the PKCE arithmetic can be checked from the outside rather
// than by asking the same code twice.
type authServer struct {
	*httptest.Server

	mu       sync.Mutex
	form     url.Values
	response string
	status   int
}

func newAuthServer(t *testing.T) *authServer {
	t.Helper()

	s := &authServer{
		status: http.StatusOK,
		response: `{"access_token":"ya29.access","refresh_token":"1//refresh",` +
			`"token_type":"Bearer","expires_in":3599}`,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		s.mu.Lock()
		s.form = r.PostForm
		status, body := s.status, s.response
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)

	return s
}

func (s *authServer) sent() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.form
}

func (s *authServer) answer(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.response = status, body
}

// flowAgainst builds a flow whose token endpoint is the fake, and whose browser
// completes the callback instead of launching anything.
//
// The browser stands in for a person: it receives the authorization URL, and
// what it does with it is what a browser would do, which is fetch the redirect.
// Everything the flow depends on, the port, the state, the challenge, arrives
// through that URL and nowhere else, which is what makes this a test of the
// flow rather than of the test.
func flowAgainst(t *testing.T, s *authServer, act func(consent *url.URL) string) *Flow {
	t.Helper()

	f := &Flow{
		ClientID:     "1234.apps.googleusercontent.example",
		ClientSecret: "notARealClientSecret",
		Scopes:       []string{ScopeSendOnly},
		Timeout:      10 * time.Second,
	}
	f.Browser = func(raw string) error {
		consent, err := url.Parse(raw)
		if err != nil {
			return err
		}

		callback := act(consent)
		if callback == "" {
			return nil
		}
		go func() {
			resp, err := http.Get(callback) //nolint:noctx // a stand-in for a browser
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	return f
}

// tokenEndpoint points the flow's exchange at the fake by rewriting the
// configuration after it is built.
func (f *Flow) withTokenEndpoint(url string) *Flow {
	f.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	f.tokenURL = url
	return f
}

// TestTheChallengeIsTheHashOfTheVerifier, computed here rather than by calling
// the same function the flow calls.
//
// A round trip through one implementation agrees with itself whatever it does.
// RFC 7636 §4.2 says the challenge is base64url(sha256(verifier)) with no
// padding, so that is what is computed and compared.
func TestTheChallengeIsTheHashOfTheVerifier(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])

	if got := Challenge(verifier); got != want {
		t.Errorf("Challenge = %q, want %q", got, want)
	}
	// RFC 7636 §4.2: no padding, and the URL alphabet, because it travels in a
	// query string.
	if strings.ContainsAny(Challenge(verifier), "=+/") {
		t.Errorf("the challenge is not base64url without padding: %q", Challenge(verifier))
	}
}

// TestTheAuthorizationURLCarriesWhatTheFlowDependsOn.
//
// Every one of these has a failure that is invisible until later.
// access_type=offline missing means no refresh token, so every command needs a
// browser. prompt=consent missing means no refresh token on a second
// authorization, because Google issues one on first consent only, and that
// failure arrives a day later when the access token expires.
func TestTheAuthorizationURLCarriesWhatTheFlowDependsOn(t *testing.T) {
	s := newAuthServer(t)

	var consent *url.URL
	f := flowAgainst(t, s, func(u *url.URL) string {
		consent = u
		return u.Query().Get("redirect_uri") + "?code=theCode&state=" + url.QueryEscape(u.Query().Get("state"))
	}).withTokenEndpoint(s.URL)

	if _, err := f.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}

	q := consent.Query()
	for name, want := range map[string]string{
		"response_type":         "code",
		"access_type":           "offline",
		"prompt":                "consent",
		"code_challenge_method": "S256",
		"client_id":             "1234.apps.googleusercontent.example",
		"scope":                 ScopeSendOnly,
	} {
		if q.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, q.Get(name), want)
		}
	}

	if consent.Scheme+"://"+consent.Host+consent.Path != AuthEndpoint {
		t.Errorf("the consent URL is %q, want %q", consent.String(), AuthEndpoint)
	}

	// The redirect is a loopback IP literal, never the name. SPEC.md §15.4: a
	// name resolves through whatever the machine's resolver says.
	redirect := q.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") {
		t.Errorf("redirect_uri = %q, want a 127.0.0.1 address", redirect)
	}
	if strings.Contains(redirect, "localhost") {
		t.Errorf("redirect_uri names localhost, which resolves through DNS: %q", redirect)
	}

	// The challenge on the URL and the verifier in the exchange are the same
	// pair, which is the whole of PKCE.
	sent := s.sent()
	if got, want := q.Get("code_challenge"), Challenge(sent.Get("code_verifier")); got != want {
		t.Errorf("the challenge does not match the verifier that was sent: %q vs %q", got, want)
	}
	if len(sent.Get("code_verifier")) < 43 {
		t.Errorf("the verifier is %d characters, and RFC 7636 §4.1 wants at least 43", len(sent.Get("code_verifier")))
	}
}

// TestACallbackWithTheWrongStateIsRejected.
//
// The state check is the whole defence against an authorization code injected
// by another page in the same browser. A flow that accepted one would exchange
// somebody else's code and store the resulting token as this profile's.
func TestACallbackWithTheWrongStateIsRejected(t *testing.T) {
	s := newAuthServer(t)

	f := flowAgainst(t, s, func(u *url.URL) string {
		return u.Query().Get("redirect_uri") + "?code=injected&state=notTheStateWeSent"
	}).withTokenEndpoint(s.URL)
	f.Timeout = 300 * time.Millisecond

	_, err := f.Login(context.Background())
	if err == nil {
		t.Fatal("a callback with the wrong state completed the flow")
	}
	if s.sent() != nil {
		t.Error("a code from a mismatched callback was exchanged")
	}
	if got := output.ExitCodeOf(err); got != output.ExitAuthRequired {
		t.Errorf("exit code = %d, want %d", got, output.ExitAuthRequired)
	}
}

// TestTheStateAndVerifierAreDifferentEveryTime, because a value reused between
// flows is a value that can be replayed into one.
func TestTheStateAndVerifierAreDifferentEveryTime(t *testing.T) {
	s := newAuthServer(t)

	seen := map[string]bool{}
	for range 5 {
		var state string
		f := flowAgainst(t, s, func(u *url.URL) string {
			state = u.Query().Get("state")
			return u.Query().Get("redirect_uri") + "?code=theCode&state=" + url.QueryEscape(state)
		}).withTokenEndpoint(s.URL)

		if _, err := f.Login(context.Background()); err != nil {
			t.Fatalf("Login: %v", err)
		}
		verifier := s.sent().Get("code_verifier")

		for _, value := range []string{state, verifier} {
			if seen[value] {
				t.Fatalf("a value repeated between flows: %q", value)
			}
			seen[value] = true
		}
	}
}

// TestConsentRefusedIsExitFourAndSaysSo.
func TestConsentRefusedIsExitFourAndSaysSo(t *testing.T) {
	s := newAuthServer(t)

	f := flowAgainst(t, s, func(u *url.URL) string {
		return u.Query().Get("redirect_uri") + "?error=access_denied&state=" +
			url.QueryEscape(u.Query().Get("state"))
	}).withTokenEndpoint(s.URL)

	_, err := f.Login(context.Background())
	if err == nil {
		t.Fatal("a refused consent completed the flow")
	}
	if got := output.ExitCodeOf(err); got != output.ExitAuthRequired {
		t.Errorf("exit code = %d, want %d", got, output.ExitAuthRequired)
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("the failure does not say what happened:\n%v", err)
	}
	if s.sent() != nil {
		t.Error("a token was requested after consent was refused")
	}
}

// TestAnAdminPolicyRefusalNamesTheWayOut.
//
// This is the failure the whole bring-your-own-client feature exists for, and
// somebody hitting it has no way to know that from the error code alone.
func TestAnAdminPolicyRefusalNamesTheWayOut(t *testing.T) {
	s := newAuthServer(t)

	f := flowAgainst(t, s, func(u *url.URL) string {
		return u.Query().Get("redirect_uri") + "?error=admin_policy_enforced&state=" +
			url.QueryEscape(u.Query().Get("state"))
	}).withTokenEndpoint(s.URL)

	_, err := f.Login(context.Background())
	if err == nil {
		t.Fatal("an admin policy refusal completed the flow")
	}
	for _, want := range []string{"administrator", "Internal", "own Cloud project"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure is missing %q:\n%v", want, err)
		}
	}
}

// TestInvalidGrantIsExplainedRatherThanQuoted.
//
// SPEC.md §6.7. "oauth2: \"invalid_grant\"" tells somebody nothing about the
// seven-day expiry that almost certainly caused it, and the library's error
// value carries the whole response body beside the code.
func TestInvalidGrantIsExplainedRatherThanQuoted(t *testing.T) {
	s := newAuthServer(t)
	s.answer(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)

	f := flowAgainst(t, s, func(u *url.URL) string {
		return u.Query().Get("redirect_uri") + "?code=theCode&state=" + url.QueryEscape(u.Query().Get("state"))
	}).withTokenEndpoint(s.URL)

	_, err := f.Login(context.Background())
	if err == nil {
		t.Fatal("an invalid_grant completed the flow")
	}
	if got := output.ExitCodeOf(err); got != output.ExitAuthRequired {
		t.Errorf("exit code = %d, want %d", got, output.ExitAuthRequired)
	}
	if strings.Contains(err.Error(), "oauth2:") {
		t.Errorf("the raw library error was printed:\n%v", err)
	}
	for _, want := range []string{"expired", "testing mode", "seven days"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not explain the likely cause, missing %q:\n%v", want, err)
		}
	}
}

// TestATokenResponseNeverReachesTheError.
//
// oauth2.RetrieveError carries the whole response body, and there is a path in
// the library where a 200 that also names an error returns one. On a token
// endpoint that body is an access token and a refresh token, so what matters is
// that this package reads the code off the value and drops it rather than
// formatting it.
func TestATokenResponseNeverReachesTheError(t *testing.T) {
	s := newAuthServer(t)
	s.answer(http.StatusOK, `{"error":"invalid_scope","access_token":"ya29.leaked","refresh_token":"1//leaked"}`)

	f := flowAgainst(t, s, func(u *url.URL) string {
		return u.Query().Get("redirect_uri") + "?code=theCode&state=" + url.QueryEscape(u.Query().Get("state"))
	}).withTokenEndpoint(s.URL)

	_, err := f.Login(context.Background())
	if err == nil {
		t.Fatal("a response naming an error completed the flow")
	}
	for _, secret := range []string{"ya29.leaked", "1//leaked"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("a token reached the failure message:\n%v", err)
		}
	}
}

// TestASuccessfulFlowReturnsWhatIsWorthStoring.
func TestASuccessfulFlowReturnsWhatIsWorthStoring(t *testing.T) {
	s := newAuthServer(t)
	before := time.Now()

	f := flowAgainst(t, s, func(u *url.URL) string {
		return u.Query().Get("redirect_uri") + "?code=theCode&state=" + url.QueryEscape(u.Query().Get("state"))
	}).withTokenEndpoint(s.URL)

	token, err := f.Login(context.Background())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	if token.AccessToken != "ya29.access" || token.RefreshToken != "1//refresh" {
		t.Errorf("token = %+v", token)
	}
	if token.Expiry.Before(time.Now()) {
		t.Errorf("expiry is already past: %v", token.Expiry)
	}

	// ObtainedAt is stored rather than derived, because SPEC.md §6.7 needs it:
	// the seven-day refresh-token death is measured from consent, and the
	// access token's expiry says nothing about it.
	if token.ObtainedAt.Before(before) || token.ObtainedAt.After(time.Now()) {
		t.Errorf("obtained_at = %v, outside the run", token.ObtainedAt)
	}
	if len(token.Scopes) != 1 || token.Scopes[0] != ScopeSendOnly {
		t.Errorf("scopes = %v", token.Scopes)
	}

	// The code and the verifier both reached the exchange, which is what makes
	// an intercepted code useless on its own.
	sent := s.sent()
	if sent.Get("code") != "theCode" || sent.Get("code_verifier") == "" {
		t.Errorf("the exchange sent %v", sent)
	}
	if sent.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type = %q", sent.Get("grant_type"))
	}
}

// TestAFlowThatNobodyAnswersTimesOutRatherThanHanging.
//
// The browser is opened and nothing comes back, which is what happens when
// somebody closes the tab. A flow that waited forever would hold a terminal,
// and in a script it would hold whatever is driving it.
func TestAFlowThatNobodyAnswersTimesOutRatherThanHanging(t *testing.T) {
	s := newAuthServer(t)

	f := flowAgainst(t, s, func(*url.URL) string { return "" }).withTokenEndpoint(s.URL)
	f.Timeout = 200 * time.Millisecond

	started := time.Now()
	_, err := f.Login(context.Background())
	if err == nil {
		t.Fatal("a flow nobody answered succeeded")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the flow took %v to give up", elapsed)
	}
	if got := output.ExitCodeOf(err); got != output.ExitAuthRequired {
		t.Errorf("exit code = %d, want %d", got, output.ExitAuthRequired)
	}
	if !strings.Contains(err.Error(), "Nothing was changed") {
		t.Errorf("the failure does not say the state of things:\n%v", err)
	}
}

// TestABrowserThatWillNotLaunchDoesNotFailTheFlow.
//
// SPEC.md §6.5, and it matters on exactly the machines this tool is built for:
// a container, a CI runner, an SSH session. On this development box there is no
// xdg-open at all, so the real failure is exec.ErrNotFound rather than a
// non-zero exit.
func TestABrowserThatWillNotLaunchDoesNotFailTheFlow(t *testing.T) {
	s := newAuthServer(t)

	reported := &collectingReporter{}
	consent := make(chan string, 1)

	f := flowAgainst(t, s, func(u *url.URL) string {
		consent <- u.String()
		return ""
	}).withTokenEndpoint(s.URL)
	f.Report = reported
	f.Timeout = 5 * time.Second

	launched := f.Browser
	f.Browser = func(url string) error {
		_ = launched(url)
		return fmt.Errorf("exec: %q: executable file not found in $PATH", "xdg-open")
	}

	// Somebody reads the URL off the terminal and opens it themselves, which is
	// what SPEC.md §6.5 says has to still work.
	go func() {
		resp, err := http.Get(callbackFor(<-consent, "theCode")) //nolint:noctx // a stand-in for a person
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	if _, err := f.Login(context.Background()); err != nil {
		t.Fatalf("a browser that would not launch failed the flow: %v", err)
	}
	if !strings.Contains(reported.text(), "Could not open a browser") {
		t.Errorf("nothing told the user to open it themselves:\n%s", reported.text())
	}
	if !strings.Contains(reported.text(), AuthEndpoint) {
		t.Errorf("the URL to open was not printed:\n%s", reported.text())
	}
}

func callbackFor(consent, code string) string {
	u, err := url.Parse(consent)
	if err != nil {
		return ""
	}
	q := u.Query()
	return q.Get("redirect_uri") + "?code=" + code + "&state=" + url.QueryEscape(q.Get("state"))
}

type collectingReporter struct {
	mu    sync.Mutex
	lines []string
}

func (c *collectingReporter) Logf(format string, a ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, a...))
}

func (c *collectingReporter) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

// TestNoClientIsAUsageFailure, which is the first thing somebody hits on a
// build from source, where the client is deliberately empty.
func TestNoClientIsAUsageFailure(t *testing.T) {
	f := &Flow{Scopes: []string{ScopeSendOnly}}

	_, err := f.Login(context.Background())
	if err == nil {
		t.Fatal("a flow with no client succeeded")
	}
	if got := output.ExitCodeOf(err); got != output.ExitUsage {
		t.Errorf("exit code = %d, want %d", got, output.ExitUsage)
	}
}

// TestAPastedCallbackCompletesAFlowTheBrowserCouldNotDeliver is the whole
// point: a login that finishes on a machine whose browser is somewhere else.
//
// Nothing here reaches the listener over a socket, which is the situation being
// modelled. The consent URL is read off the report, the callback URL a browser
// would have failed to load is built from it, and it goes in on Paste the way
// somebody pastes it.
func TestAPastedCallbackCompletesAFlowTheBrowserCouldNotDeliver(t *testing.T) {
	s := newAuthServer(t)

	reported := &collectingReporter{}
	consent := make(chan string, 1)
	paste, writer := io.Pipe()

	f := flowAgainst(t, s, func(u *url.URL) string {
		consent <- u.String()
		return ""
	}).withTokenEndpoint(s.URL)
	f.Report = reported
	f.Timeout = 5 * time.Second
	f.Paste = paste

	// The browser is on another machine, so it consents and then cannot
	// deliver anything back here. The launcher still runs, because that is
	// what feeds the consent URL to the test.
	launched := f.Browser
	f.Browser = func(url string) error {
		_ = launched(url)
		return fmt.Errorf("no browser on this machine")
	}

	go func() {
		callback := callbackFor(<-consent, "theCode")
		_, _ = writer.Write([]byte(callback + "\n"))
	}()

	token, err := f.Login(context.Background())
	if err != nil {
		t.Fatalf("a pasted callback did not complete the flow: %v", err)
	}
	if token == nil || token.RefreshToken == "" {
		t.Fatalf("the flow completed without a refresh token: %+v", token)
	}
	if !strings.Contains(reported.text(), "Paste the URL it failed on") {
		t.Errorf("nobody was told the paste route exists:\n%s", reported.text())
	}
}

// TestAWrongPasteCostsASentenceRatherThanTheAttempt.
//
// Three minutes is the whole budget and the line before the right one is
// usually a stray newline or the consent URL from the wrong window. A route
// that gave up on the first wrong answer would be a route somebody gets one
// attempt at.
func TestAWrongPasteCostsASentenceRatherThanTheAttempt(t *testing.T) {
	s := newAuthServer(t)

	reported := &collectingReporter{}
	consent := make(chan string, 1)
	paste, writer := io.Pipe()

	f := flowAgainst(t, s, func(u *url.URL) string {
		consent <- u.String()
		return ""
	}).withTokenEndpoint(s.URL)
	f.Report = reported
	f.Timeout = 5 * time.Second
	f.Paste = paste

	launched := f.Browser
	f.Browser = func(url string) error {
		_ = launched(url)
		return fmt.Errorf("no browser on this machine")
	}

	go func() {
		url := <-consent
		// A blank line, then something that is not a URL, then the consent URL
		// pasted by mistake, then the real one.
		_, _ = writer.Write([]byte("\n"))
		_, _ = writer.Write([]byte("not a url\n"))
		_, _ = writer.Write([]byte(url + "\n"))
		_, _ = writer.Write([]byte(callbackFor(url, "theCode") + "\n"))
	}()

	if _, err := f.Login(context.Background()); err != nil {
		t.Fatalf("the flow gave up before the right paste arrived: %v", err)
	}
	if !strings.Contains(reported.text(), "not this flow's callback") {
		t.Errorf("a wrong paste was accepted or was refused silently:\n%s", reported.text())
	}
}

// TestNoPasteReaderMeansNoPromptAndNoRead holds the rule that nothing blocks on
// input when stdin is not a terminal.
//
// The gate is internal/cli's: it passes a reader only when output.Interactive
// says somebody is there. This is the other side of that contract, and it
// asserts the absence of the prompt as well as the absence of the read, because
// a prompt printed to somebody who cannot answer it is its own defect.
func TestNoPasteReaderMeansNoPromptAndNoRead(t *testing.T) {
	s := newAuthServer(t)

	reported := &collectingReporter{}
	consent := make(chan string, 1)

	f := flowAgainst(t, s, func(u *url.URL) string {
		consent <- u.String()
		return ""
	}).withTokenEndpoint(s.URL)
	f.Report = reported
	f.Timeout = 5 * time.Second

	go func() {
		resp, err := http.Get(callbackFor(<-consent, "theCode")) //nolint:noctx // a stand-in for a browser
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	if _, err := f.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if strings.Contains(reported.text(), "Paste the URL") {
		t.Errorf("a paste prompt was printed with no reader to answer it:\n%s", reported.text())
	}
}
