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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// The sentinels from SPEC.md §7.5. A caller tests for one with errors.Is rather
// than reading a status code, so that the four conditions worth branching on
// have names.
var (
	ErrNotFound    = errors.New("no such space or message")
	ErrPermission  = errors.New("not permitted")
	ErrRateLimited = errors.New("rate limited")
	ErrAuthExpired = errors.New("authorization expired")

	// ErrInvalidRequest is a 400: the API could not make sense of what was
	// asked, as opposed to understanding it and having nothing to return.
	//
	// A fifth sentinel beyond SPEC.md §7.5's four, added because the difference
	// between it and ErrNotFound is a difference a person acts on. Measured on
	// spaces:findDirectMessage, which answers 400 for a user reference it cannot
	// resolve to anybody and 404 for a real person with no direct message: one
	// means check the spelling and the other means open the conversation once.
	// A caller with only a status code would have to import net/http to tell
	// them apart, and only internal/chat may.
	ErrInvalidRequest = errors.New("the request was not understood")

	// ErrTruncated is a walk that ended before the far end ran out of pages,
	// for a reason that is not the caller's doing and not a request failure.
	// Its own sentinel so that a caller can tell "this answer is short" from
	// "this request failed", which are different things to act on.
	ErrTruncated = errors.New("the result is incomplete")
)

// APIError is a request that came back wrong (SPEC.md §7.5).
//
// It also covers a request that did not come back at all, with StatusCode zero
// and Status UNAVAILABLE. One type rather than two because the caller's question
// is the same either way, "did this send happen", and because the retry policy
// has to answer it for both.
type APIError struct {
	// StatusCode is the HTTP status, or 0 when there was no response.
	StatusCode int

	// Status is the API's own name for the condition: PERMISSION_DENIED,
	// INVALID_ARGUMENT, UNAUTHENTICATED. Google returns it as a string in the
	// error body, and it is more specific than the HTTP code: a bad webhook URL
	// and a malformed message are both 400.
	Status string

	// Message is what the far end said, or what we can say about a failure that
	// never reached it.
	Message string

	// Details are the google.rpc detail objects, kept raw. Each one is tagged
	// with an @type and this tool understands one of them, so decoding the rest
	// would be inventing structure nobody reads.
	Details []json.RawMessage

	// Retryable is whether this condition is retryable in principle. Whether it
	// was actually retried also depends on the request: a 503 is retryable and a
	// POST without a message ID is still not replayed. See plan.
	Retryable bool

	// RetryAfter is what the server asked us to wait, or zero.
	RetryAfter time.Duration

	// Attempts is how many were made. Reported so that a failure after five
	// tries does not read like a failure after one.
	Attempts int

	// advice is what this tool knows that the server's message does not say.
	advice string

	// wrapped carries the exit code and the machine-readable code out to
	// internal/output, and the sentinel out to errors.Is. Both travel by
	// unwrapping rather than by a method, because output.ExitCodeOf walks the
	// error chain and knows nothing about this package.
	wrapped error

	// cause is the transport failure underneath, when there was one. Kept so
	// that the retry policy can ask whether it happened at the dial stage,
	// which is the one case where replaying a POST is safe.
	cause error
}

func (e *APIError) Error() string {
	var b strings.Builder

	switch {
	case e.StatusCode == 0:
		b.WriteString(e.Message)
	case e.Status != "":
		fmt.Fprintf(&b, "%s (HTTP %d %s)", e.Message, e.StatusCode, e.Status)
	default:
		fmt.Fprintf(&b, "%s (HTTP %d)", e.Message, e.StatusCode)
	}

	if e.advice != "" {
		b.WriteString("\n")
		b.WriteString(e.advice)
	}
	if e.Attempts > 1 {
		fmt.Fprintf(&b, "\nGave up after %d attempts.", e.Attempts)
	}
	return b.String()
}

