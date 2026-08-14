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

// Package loopback answers the one HTTP request an OAuth flow has to receive.
//
// It is the only package outside internal/chat that may import net/http, and
// the exemption is narrow on purpose: it may listen and it may not speak.
// internal/lint holds that, by name, and fails on any client-side use of the
// package. The alternative was parsing a request line and a query string by
// hand to avoid the import, which is exactly the sort of code that is subtly
// wrong for a year.
//
// The listener binds 127.0.0.1 as an IP literal and never the name
// "localhost". SPEC.md §15.4 makes that a hard rule and the reason is not
// pedantry: a name is resolved by whatever the machine's resolver says, so an
// entry in /etc/hosts, a DNS search domain, or a resolver somebody else
// controls turns the redirect that carries the authorization code into a
// redirect to them.
package loopback

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// shutdownGrace bounds how long the listener stays up after it has what it came
// for (SPEC.md §6.3 asks for two seconds).
//
// It is not zero because the browser is still reading the response when the
// handler returns, and a connection torn down underneath it shows the user a
// failure page at the end of a flow that worked.
const shutdownGrace = 2 * time.Second

// Result is what came back on the redirect.
type Result struct {
	// Code is the authorization code. Empty when the user refused or the
	// authorization server reported a problem.
	Code string

	// Err is the authorization server's own refusal, already turned into
	// something a person can read.
	Err error
}

// Server is a listener waiting for exactly one callback.
type Server struct {
	listener net.Listener
	http     *http.Server
	result   chan Result

	// want is the state value this flow sent. A callback carrying anything
	// else is not answered.
	want string
}

// Listen starts the listener on an ephemeral loopback port.
//
// The port is chosen by the kernel, which is what makes this usable at all: a
// fixed port collides with whatever else is running and cannot be registered
// with Google in advance anyway. RFC 8252 §7.3 is why that is allowed, and why
// the redirect URI is registered as a loopback address with no port.
func Listen(state string) (*Server, error) {
	if state == "" {
		// A flow with no state has nothing to check the callback against, and
		// the check is the whole defence against a code injected by another
		// page in the same browser.
		return nil, errors.New("a loopback listener needs the state value to check the callback against")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("cannot listen on 127.0.0.1: %w", err)
	}

	s := &Server{
		listener: listener,
		result:   make(chan Result, 1),
		want:     state,
	}
	s.http = &http.Server{
		Handler: http.HandlerFunc(s.handle),

		// A browser that connects and says nothing would otherwise hold the
		// flow open until its own timeout.
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() { _ = s.http.Serve(listener) }()
	return s, nil
}

// RedirectURL is what the authorization server sends the browser back to.
func (s *Server) RedirectURL() string {
	return "http://" + s.listener.Addr().String() + "/"
}

// Wait blocks until the callback arrives, the context ends, or the flow times
// out, and then shuts the listener down.
//
// The shutdown is here rather than in the handler because a handler that stops
// its own server deadlocks: Shutdown waits for handlers to return.
func (s *Server) Wait(ctx context.Context) (Result, error) {
	defer func() { _ = s.Close() }()

	select {
	case result := <-s.result:
		return result, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Close shuts the listener down, giving the browser a moment to finish reading
// the page it was sent.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := s.http.Shutdown(ctx); err != nil {
		// Shutdown only fails by running out of time, and the listener is
		// closed either way, so there is nothing a caller could do about it.
		return s.listener.Close()
	}
	return nil
}

// handle answers the redirect.
//
// A request carrying neither a code nor an error is not the callback: a browser
// asks for /favicon.ico as soon as it renders the page, and treating that as
// the answer would end the flow before the real one arrived.
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	code, authErr, state := query.Get("code"), query.Get("error"), query.Get("state")

	if code == "" && authErr == "" {
		http.NotFound(w, r)
		return
	}

	// Constant time, and before anything else is looked at. SPEC.md §15.3: a
	// callback whose state does not match is rejected, and it is rejected
	// without saying why, because whoever sent it is not the person who started
	// this flow and telling them what was wrong with their guess helps only
	// them.
	if subtle.ConstantTimeCompare([]byte(state), []byte(s.want)) != 1 {
		http.NotFound(w, r)
		return
	}

	result := Result{Code: code}
	page := successPage
	if authErr != "" {
		result = Result{Err: describeAuthError(authErr, query.Get("error_description"))}
		page = refusedPage
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// No caching. The URL in the address bar holds the authorization code, and
	// a cached page is one more place it sits afterwards.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(page))

	// Buffered, so a second callback cannot block a handler forever. The first
	// one wins and the rest are answered and discarded.
	select {
	case s.result <- result:
	default:
	}
}

// describeAuthError turns the authorization server's code into a sentence.
//
// The two that matter are the two that happen. Everything else is passed
// through as the code, because inventing prose for a condition nobody has seen
// is how a message ends up describing the wrong thing.
func describeAuthError(code, description string) error {
	switch code {
	case "access_denied":
		return errors.New("consent was refused, so no authorization was granted")
	case "admin_policy_enforced":
		return errors.New("an administrator policy blocks this application for your organization.\n" +
			"This is the case a bring-your-own OAuth client exists for: a client created in your own " +
			"Cloud project, with an Internal user type, is not subject to third-party app access control")
	}

	if description != "" {
		return fmt.Errorf("the authorization server refused: %s (%s)", code, description)
	}
	return fmt.Errorf("the authorization server refused: %s", code)
}

// The pages are self-contained: no stylesheet, no font, no image, no script.
//
// A page served at the end of an authorization flow is the last thing between
// somebody and a working tool, and it renders on whatever browser they had
// open, possibly offline, possibly behind a proxy that blocks what it does not
// recognize. Anything fetched from elsewhere is a way for it to fail. It is
// also one more request carrying a Referer, and the referring URL here holds
// the authorization code.
const successPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Authorized</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 30rem; margin: 4rem auto; line-height: 1.5">
<h1 style="font-size: 1.25rem">Authorized</h1>
<p>You can close this window and go back to the terminal.</p>
</body></html>
`

const refusedPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Not authorized</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 30rem; margin: 4rem auto; line-height: 1.5">
<h1 style="font-size: 1.25rem">Not authorized</h1>
<p>Nothing was changed. The terminal has the details.</p>
</body></html>
`
