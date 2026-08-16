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
	"net/http"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// TestTheExitCodeComesFromTheFrozenTable.
//
// The codes are a contract with every script and agent that calls this tool, so
// the mapping is written out here rather than derived. The three that matter:
// an expired authorization is 4 because the caller has to log in again, a rate
// limit that outlived the backoff is 6 because the caller has to wait, and
// everything else that came back from the API is 3. No fourth bucket is
// invented alongside them.
func TestTheExitCodeComesFromTheFrozenTable(t *testing.T) {
	for _, tc := range []struct {
		status int
		name   string
		want   output.ExitCode
	}{
		{http.StatusBadRequest, "INVALID_ARGUMENT", output.ExitAPI},
		{http.StatusUnauthorized, "UNAUTHENTICATED", output.ExitAuthRequired},
		{http.StatusForbidden, "PERMISSION_DENIED", output.ExitAPI},
		{http.StatusNotFound, "NOT_FOUND", output.ExitAPI},
		{http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", output.ExitRateLimited},
		{http.StatusInternalServerError, "INTERNAL", output.ExitAPI},
		{http.StatusServiceUnavailable, "UNAVAILABLE", output.ExitAPI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := wrapFor(tc.status, tc.name)
			if got := output.ExitCodeOf(err); got != tc.want {
				t.Errorf("HTTP %d exit code = %d, want %d", tc.status, got, tc.want)
			}
		})
	}
}

// TestTheSentinelsAreReachable. A caller branches on errors.Is rather than on a
// status code, so the four conditions worth branching on have names, and those
// names have to survive the wrapping that carries the exit code.
func TestTheSentinelsAreReachable(t *testing.T) {
	for _, tc := range []struct {
		status   int
		name     string
		sentinel error
	}{
		{http.StatusNotFound, "NOT_FOUND", ErrNotFound},
		{http.StatusForbidden, "PERMISSION_DENIED", ErrPermission},
		{http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", ErrRateLimited},
		{http.StatusUnauthorized, "UNAUTHENTICATED", ErrAuthExpired},
	} {
		err := &APIError{StatusCode: tc.status, Status: tc.name, wrapped: wrapFor(tc.status, tc.name)}
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("HTTP %d does not match its sentinel", tc.status)
		}
	}

	// And a status with no sentinel matches none of them, rather than matching
	// whichever one happens to be first.
	other := &APIError{StatusCode: 400, Status: "INVALID_ARGUMENT", wrapped: wrapFor(400, "INVALID_ARGUMENT")}
	for _, sentinel := range []error{ErrNotFound, ErrPermission, ErrRateLimited, ErrAuthExpired} {
		if errors.Is(other, sentinel) {
			t.Errorf("a 400 matched %v", sentinel)
		}
	}
}

// TestTheJSONErrorCodeIsTheAPIStatus. SPEC.md §11.2 gives the example
// {"error":{"code":"PERMISSION_DENIED",...}}, so the code a program reads is the
// API's own name for the condition rather than a number or a word of ours.
func TestTheJSONErrorCodeIsTheAPIStatus(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusForbidden, "PERMISSION_DENIED", "The caller does not have permission", "IAM_PERMISSION_DENIED")
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a 403 was reported as a successful send")
	}

	var buf strings.Builder
	if got := output.Report(&buf, err, true); got != output.ExitAPI {
		t.Errorf("exit code = %d, want %d", got, output.ExitAPI)
	}
	if !strings.Contains(buf.String(), `"code":"PERMISSION_DENIED"`) {
		t.Errorf("the JSON error does not carry the API status:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `"exit_code":3`) {
		t.Errorf("the JSON error does not carry the exit code:\n%s", buf.String())
	}
}

// TestA403OnAWebhookNamesTheLikelyCause.
//
// Appendix A.10, and the reason this branch exists at all. Somebody reading
// PERMISSION_DENIED concludes their URL is wrong and copies it again, which
// changes nothing, because what blocked them is an administrator setting they
// cannot see. The message has to say so, since the person reading it has no way
// to find out.
func TestA403OnAWebhookNamesTheLikelyCause(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusForbidden, "PERMISSION_DENIED", "The caller does not have permission", "IAM_PERMISSION_DENIED")
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a 403 was reported as a successful send")
	}

	message := err.Error()
	for _, want := range []string{"Chat apps", "organizational unit", "administrator", "alerts"} {
		if !strings.Contains(message, want) {
			t.Errorf("the 403 message is missing %q:\n%s", want, message)
		}
	}
}

