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

// Package chat is the only package in this repository that builds an HTTP
// request, and that is a security boundary rather than a matter of taste.
//
// Every credential this tool handles leaves the process inside a request: a
// bearer token in an Authorization header, or a webhook URL whose key and token
// query parameters are the whole of the authentication for one space. Redaction
// happens here, where the request is built, so that it holds for every caller
// rather than for every caller who remembered. internal/lint refuses an import
// of net/http anywhere else.
//
// The client is written by hand for the reasons in SPEC.md §7.1. The generated
// Google client is about forty thousand lines and drags a transport chain and
// OpenTelemetry along with it, for roughly eighteen endpoints, and it hands over
// no direct control of the 429 backoff, which matters here because Chat's
// per-space quota is shared with every other app acting in that space.
package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

const (
	// BaseURL is the Chat API root (SPEC.md §7.2).
	BaseURL = "https://chat.googleapis.com/v1"

	// UploadBaseURL is where a media upload goes. A different base path rather
	// than a different endpoint under the same one, which is easy to miss and
	// answers a 404 that otherwise makes no sense.
	UploadBaseURL = "https://chat.googleapis.com/upload/v1"
)

const (
	// DefaultTimeout bounds one attempt, not one operation (SPEC.md §7.4).
	//
	// The distinction is the whole of what --timeout means, and it was worth
	// deciding rather than discovering. A budget spent on the first attempt
	// leaves nothing for the other four, which turns the retry policy into
	// decoration; a budget spread across five attempts gives each of them six
	// seconds, which is short enough to fail a healthy request on a slow link.
	// So the flag bounds an attempt, the retry policy bounds how many there
	// are, and the flag help says so, because a caller who waits three times
	// longer than the number they typed is entitled to have been told.
	//
	// A caller who needs the operation bounded has the honest mechanism for it:
	// a context with a deadline, which every method here takes and the retry
	// loop honours between attempts.
	DefaultTimeout = 30 * time.Second

	// UploadTimeout is the attempt budget for media (SPEC.md §7.4). Set per
	// request rather than globally, because a thirty-second cap is right for an
	// API call and wrong for a file.
	UploadTimeout = 300 * time.Second
)

// maxResponseBytes bounds what one response may cost us in memory.
//
// The threat model says plainly that a hostile space can make requests slow or
// expensive. An unbounded io.ReadAll on a body somebody else controls is the
// cheapest way to be made to pay: a server that never stops writing turns a
// send into an out-of-memory kill. Eight mebibytes is far past any message or
// page of messages this API returns, and a body that exceeds it fails rather
// than being parsed short, because a truncated document that happens to parse
// is worse than one that does not.
const maxResponseBytes = 8 << 20

// Authorizer supplies the credential for a request.
//
// The two transports are not alike and this interface is shaped for both,
// deliberately and before the second one exists, because retrofitting it in
// Milestone 3 would mean rewriting the retry loop underneath it. A webhook
// carries its credential in the base URL's query, so it needs no Authorizer at
// all and passes nil. User OAuth carries a bearer token in a header, which can
// expire, which is why Refresh is here.
//
// It deals in a header value rather than in a request because implementations
// live outside this package, and only this package may name net/http.
type Authorizer interface {
	// Authorization returns the Authorization header value to send, or the
	// empty string when this credential does not travel in a header.
	Authorization(ctx context.Context) (string, error)

	// Refresh renews the credential after a 401 and reports whether it now
	// holds a different one. False means there is nothing new to retry with,
	// and the caller is out of authorization rather than out of luck.
	Refresh(ctx context.Context) (bool, error)
}

// Logger receives one line per request, response, and retry, at --verbose.
//
// An interface rather than an io.Writer so that the line stays a line: the
// renderer that implements this decides whether stderr is prose or a stream of
// JSON documents, and this package should not have to know which.
type Logger interface {
	Logf(format string, a ...any)
}