// Unwrap returns both halves of what this error carries: the output.Error that
// holds the exit code and the sentinel, and the transport failure underneath
// when there was one.
//
// Two rather than one because they answer different questions and both get
// asked. output.ExitCodeOf needs the first to know how the process should
// leave; the retry policy needs the second to know whether a dial ever
// happened. Chaining them in sequence would work and would also mean the order
// of two unrelated things mattered.
func (e *APIError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.wrapped}
	}
	return []error{e.wrapped, e.cause}
}

// Reason returns the machine-readable reason from the first google.rpc.ErrorInfo
// among the details, or the empty string.
//
// It is worth having because it is the only part of an error body that is
// stable. A live 400 from a webhook URL with a bad key carries the message "API
// key not valid. Please pass a valid API key.", which is prose that can be
// reworded, and the reason API_KEY_INVALID, which cannot.
func (e *APIError) Reason() string {
	for _, raw := range e.Details {
		var info struct {
			Type   string `json:"@type"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			continue
		}
		if info.Reason != "" {
			return info.Reason
		}
	}
	return ""
}

// errorEnvelope is the shape every Google API error body has. Confirmed against
// the live API rather than assumed: an unauthenticated GET and a webhook POST
// with a bad key both answer with exactly this, details included.
type errorEnvelope struct {
	Error struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Status  string            `json:"status"`
		Details []json.RawMessage `json:"details"`
	} `json:"error"`
}

// apiError builds the error for a response that was not a 2xx.
func (c *Client) apiError(resp *http.Response, payload []byte) error {
	e := &APIError{
		StatusCode: resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), c.now()),
		Retryable:  retryableStatus(resp.StatusCode),
	}

	var envelope errorEnvelope
	if err := json.Unmarshal(payload, &envelope); err == nil && envelope.Error.Message != "" {
		e.Status = envelope.Error.Status
		e.Message = envelope.Error.Message
		e.Details = envelope.Error.Details
	} else {
		// A body that is not the envelope is a proxy page, an empty 3xx, or
		// something else that never reached the API. Saying so beats quoting
		// HTML at somebody.
		e.Message = "the server answered " + resp.Status + " with no error message"
	}

	e.Message = c.scrub(e.Message)
	e.Details = c.scrubDetails(e.Details)
	e.advice = c.advise(e, resp)
	e.wrapped = wrapFor(e.StatusCode, e.Status)
	return e
}

// scrubDetails strikes the profile's credentials out of the detail objects.
//
// Details are kept raw and nothing prints them today, which is exactly why they
// are scrubbed here rather than wherever something first does. A server that
// echoed a request parameter into a detail would otherwise be handing a
// credential to whichever future caller decides these are worth showing.
//
// Replacement inside the encoded JSON is safe: what comes out is still a valid
// document, because a secret that appears verbatim is inside a string and
// REDACTED is an ordinary run of characters.
func (c *Client) scrubDetails(details []json.RawMessage) []json.RawMessage {
	if len(c.secrets) == 0 {
		return details
	}

	for i, raw := range details {
		details[i] = json.RawMessage(c.scrub(string(raw)))
	}
	return details
}

// transportError is a request that never got an answer.
func (c *Client) transportError(err error) error {
	message := c.scrub(describeTransport(err))

	return &APIError{
		StatusCode: 0,
		Status:     "UNAVAILABLE",
		Message:    message,
		Retryable:  true,
		advice:     "",
		wrapped:    output.Errorf("UNAVAILABLE", output.ExitAPI, "%s", message),
		cause:      err,
	}
}

// readError is a response whose body would not read.
//
// The two cases are not alike, and the retry policy needs them apart. A body
// cut off partway through is a connection that failed and is worth another
// attempt; a body over the size limit will be over it again, and retrying is
// how a hostile server gets us to download the same eight mebibytes five times.
func (c *Client) readError(err error) error {
	message := c.scrub(err.Error())

	return &APIError{
		StatusCode: 0,
		Status:     "UNAVAILABLE",
		Message:    message,
		Retryable:  !errors.Is(err, errTooLarge),
		wrapped:    output.Errorf("UNAVAILABLE", output.ExitAPI, "%s", message),
		cause:      err,
	}
}

// wrapTransport is for a local failure that will not change on a second
// attempt: a request that would not build, a response that would not decode.
func (c *Client) wrapTransport(err error) error {
	message := c.scrub(err.Error())

	return &APIError{
		StatusCode: 0,
		Status:     "UNAVAILABLE",
		Message:    message,
		Retryable:  false,
		wrapped:    output.Errorf("UNAVAILABLE", output.ExitAPI, "%s", message),
		cause:      err,
	}
}

// describeTransport turns a net/http failure into a sentence with no credential
// in it.
//
// This is the leak the whole package is arranged to prevent. net/http wraps
// every failure in a *url.Error whose Error method reads
//
//	Post "https://chat.googleapis.com/v1/spaces/AAA/messages?key=...&token=...": dial tcp: ...
//
// so the default rendering of a connection failure publishes the credential for
// the space. The URL is redacted and the reason is kept. scrub runs over the
// result as well, because the reason came from somewhere else.
func describeTransport(err error) string {
	e, ok := errors.AsType[*url.Error](err)
	if !ok {
		return err.Error()
	}

	reason := e.Err.Error()
	switch {
	case e.Timeout():
		return fmt.Sprintf("%s %s timed out: %s", e.Op, auth.RedactURL(e.URL), reason)
	default:
		return fmt.Sprintf("%s %s failed: %s", e.Op, auth.RedactURL(e.URL), reason)
	}
}

// wrapFor builds the chain that carries an exit code and a sentinel.
//
// The chain is APIError -> *output.Error -> sentinel. output.ExitCodeOf finds
// the middle one and reads the frozen code off it; errors.Is finds the last one.
// The codes are the ones already in the table and no new taxonomy is invented
// alongside them: an expired authorization is 4, a rate limit that outlived the
// backoff is 6, and everything else that came back from the API is 3.
func wrapFor(status int, apiStatus string) error {
	code := apiStatus
	if code == "" {
		code = "API_ERROR"
	}

	exit := output.ExitAPI
	var sentinel error

	switch status {
	case http.StatusUnauthorized:
		exit, sentinel = output.ExitAuthRequired, ErrAuthExpired
	case http.StatusTooManyRequests:
		// Any 429 a caller sees is one that outlived the backoff, because the
		// loop retries every one of them until the attempts run out. So it is
		// always exit 6, whose meaning is "wait", not "investigate".
		exit, sentinel = output.ExitRateLimited, ErrRateLimited
	case http.StatusForbidden:
		sentinel = ErrPermission
	case http.StatusNotFound:
		sentinel = ErrNotFound
	case http.StatusBadRequest:
		sentinel = ErrInvalidRequest
	}

	return &output.Error{Code: code, Exit: exit, Message: code, Err: sentinel}
}

// retryableStatus reports whether a status is retryable in principle
// (SPEC.md §7.4). 403 is deliberately absent: a permission denial does not
// become a permission grant by being asked again.
func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusUnauthorized ||
		status >= 500
}

// advise says what this tool knows and the server's message does not.
//
// The split by transport is the point. The same status means different things
// and has different fixes depending on how the profile authenticates, and the
// person reading the error has no way to know which: a 404 tells a user-OAuth
// caller to go and list their spaces, and telling a webhook caller the same
// thing sends them to a command their profile cannot run.
func (c *Client) advise(e *APIError, resp *http.Response) string {
	if resp.StatusCode/100 == 3 {
		return "The server answered with a redirect, which is not followed, because the credential for this profile would travel to wherever it pointed."
	}
	if c.transport == config.TransportWebhook {
		return c.adviseWebhook(e)
	}
	return c.adviseOAuth(e, resp.Request)
}

func (c *Client) adviseWebhook(e *APIError) string {
	switch {
	case e.StatusCode == http.StatusForbidden:
		// Appendix A.10, and the reason this branch exists at all. Somebody
		// reading PERMISSION_DENIED reasonably concludes their URL is wrong and
		// goes and copies it again, which changes nothing, because the setting
		// that blocked them is one only an administrator can see.
		return "A webhook that is refused like this usually means Chat apps are turned off for the organizational unit that owns this space. " +
			"That is an administrator setting rather than a problem with the URL, so re-copying the webhook will not change it." +
			c.profileSuffix()

	case e.StatusCode == http.StatusBadRequest && strings.Contains(e.Reason(), "API_KEY"):
		return "The key in this webhook URL was not accepted, so the URL is incomplete, mistyped, or has been revoked. " +
			"Copy it again from the space, under Apps & integrations, Webhooks." + c.profileSuffix()

	case e.StatusCode == http.StatusNotFound:
		return "The space this webhook posted to no longer exists, or the webhook itself was deleted. " +
			"A webhook profile cannot list spaces, so the answer is to copy a current URL from the space in Chat." + c.profileSuffix()

	case e.StatusCode == http.StatusUnauthorized:
		return "This URL carries its own credential and should never need an account, so a request for one means the URL is not a webhook URL." + c.profileSuffix()
	}
	return ""
}

func (c *Client) adviseOAuth(e *APIError, req *http.Request) string {
	switch e.StatusCode {
	case http.StatusForbidden:
		// An edit is the one write with a rule people do not expect, and it was
		// measured rather than guessed: editing a message this account sent
		// answers 200, and editing one somebody else sent answers 403, in the
		// same space, on the same token, a second apart. Delete does not behave
		// that way, so this says edit and nothing more.
		//
		// The generic sentence stays underneath it. A 403 on an edit can also
		// be a space this account cannot write in at all, and replacing the
		// general answer with the likely one would send somebody looking in the
		// wrong place on the day it is the other thing.
		if req != nil && req.Method == http.MethodPatch {
			return "Chat only lets the author edit a message, so this usually means somebody else sent it. " +
				"The account this profile is authorized as is not allowed to do that here." + c.profileSuffix()
		}
		return "The account this profile is authorized as is not allowed to do that here." + c.profileSuffix()

	case http.StatusNotFound:
		// The Chat API answers "Google Chat app not found" with a 404 on every
		// write, for a client whose Cloud project has the API enabled but no
		// Chat app configured on it. Nothing about that is a missing space, and
		// the generic advice below is actively wrong for it: `spaces list`
		// works, because reading is exactly what still works, so somebody
		// follows it, sees their spaces, and learns nothing.
		//
		// Measured on 2026-08-16. Every read returned 200 and every write,
		// including messages.create, returned this. It is the difference
		// between turning the API on and configuring the app that uses it.
		if strings.Contains(e.Message, "Chat app not found") {
			return "This is not about the space. Enabling the Chat API is not the same as configuring a Chat app on it, " +
				"and every write needs the second: reading works on this profile and posting, editing, deleting and reacting do not. " +
				"Configure the app under Chat API, Configuration in the Google Cloud console for the project this client belongs to." +
				c.profileSuffix()
		}
		return "Run '" + meta.AppName + " spaces list' to see the spaces this profile can reach." + c.profileSuffix()

	case http.StatusUnauthorized:
		suffix := ""
		if c.profile != "" {
			suffix = " --profile " + c.profile
		}
		return "Run '" + meta.AppName + " auth login" + suffix + "'."
	}
	return ""
}

// profileSuffix names the profile, per SPEC.md §8.2. Somebody running this
// against four profiles in one script needs to know which one failed, and the
// server's message cannot tell them.
func (c *Client) profileSuffix() string {
	if c.profile == "" {
		return ""
	}
	return "\nProfile: " + c.profile + "."
}

// setAttempts records how many attempts were made on the error that ended them.
func setAttempts(err error, attempts int) {
	if e, ok := errors.AsType[*APIError](err); ok {
		e.Attempts = attempts
	}
}
