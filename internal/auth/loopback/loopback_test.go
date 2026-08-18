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

package loopback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testState = "aStateValueThatIsThirtyTwoBytesLong"

// fetch is the request itself, with no testing.T in it.
//
// Separated so that browse can run it on a goroutine. Nothing that runs off the
// test's own goroutine may touch a testing.T: t.Fatalf calls FailNow, which is
// documented as having to be called from the goroutine running the test, and
// from any other one it stops only itself while the test carries on.
func fetch(url string) (int, string, error) {
	resp, err := http.Get(url) //nolint:noctx // a stand-in for a browser
	if err != nil {
		return 0, "", fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("reading the page: %w", err)
	}
	return resp.StatusCode, string(body), nil
}

// get fetches a URL the way the browser would, on the test's own goroutine.
func get(t *testing.T, url string) (int, string) {
	t.Helper()

	status, body, err := fetch(url)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return status, body
}

// browse makes the callback request while the test blocks in Wait.
//
// The request has to be in flight rather than finished, because that is the
// real flow: Listen, print the URL, open a browser, Wait. The buffered result
// channel means a request that landed first would work too, so this could have
// been written without a goroutine at all, and it is not, because then the
// tests would stop covering the ordering that actually happens.
//
// What they must not do is let the goroutine outlive the test, and four of them
// did. Each spawned a goroutine calling get and nothing waited for it, so when
// the test body finished first, the t.Cleanup that get registered closed the
// response body underneath io.ReadAll. The read failed, the goroutine called
// t.Fatalf on a test that had already completed, and the package died with
// "Log in goroutine after TestThePageIsSelfContained has completed" rather than
// with a test failure. Reproduced on an untouched tree at roughly one run in
// 450, which is rare enough to read as an unrelated flake and often enough to
// redden somebody's pull request.
//
// The wait is registered here rather than returned, so that it happens whether
// or not a caller remembers it. A cleanup is the one place that is guaranteed
// to run, and receiving from a closed channel is what makes waiting twice safe.
// The failure is reported from the cleanup, which runs on the test's own
// goroutine, so FailNow's rule is kept.
func browse(t *testing.T, url string) {
	t.Helper()

	var err error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _, err = fetch(url)
	}()

	t.Cleanup(func() {
		<-finished
		if err != nil {
			t.Errorf("the browser request failed: %v", err)
		}
	})
}

// TestTheListenerIsOnLoopbackAndNowhereElse.
//
// SPEC.md §15.4. Bound to anything else, the redirect that carries the
// authorization code is reachable from the network, and on a laptop on a café
// network that is everybody in the café.
func TestTheListenerIsOnLoopbackAndNowhereElse(t *testing.T) {
	s, err := Listen(testState)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	host, _, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatalf("the address does not split: %v", err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatalf("the listener is on the name %q rather than an address; a name resolves "+
			"through whatever the machine's resolver says", host)
	}
	if !ip.IsLoopback() {
		t.Errorf("the listener is on %s, which is reachable from another host", ip)
	}
	if !strings.HasPrefix(s.RedirectURL(), "http://127.0.0.1:") {
		t.Errorf("RedirectURL = %q", s.RedirectURL())
	}
}

// TestAListenerNeedsSomethingToCheckAgainst. A flow with no state has no
// defence against an authorization code injected by another page in the same
// browser, so it is refused rather than started.
func TestAListenerNeedsSomethingToCheckAgainst(t *testing.T) {
	if _, err := Listen(""); err == nil {
		t.Fatal("a listener started with no state to check against")
	}
}

// TestTheCallbackIsAnsweredAndTheCodeComesBack.
func TestTheCallbackIsAnsweredAndTheCodeComesBack(t *testing.T) {
	s, err := Listen(testState)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	browse(t, s.RedirectURL()+"?code=theCode&state="+url.QueryEscape(testState))

	result, err := s.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.Code != "theCode" {
		t.Errorf("code = %q", result.Code)
	}
	if result.Err != nil {
		t.Errorf("a successful callback carried an error: %v", result.Err)
	}
}