// Options configure a Client.
type Options struct {
	// BaseURL is what every request path is joined onto. For user OAuth it is
	// BaseURL above; for a webhook it is the whole endpoint including the query
	// that authenticates it, and the request path is then empty.
	BaseURL string

	// Transport is which of the two this is. Carried only so that a failure can
	// say something true: a 404 tells a user-OAuth caller to list their spaces
	// and tells a webhook caller that listing spaces is not something they can
	// do.
	Transport config.Transport

	// Profile names the profile in an error, per SPEC.md §8.2. Somebody running
	// this against four profiles needs to know which one failed.
	Profile string

	// Timeout bounds one attempt. Zero means DefaultTimeout.
	Timeout time.Duration

	// Auth is nil when the credential is in BaseURL.
	Auth Authorizer

	// HTTP is the client to send with. Zero means a new one. Whatever arrives
	// here is copied and has its redirect policy replaced, because that policy
	// is a security property of this package and not a setting.
	HTTP *http.Client

	// Log is nil unless --verbose.
	Log Logger
}

// Client is a Chat API client for one profile.
type Client struct {
	base      *url.URL
	transport config.Transport
	profile   string
	timeout   time.Duration
	auth      Authorizer
	http      *http.Client
	log       Logger

	// secrets are the credential values from the base URL, held so that they
	// can be struck out of anything this package is about to say. See scrub.
	secrets []string

	// Injected so that the retry policy can be tested as a policy rather than
	// as a clock. A test that asserts five attempts by taking fifteen seconds
	// is a test somebody eventually deletes.
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

// New builds a client for one profile.
func New(opts Options) (*Client, error) {
	base, err := parseBase(opts.BaseURL)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{}
	if opts.HTTP != nil {
		copied := *opts.HTTP
		httpClient = &copied
	}
	httpClient.CheckRedirect = refuseRedirect

	c := &Client{
		base:      base,
		transport: opts.Transport,
		profile:   opts.Profile,
		timeout:   opts.Timeout,
		auth:      opts.Auth,
		http:      httpClient,
		log:       opts.Log,
		secrets:   baseSecrets(base),
		now:       time.Now,
		sleep:     sleepFor,
		jitter:    fullJitter,
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	return c, nil
}

// parseBase reads the base URL and refuses one that would carry a credential in
// the clear.
//
// A webhook URL is a bearer credential wearing the costume of a URL, and http://
// puts it on the wire for anybody on the path. Google issues them as https and
// there is no legitimate plaintext case, so the only exception is a loopback
// address, which is what a test server is. The literal "localhost" is not
// accepted for it: it resolves through DNS and is therefore not necessarily
// loopback at all, which is the same reason SPEC.md §15.4 refuses it for the
// OAuth listener.
func parseBase(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, clientErr("no base URL was given, so there is nowhere to send this")
	}

	base, err := url.Parse(raw)
	if err != nil {
		// The URL is not quoted back. This field holds a webhook URL, and the
		// whole point of the rule is that its contents are a credential.
		return nil, clientErr("the base URL for this profile will not parse: %v", redactParseError(err))
	}
	if base.Host == "" {
		return nil, clientErr("the base URL for this profile names no host")
	}

	if base.Scheme == "https" {
		return base, nil
	}
	if base.Scheme == "http" && auth.IsLoopbackHost(base.Hostname()) {
		return base, nil
	}
	return nil, clientErr("the base URL for this profile is %s, and a credential does not travel over anything but https",
		schemeOrNone(base.Scheme))
}

func schemeOrNone(scheme string) string {
	if scheme == "" {
		return "not a URL with a scheme"
	}
	return scheme
}

// refuseRedirect stops the client following a 3xx anywhere.
//
// A redirect is how a credential reaches a host nobody chose. net/http strips
// the Authorization header when a redirect crosses origins, which covers the
// user-OAuth case and does nothing at all for the webhook one, where the
// credential is in the query string and travels with the URL. Rather than
// classify redirects, none are followed: the Chat API does not use them, and
// the 3xx is returned as the failure it is.
func refuseRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// baseSecrets collects the credential values out of the base URL's query.
//
// Held so that scrub can strike them out of anything this package is about to
// print. Short values are skipped: a two-character token would match all over
// an unrelated message and redact prose instead of a secret.
func baseSecrets(base *url.URL) []string {
	const shortest = 8

	var secrets []string
	for name, values := range base.Query() {
		if !auth.SecretParam(name) {
			continue
		}
		for _, value := range values {
			if len(value) < shortest {
				continue
			}
			secrets = append(secrets, value)
			if escaped := url.QueryEscape(value); escaped != value {
				secrets = append(secrets, escaped)
			}
		}
	}
	slices.Sort(secrets)
	return slices.Compact(secrets)
}

// Request is one call to the API.
type Request struct {
	Method string

	// Path is relative to the base, always, and is joined onto it rather than
	// substituted for it. See resolve.
	Path string

	// Query is merged over the base URL's own query. It is built by this
	// repository and never by a server, and resolve refuses a credential
	// parameter here regardless.
	Query url.Values

	// Body is marshalled as JSON when it is not nil.
	Body any

	// Idempotent says a replay of this request cannot produce a second side
	// effect, which for a POST means the caller supplied a message ID. See
	// safeToReplay: getting this wrong is how one send becomes two messages in
	// a space full of people.
	Idempotent bool

	// Timeout overrides the client's per-attempt budget. Zero means the
	// client's. Media uses it and nothing else should.
	Timeout time.Duration
}

// safeToReplay reports whether repeating this request cannot produce a second
// side effect.
//
// GET, HEAD, PUT and DELETE are idempotent by definition: repeating one leaves
// the same state behind, and a DELETE that answers 404 the second time has
// still done exactly what was asked. POST is not, and neither is PATCH, whose
// idempotence depends on what the patch says rather than on the method, so both
// have to opt in. Defaulting PATCH to safe would be right for the message
// update this API has and wrong the first time it is not.
func (r Request) safeToReplay() bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
		return true
	}
	return r.Idempotent
}