// TestABadWebhookKeyIsExplained.
//
// A recon finding rather than a guess: a live POST to a webhook-shaped URL with
// a bad key answers 400 INVALID_ARGUMENT with reason API_KEY_INVALID, not 401 or
// 403. That is the most likely thing to go wrong with a webhook profile, so it
// is the message most worth being useful, and the reason is what it keys off,
// because the prose can be reworded and the reason cannot.
func TestABadWebhookKeyIsExplained(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"API key not valid. Please pass a valid API key.", "API_KEY_INVALID")
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a 400 was reported as a successful send")
	}
	if !strings.Contains(err.Error(), "Copy it again") {
		t.Errorf("the message does not say what to do about a bad webhook URL:\n%v", err)
	}
	if h.count() != 1 {
		t.Errorf("made %d requests, want 1: a bad key is not retried", h.count())
	}
}

// TestA404TellsEachTransportSomethingItCanActed On is split in two because the
// same status has two different fixes, and giving a webhook user the user-OAuth
// answer sends them to a command their profile cannot run.
func TestA404TellsEachTransportSomethingItCanActOn(t *testing.T) {
	t.Run("useroauth", func(t *testing.T) {
		h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "Requested entity was not found.", "")
		})
		h.client.transport = config.TransportUserOAuth

		_, err := sendOne(h)
		if err == nil {
			t.Fatal("a 404 was reported as a successful send")
		}
		if want := meta.AppName + " spaces list"; !strings.Contains(err.Error(), want) {
			t.Errorf("the 404 message does not suggest %q:\n%v", want, err)
		}
	})

	t.Run("webhook", func(t *testing.T) {
		h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "Requested entity was not found.", "")
		})

		_, err := sendOne(h)
		if err == nil {
			t.Fatal("a 404 was reported as a successful send")
		}
		if strings.Contains(err.Error(), meta.AppName+" spaces list") {
			t.Errorf("the 404 message sends a webhook profile to a command it cannot run:\n%v", err)
		}
		if !strings.Contains(err.Error(), "cannot list spaces") {
			t.Errorf("the 404 message does not say what a webhook profile can do instead:\n%v", err)
		}
	})
}

// TestTheUserOAuthAdviceNamesTheProfile. Somebody running this against four
// profiles in one script needs to know which one to log in again, and the
// server's message cannot tell them.
func TestTheUserOAuthAdviceNamesTheProfile(t *testing.T) {
	for _, tc := range []struct {
		status int
		name   string
		want   string
	}{
		{http.StatusUnauthorized, "UNAUTHENTICATED", meta.AppName + " auth login --profile alerts"},
		{http.StatusForbidden, "PERMISSION_DENIED", "Profile: alerts."},
	} {
		h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
			writeAPIError(w, tc.status, tc.name, "Denied.", "")
		})
		h.client.transport = config.TransportUserOAuth
		h.client.auth = staticAuthorizer{header: "Bearer expired"}

		_, err := sendOne(h)
		if err == nil {
			t.Fatalf("a %d was reported as a successful send", tc.status)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("the %d message is missing %q:\n%v", tc.status, tc.want, err)
		}
	}
}

// TestReasonReadsTheDetails against the detail shape the live API returns.
func TestReasonReadsTheDetails(t *testing.T) {
	e := &APIError{}
	if got := e.Reason(); got != "" {
		t.Errorf("Reason with no details = %q", got)
	}

	e.Details = rawDetails(t,
		`{"@type":"type.googleapis.com/google.rpc.LocalizedMessage","locale":"en-US","message":"API key not valid."}`,
		`{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID","domain":"googleapis.com"}`,
	)
	if got := e.Reason(); got != "API_KEY_INVALID" {
		t.Errorf("Reason = %q, want API_KEY_INVALID", got)
	}
}

// TestABodyThatIsNotTheEnvelopeIsNotQuoted. A proxy that answers with an HTML
// page is not the API, and pasting its markup into a terminal helps nobody.
func TestABodyThatIsNotTheEnvelopeIsNotQuoted(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, "<html><head><title>502 Bad Gateway</title></head></html>")
	})

	_, err := h.client.do(context.Background(), Request{Method: http.MethodGet})
	if err == nil {
		t.Fatal("an HTML 502 was reported as success")
	}
	if strings.Contains(err.Error(), "<html>") {
		t.Errorf("the failure quotes the body back:\n%v", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("the failure does not say what the status was:\n%v", err)
	}
}