// TestAMismatchedStateIsNotAnswered.
//
// Rejected without comment: whoever sent it is not the person who started this
// flow, and telling them what was wrong with their guess helps only them.
func TestAMismatchedStateIsNotAnswered(t *testing.T) {
	s, err := Listen(testState)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	status, body := get(t, s.RedirectURL()+"?code=injected&state=notTheState")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
	if strings.Contains(strings.ToLower(body), "state") {
		t.Errorf("the refusal explains itself to whoever sent it:\n%s", body)
	}

	// And the flow is still waiting, because nothing valid has arrived.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if _, err := s.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a mismatched callback ended the flow: %v", err)
	}
}

// TestARequestThatIsNotTheCallbackIsIgnored.
//
// A browser asks for /favicon.ico as soon as it renders the page. Treating that
// as the answer would end the flow before the real callback arrived, which is a
// bug that only shows up in a real browser.
func TestARequestThatIsNotTheCallbackIsIgnored(t *testing.T) {
	s, err := Listen(testState)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if status, _ := get(t, s.RedirectURL()+"favicon.ico"); status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if _, err := s.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a favicon request ended the flow: %v", err)
	}
}

// TestAnAuthorizationErrorComesBackAsOne, with the two that actually happen
// turned into sentences.
func TestAnAuthorizationErrorComesBackAsOne(t *testing.T) {
	for _, tc := range []struct{ code, says string }{
		{"access_denied", "consent was refused"},
		{"admin_policy_enforced", "administrator policy"},
		{"something_else", "something_else"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			s, err := Listen(testState)
			if err != nil {
				t.Fatalf("Listen: %v", err)
			}

			browse(t, s.RedirectURL()+"?error="+tc.code+"&state="+url.QueryEscape(testState))

			result, err := s.Wait(context.Background())
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if result.Err == nil {
				t.Fatal("an authorization error came back as a success")
			}
			if !strings.Contains(result.Err.Error(), tc.says) {
				t.Errorf("the error does not say %q:\n%v", tc.says, result.Err)
			}
			if result.Code != "" {
				t.Errorf("a refused authorization carried a code: %q", result.Code)
			}
		})
	}
}

// TestThePageIsSelfContained.
//
// A page served at the end of an authorization flow is the last thing between
// somebody and a working tool, and it renders on whatever browser they had
// open, possibly offline, possibly behind a proxy that blocks what it does not
// recognize. Anything fetched from elsewhere is a way for that to fail, and it
// is also a request carrying a Referer, and the referring URL here holds the
// authorization code.
func TestThePageIsSelfContained(t *testing.T) {
	s, err := Listen(testState)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	browse(t, s.RedirectURL()+"?code=theCode&state="+url.QueryEscape(testState))
	if _, err := s.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	for _, page := range []string{successPage, refusedPage} {
		for _, forbidden := range []string{"<script", "<link", "<img", "@import", "http://", "https://", "//fonts"} {
			if strings.Contains(strings.ToLower(page), forbidden) {
				t.Errorf("the page fetches something from elsewhere (%q):\n%s", forbidden, page)
			}
		}
		if !strings.Contains(page, "<!doctype html>") {
			t.Errorf("the page is not a document:\n%s", page)
		}
	}
}

// TestTheListenerIsClosedAfterTheCallback, per SPEC.md §6.3, which asks for two
// seconds. A listener that outlived the flow is a port on this machine that
// answers to anything for as long as the process runs.
func TestTheListenerIsClosedAfterTheCallback(t *testing.T) {
	s, err := Listen(testState)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	address := s.listener.Addr().String()

	browse(t, s.RedirectURL()+"?code=theCode&state="+url.QueryEscape(testState))

	started := time.Now()
	if _, err := s.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(started); elapsed > shutdownGrace+time.Second {
		t.Errorf("the listener took %v to close, and §6.3 allows %v", elapsed, shutdownGrace)
	}

	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Errorf("%s is still accepting connections after the flow finished", address)
	}
}

// TestClosingTwiceIsNotAFailure, because Wait closes on its way out and a
// caller with a defer closes again. A shutdown that errored the second time
// would turn a working flow into a reported failure.
func TestClosingTwiceIsNotAFailure(t *testing.T) {
	s, err := Listen(testState)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