// do sends a request, retrying under the policy in SPEC.md §7.4, and returns
// the response body.
func (c *Client) do(ctx context.Context, req Request) ([]byte, error) {
	target, err := c.resolve(req)
	if err != nil {
		return nil, err
	}
	body, err := encodeBody(req.Body)
	if err != nil {
		return nil, err
	}

	refreshed := false
	for attempt := 1; ; attempt++ {
		payload, err := c.attempt(ctx, req, target, body)
		if err == nil {
			return payload, nil
		}
		setAttempts(err, attempt)

		if stop := c.prepareRetry(ctx, req, err, attempt, &refreshed); stop != nil {
			return nil, stop
		}
	}
}

// prepareRetry decides whether there is another attempt and gets ready for it.
//
// It returns nil to mean "go round again" and an error to mean "report this".
// The two outcomes are one function because they are one decision, and because
// the preparation is part of it: waiting is what makes a backoff a backoff, and
// refreshing is what makes the second attempt different from the first.
func (c *Client) prepareRetry(ctx context.Context, req Request, cause error, attempt int, refreshed *bool) error {
	kind := c.plan(cause, req, *refreshed)
	if kind == retryStop || attempt >= maxAttempts {
		return cause
	}

	if kind == retryRefresh {
		*refreshed = true
		return c.refresh(ctx, cause)
	}
	return c.wait(ctx, attempt, cause)
}

// attempt makes one request and reads one response.
func (c *Client) attempt(ctx context.Context, req Request, target *url.URL, body []byte) ([]byte, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = c.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := c.build(ctx, req, target, body)
	if err != nil {
		return nil, err
	}

	c.logRequest(httpReq)
	started := c.now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, c.transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.logResponse(resp, c.now().Sub(started))

	payload, err := readBody(resp)
	if err != nil {
		return nil, c.readError(err)
	}
	if resp.StatusCode/100 == 2 {
		return payload, nil
	}
	return nil, c.apiError(resp, payload)
}