// TestAnErrorMessageFromTheServerIsScrubbed.
//
// The far end chooses this string. A server that echoes the request URL into
// its own error message would otherwise put the credential into ours, and this
// is the backstop for every path where a value we did not write reaches a
// stream: the profile's secrets are known exactly, so anything about to be
// printed is checked against them rather than against a pattern.
func TestAnErrorMessageFromTheServerIsScrubbed(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"Bad request for "+r.URL.String(), "BAD_REQUEST")
	})

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a 400 was reported as a successful send")
	}
	for _, secret := range []string{testKey, testToken} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("a credential came back out through the server's own message:\n%v", err)
		}
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("nothing was redacted, so the scrub did not run:\n%v", err)
	}
}

// TestTheErrorReadsAsASentence, because it is the only thing most callers will
// ever see of this package.
func TestTheErrorReadsAsASentence(t *testing.T) {
	e := &APIError{
		StatusCode: http.StatusForbidden,
		Status:     "PERMISSION_DENIED",
		Message:    "The caller does not have permission",
		advice:     "Chat apps are turned off.",
		Attempts:   1,
		wrapped:    wrapFor(http.StatusForbidden, "PERMISSION_DENIED"),
	}
	want := "The caller does not have permission (HTTP 403 PERMISSION_DENIED)\nChat apps are turned off."
	if got := e.Error(); got != want {
		t.Errorf("Error() =\n%q\nwant\n%q", got, want)
	}

	// One attempt is the ordinary case and says nothing; more than one is worth
	// reporting, because a failure after five tries does not read like a
	// failure after one.
	e.Attempts = 5
	if !strings.Contains(e.Error(), "Gave up after 5 attempts.") {
		t.Errorf("Error() does not report the attempts:\n%s", e.Error())
	}

	transport := &APIError{Status: "UNAVAILABLE", Message: "Post to REDACTED failed: connection refused"}
	if got := transport.Error(); got != "Post to REDACTED failed: connection refused" {
		t.Errorf("a transport failure reads as %q", got)
	}
}

func rawDetails(t *testing.T, raw ...string) []json.RawMessage {
	t.Helper()

	details := make([]json.RawMessage, 0, len(raw))
	for _, one := range raw {
		details = append(details, json.RawMessage(one))
	}
	return details
}

// TestAChatAppNotFoundIsNotAMissingSpace.
//
// Measured on 2026-08-16. A Cloud project with the Chat API enabled but no Chat
// app configured on it reads everything and writes nothing: spaces, messages
// and members all answered 200, and messages.create, messages.patch,
// messages.delete and reactions.create all answered this 404.
//
// The generic 404 advice is "run spaces list to see the spaces this profile can
// reach", which is worse than saying nothing. It is not about the space, and
// `spaces list` is exactly the thing that still works, so somebody follows it,
// sees their spaces, and is no closer.
func TestAChatAppNotFoundIsNotAMissingSpace(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND",`+
			`"message":"Google Chat app not found. To create a Chat app, you must turn on the Chat API `+
			`and configure the app in the Google Cloud console."}}`)
	})
	h.client.transport = config.TransportUserOAuth

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a 404 was reported as a successful send")
	}

	if strings.Contains(err.Error(), "spaces list") {
		t.Errorf("the advice sends somebody to the command that still works:\n%v", err)
	}
	for _, want := range []string{
		"not about the space", // what it is not.
		"Configuration",       // where to go.
		"Cloud console",       // where that is.
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the advice does not mention %q:\n%v", want, err)
		}
	}
}

// TestAnOrdinary404StillSaysToListSpaces, so that the branch above is a special
// case and not a replacement. A space that really has gone is the common 404,
// and `spaces list` is the right answer to it.
func TestAnOrdinary404StillSaysToListSpaces(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"Requested entity was not found."}}`)
	})
	h.client.transport = config.TransportUserOAuth

	_, err := sendOne(h)
	if err == nil {
		t.Fatal("a 404 was reported as a successful send")
	}
	if !strings.Contains(err.Error(), "spaces list") {
		t.Errorf("an ordinary 404 lost its advice:\n%v", err)
	}
}