// build turns a Request into an http.Request with the headers every call
// carries.
func (c *Client) build(ctx context.Context, req Request, target *url.URL, body []byte) (*http.Request, error) {
	// A fresh reader per attempt, because a retry re-reads it. Handing
	// http.NewRequestWithContext a *bytes.Reader also gives it ContentLength
	// and GetBody for free, which is what lets net/http replay the body itself
	// when it has to reopen a connection.
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, target.String(), reader)
	if err != nil {
		return nil, c.wrapTransport(err)
	}

	// SPEC.md §7.4: every request identifies this build, so that somebody
	// looking at Chat API traffic they did not expect can find out what is
	// making it.
	httpReq.Header.Set("User-Agent", meta.UserAgent())
	httpReq.Header.Set("Accept", "application/json")
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json; charset=UTF-8")
	}

	if c.auth == nil {
		return httpReq, nil
	}
	value, err := c.auth.Authorization(ctx)
	if err != nil {
		return nil, err
	}
	if value != "" {
		httpReq.Header.Set("Authorization", value)
	}
	return httpReq, nil
}

// errTooLarge is a response body past the limit. A sentinel rather than a
// message, because it is the one body failure that will happen again if we ask
// again, and the retry policy has to be able to tell.
var errTooLarge = errors.New("response too large")

// readBody reads the response, refusing one that is larger than we will hold.
//
// One byte past the limit is read on purpose. Reading exactly the limit cannot
// tell a body that fits from one that was cut off, and a document parsed from a
// truncated body is a wrong answer reported as a right one.
func readBody(resp *http.Response) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxResponseBytes {
		return nil, fmt.Errorf("the response is over %d bytes, which is more than this tool will hold in memory: %w",
			maxResponseBytes, errTooLarge)
	}
	return payload, nil
}

// resolve builds the URL for a request and holds the two rules that keep a
// credential where it belongs.
//
// The path is relative, always, and is joined onto the base rather than
// substituted for it. An absolute path, a scheme-relative one, or a walk up
// through .. would move the request to a host nobody chose, and the credential
// travels with it: for a webhook the credential is the query string of the very
// URL being rewritten.
//
// The query is merged over the base's own, so a webhook's key and token survive
// a request that adds a threadKey. A request may not set a credential
// parameter, even though every caller today is code in this repository, because
// the check costs one line and the failure it prevents is silent.
func (c *Client) resolve(req Request) (*url.URL, error) {
	target := *c.base
	if req.Path != "" {
		if err := checkRelative(req.Path); err != nil {
			return nil, err
		}
		target = *target.JoinPath(req.Path)
	}

	query := c.base.Query()
	for name, values := range req.Query {
		if auth.SecretParam(name) {
			return nil, clientErr("a request tried to set the %q query parameter, which is a credential and belongs to the profile", name)
		}
		query[name] = values
	}
	target.RawQuery = query.Encode()

	if err := c.sameOrigin(&target); err != nil {
		return nil, err
	}
	return &target, nil
}

// checkRelative refuses a path that is not one.
func checkRelative(path string) error {
	switch {
	case strings.HasPrefix(path, "/"):
		// Covers "//host/x" as well, which is scheme-relative and is the
		// shortest way to change hosts.
		return clientErr("the request path %q is absolute, and a path is joined onto the base rather than replacing it", path)
	case strings.Contains(path, "://"):
		return clientErr("the request path %q is a URL, and only a path may be joined onto the base", path)
	case strings.ContainsAny(path, "?#"):
		return clientErr("the request path %q carries a query or a fragment, which are set as values rather than written into the path", path)
	}

	if slices.Contains(strings.Split(path, "/"), "..") {
		return clientErr("the request path %q walks up out of the base path", path)
	}
	return nil
}

// sameOrigin is the second layer, and it exists because the first one is a
// parser.
//
// checkRelative decides what a path may look like, and a parser that accepts
// something it should not is a class of bug with a long history. This asks the
// built URL the question that actually matters: is this still the host, the
// scheme, and the subtree the profile's credential was issued for.
func (c *Client) sameOrigin(target *url.URL) error {
	if target.Scheme != c.base.Scheme || target.Host != c.base.Host {
		return clientErr("this request would go to %s rather than to %s, and the credential would go with it",
			hostOf(target), hostOf(c.base))
	}
	if !strings.HasPrefix(target.EscapedPath(), c.base.EscapedPath()) {
		return clientErr("this request would leave the base path %s", c.base.EscapedPath())
	}
	return nil
}

func hostOf(u *url.URL) string { return u.Scheme + "://" + u.Host }

// encodeBody marshals a request body.
//
// HTML escaping is off for the reason internal/output turns it off: Chat markup
// writes a link as <url|text> and a mention as <users/all>, so the default
// encoder would fill a message body with < escapes. Both forms decode to
// the same string, and the one that is readable in a dry run and in a --verbose
// log is worth having.
func encodeBody(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, clientErr("this request body cannot be encoded as JSON: %v", err)
	}
	return buf.Bytes(), nil
}

// logRequest writes the request to the verbose log, redacted.
//
// The Authorization header prints as REDACTED rather than being left out.
// Omitting it answers a different question from the one somebody reading a log
// is asking: they want to know whether a credential was sent, and a missing
// line says it was not.
func (c *Client) logRequest(req *http.Request) {
	if c.log == nil {
		return
	}

	c.logf("> %s %s", req.Method, auth.RedactURL(req.URL.String()))
	for _, name := range slices.Sorted(maps.Keys(req.Header)) {
		c.logf("> %s: %s", name, redactHeader(name, req.Header.Get(name)))
	}
}

func (c *Client) logResponse(resp *http.Response, elapsed time.Duration) {
	if c.log == nil {
		return
	}

	c.logf("< %s in %s", resp.Status, elapsed.Round(time.Millisecond))
	if after := resp.Header.Get("Retry-After"); after != "" {
		c.logf("< Retry-After: %s", after)
	}
}

// sensitiveHeaders are the ones whose value is a credential.
//
// X-Goog-Api-Key is here because it is how an API key travels when it is not in
// the query string, and this tool would rather redact a header it never sets
// than acquire one later and forget.
var sensitiveHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"Cookie":              true,
	"Set-Cookie":          true,
	"X-Goog-Api-Key":      true,
}

func redactHeader(name, value string) string {
	if sensitiveHeaders[http.CanonicalHeaderKey(name)] {
		return auth.Redacted
	}
	return value
}

// logf writes one line to the verbose log, with the profile's credentials
// struck out of it.
func (c *Client) logf(format string, a ...any) {
	if c.log == nil {
		return
	}
	c.log.Logf("%s", c.scrub(fmt.Sprintf(format, a...)))
}

// scrub removes the profile's own credential values from a string.
//
// This is the backstop, and it is here because redaction that depends on
// knowing the shape of a value only works on values whose shape was
// anticipated. A *url.Error from net/http quotes the whole URL it failed on,
// query string and all; a future error message could quote something else. The
// values in a webhook URL are known exactly, so anything about to be printed
// can be checked against them rather than against a pattern.
func (c *Client) scrub(s string) string {
	for _, secret := range c.secrets {
		s = strings.ReplaceAll(s, secret, auth.Redacted)
	}
	return s
}

// clientErr is a failure in what was asked for rather than in what came back.
// Exit 2: nothing was sent, and no retry changes the answer.
func clientErr(format string, a ...any) error {
	return output.Errorf("REQUEST", output.ExitUsage, format, a...)
}

// redactParseError keeps a malformed base URL out of the message that reports
// it.
//
// url.Parse wraps its reason in a *url.Error, whose Error method quotes the URL
// it was given back at you. What it was given is a webhook URL, so the reason is
// taken out and the quotation is left behind.
func redactParseError(err error) string {
	if e, ok := errors.AsType[*url.Error](err); ok {
		return e.Err.Error()
	}
	return "it is not a URL"
}
