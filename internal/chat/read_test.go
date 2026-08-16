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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
)

// bearer is an Authorizer that never refreshes, standing in for the token
// source internal/auth builds. Nothing here exercises refreshing: that is
// internal/auth's own test, and a copy of it living here would be a second
// place to keep it true.
type bearer struct{}

func (bearer) Authorization(context.Context) (string, error) { return "Bearer test-access", nil }
func (bearer) Refresh(context.Context) (bool, error)         { return false, nil }

// reader is a user-OAuth client pointed at a test server.
type reader struct {
	client *Client
	server *httptest.Server

	mu       sync.Mutex
	requests []string
}

// paths returns the request paths and queries this server was asked for, in
// order. Recorded rather than counted, because the interesting assertions are
// about what pageSize and pageToken were asked for rather than about how many
// requests happened.
func (r *reader) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

func (r *reader) count() int { return len(r.paths()) }

func newReader(t *testing.T, handler http.HandlerFunc) *reader {
	t.Helper()

	r := &reader{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, req.URL.RequestURI())
		r.mu.Unlock()

		handler(w, req)
	}))
	t.Cleanup(r.server.Close)

	client, err := New(Options{
		BaseURL:   r.server.URL + "/v1",
		Transport: config.TransportUserOAuth,
		Profile:   "work",
		Auth:      bearer{},
		HTTP:      r.server.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The retry policy is tested as a policy in retry_test.go. Here it only has
	// to not turn a 500 fixture into fifteen seconds of wall clock, so the sleep
	// is recorded rather than taken.
	client.jitter = func(window time.Duration) time.Duration { return window }
	client.sleep = func(context.Context, time.Duration) error { return nil }

	r.client = client
	return r
}

// pagesOf builds a handler that serves a fixed number of message pages, each
// holding one message, with a next-page token until the last.
func pagesOf(t *testing.T, total int) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, req *http.Request) {
		page := 0
		if token := req.URL.Query().Get("pageToken"); token != "" {
			if _, err := fmt.Sscanf(token, "page-%d", &page); err != nil {
				t.Errorf("the client sent a page token this server never issued: %q", token)
			}
		}

		next := ""
		if page+1 < total {
			next = fmt.Sprintf(`"nextPageToken": "page-%d",`, page+1)
		}
		_, _ = fmt.Fprintf(w, `{%s "messages": [{"name": "spaces/AAA/messages/m%d", "text": "message %d"}]}`,
			next, page, page)
	}
}

// collect drains an iterator into a slice, stopping at the first error.
func collect[T any](items func(func(T, error) bool)) ([]T, error) {
	var out []T
	for item, err := range items {
		if err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, nil
}

// TestAListStopsAtTheLimitRatherThanReadingEverything.
//
// The claim from m3-04: a caller asking for three gets exactly three. Getting
// this wrong in the other direction is the failure m4-07 names, where a
// truncated answer is reported as the whole thing, and getting it wrong this way
// spends a per-space quota shared with every other app in the space.
func TestAListStopsAtTheLimitRatherThanReadingEverything(t *testing.T) {
	r := newReader(t, pagesOf(t, 10))

	got, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{
		Space: "spaces/AAA",
		Limit: 3,
	}))
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d messages, want 3", len(got))
	}

	// Three pages of one, and then it stopped. A fourth request would mean the
	// limit was applied after fetching rather than as part of it.
	if r.count() != 3 {
		t.Errorf("made %d requests for a limit of 3: %v", r.count(), r.paths())
	}
}

// TestAListAsksForOnlyWhatIsStillWanted.
//
// pageSize shrinks as the limit is approached, so a --limit 3 never asks a
// server for a thousand messages and throws away 997 of them.
func TestAListAsksForOnlyWhatIsStillWanted(t *testing.T) {
	r := newReader(t, pagesOf(t, 10))

	if _, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{
		Space: "spaces/AAA",
		Limit: 3,
	})); err != nil {
		t.Fatalf("Messages: %v", err)
	}

	for i, want := range []string{"pageSize=3", "pageSize=2", "pageSize=1"} {
		if !strings.Contains(r.paths()[i], want) {
			t.Errorf("request %d asked for %q, want it to contain %q", i, r.paths()[i], want)
		}
	}
}

// TestAListWithNoLimitWalksEveryPage.
//
// --limit 0 is the export case and has to mean all of it. The page size is the
// documented maximum, because at that point round trips are the cost.
func TestAListWithNoLimitWalksEveryPage(t *testing.T) {
	r := newReader(t, pagesOf(t, 4))

	got, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{
		Space: "spaces/AAA",
		Limit: 0,
	}))
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("got %d messages, want 4", len(got))
	}
	if !strings.Contains(r.paths()[0], fmt.Sprintf("pageSize=%d", maxPageSize)) {
		t.Errorf("an unlimited list asked for %q, want the maximum page size", r.paths()[0])
	}
}

// TestTheFirstItemArrivesBeforeTheLastPageIsFetched.
//
// This is the streaming claim from the card, and it is the reason these methods
// return an iterator rather than a slice. Asserted by counting requests at the
// moment the first item is in hand: if the whole walk had to finish first, the
// count would already be four.
func TestTheFirstItemArrivesBeforeTheLastPageIsFetched(t *testing.T) {
	r := newReader(t, pagesOf(t, 4))

	seen := 0
	for _, err := range r.client.Messages(context.Background(), ListMessagesRequest{Space: "spaces/AAA"}) {
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		seen++
		if seen == 1 && r.count() != 1 {
			t.Fatalf("the first message arrived after %d requests, so the list is not streaming", r.count())
		}
	}
	if seen != 4 {
		t.Errorf("saw %d messages, want 4", seen)
	}
}

// TestBreakingOutOfARangeStopsFetching.
//
// The other half of streaming. A caller that stops reading has to stop the
// walk, or `tail -n 1` would quietly download a space's history.
func TestBreakingOutOfARangeStopsFetching(t *testing.T) {
	r := newReader(t, pagesOf(t, 10))

	for range r.client.Messages(context.Background(), ListMessagesRequest{Space: "spaces/AAA"}) {
		break
	}
	if r.count() != 1 {
		t.Errorf("made %d requests after the caller broke out of the range, want 1", r.count())
	}
}

// TestAFailureMidwayReachesTheCallerThatIsConsumingThatPage.
//
// The error is the second half of the yielded pair rather than a return value,
// so a failure on page two arrives in order, after page one's items. A caller
// cannot mistake a partial answer for a complete one without ignoring it
// explicitly.
func TestAFailureMidwayReachesTheCallerThatIsConsumingThatPage(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("pageToken") == "" {
			_, _ = fmt.Fprint(w, `{"nextPageToken": "page-1", "messages": [{"name": "spaces/AAA/messages/m0"}]}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error": {"code": 500, "status": "INTERNAL", "message": "boom"}}`)
	})

	got, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{Space: "spaces/AAA"}))
	if err == nil {
		t.Fatal("a failed page was reported as the end of the list")
	}
	if len(got) != 1 {
		t.Errorf("got %d messages before the failure, want 1", len(got))
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("the failure does not carry what the server said: %v", err)
	}
}

// TestANonAdvancingPageTokenStopsTheWalkAndSaysSo.
//
// A server that returns the same token forever would otherwise be an infinite
// loop: a command that never returns, spending quota, on a machine somebody has
// to go and find. It cannot happen against the real API, and it costs one
// comparison to make it impossible.
//
// Stopping was the whole of this test until m4-07, and stopping is only half
// the job. Every other way a walk ends early is either the caller's own doing,
// which is not truncation, or a request failure, which exits non-zero. This one
// ended short with no error at exit zero, which is a truncated result reported
// as complete: the invariant m4-07 owns, produced by this repository's own
// defence against a far end that will not paginate.
//
// The rows already fetched are still yielded. They were real, and a partial
// answer with a non-zero exit is honest where a partial answer with a zero exit
// is not.
func TestANonAdvancingPageTokenStopsTheWalkAndSaysSo(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"nextPageToken": "stuck", "messages": [{"name": "spaces/AAA/messages/m"}]}`)
	})

	var got []Message
	var err error
	for message, e := range r.client.Messages(context.Background(), ListMessagesRequest{Space: "spaces/AAA"}) {
		if e != nil {
			err = e
			break
		}
		got = append(got, message)
	}

	if err == nil {
		t.Fatal("a walk that stopped early reported nothing, so it is indistinguishable from a complete one")
	}
	if !errors.Is(err, ErrTruncated) {
		t.Errorf("the failure is not ErrTruncated: %v", err)
	}
	if got := output.ExitCodeOf(err); got == output.ExitOK {
		t.Errorf("a truncated list exited %d, which a script reads as success", got)
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("the failure does not say the result is short:\n%v", err)
	}

	// Two pages: the first issues "stuck", the second is asked for with it and
	// returns it again, which is where the walk gives up. Both pages' rows are
	// kept.
	if len(got) != 2 {
		t.Errorf("got %d messages from a server stuck on one token, want 2", len(got))
	}
}

// TestMessagesDefaultsToNewestFirst.
//
// The decision recorded in m3-04. A caller who says nothing gets the latest
// messages, because the default limit has to cut from the recent end to be
// useful. The API's own default is the opposite, so leaving orderBy off would
// return the start of the space's history.
func TestMessagesDefaultsToNewestFirst(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"messages": []}`)
	})

	if _, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{Space: "spaces/AAA"})); err != nil {
		t.Fatalf("Messages: %v", err)
	}

	got := r.paths()[0]
	if !strings.Contains(got, "orderBy=createTime+DESC") {
		t.Errorf("request was %q, want it to order by createTime DESC", got)
	}
}

// TestAReadRefusesABadSpaceNameWithoutAskingTheAPI.
//
// The same rule as every other place a space reaches a request path, from the
// same function. Escaping is the second layer and never the only one, and a name
// that fails the pattern must not become a URL at all.
func TestAReadRefusesABadSpaceNameWithoutAskingTheAPI(t *testing.T) {
	for _, space := range []string{
		"",
		"AAA",
		"spaces/AAA/messages",
		"spaces/../../etc",
		"spaces/AAA?key=x",
		"https://elsewhere.invalid/v1/spaces/AAA",
	} {
		t.Run(space, func(t *testing.T) {
			r := newReader(t, func(http.ResponseWriter, *http.Request) {
				t.Error("a refused space name still reached the network")
			})

			if _, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{Space: space})); err == nil {
				t.Errorf("%q was accepted as a space name", space)
			}
			if r.count() != 0 {
				t.Errorf("made %d requests for a name that should never have been sent", r.count())
			}
		})
	}
}

// TestAMessageNameIsCheckedAndAdmitsTheShapeTheAPIActuallyReturns.
//
// The dot is the case worth having. A generated message name reads as
// spaces/AAA/messages/nMs6.nMs6, so a pattern copied from the space rule would
// refuse every name the API hands back, and the failure would look like the API
// having changed rather than like this tool being wrong.
func TestAMessageNameIsCheckedAndAdmitsTheShapeTheAPIActuallyReturns(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"spaces/AAA/messages/nMs6.nMs6", true},
		{"spaces/AAA/messages/client-0123abcd", true},
		{"spaces/AAA/messages/BBB", true},
		{"", false},
		{"spaces/AAA", false},
		{"spaces/AAA/messages/", false},
		{"spaces/AAA/messages/BBB/reactions/CCC", false},
		{"spaces/AAA/messages/../../../etc", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckMessageName(tc.name)
			if tc.ok && err != nil {
				t.Errorf("CheckMessageName(%q) = %v, want it accepted", tc.name, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("CheckMessageName(%q) was accepted", tc.name)
			}
		})
	}
}

// TestAPageTokenChosenByTheServerCannotChangeTheRequest.
//
// A page token is the one value in this file that comes from the far end and
// then goes back into a request we build. Everything else is either from the
// operator or from this repository. So it is the obvious place to try to inject:
// a token of "x&key=stolen" or "../../../v1/spaces" would, if it were pasted
// into a URL rather than set as a value, add a parameter or move the path.
//
// It cannot, because it is set through url.Values and escaped on encode, and
// because the path is fixed before the query is built. Asserted rather than
// reasoned about, since the reasoning depends on an encoder somebody could
// replace with string concatenation on a tired afternoon.
func TestAPageTokenChosenByTheServerCannotChangeTheRequest(t *testing.T) {
	hostile := "x&key=stolen&pageSize=9999#/../../../etc"

	first := true
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		if first {
			first = false
			_, _ = fmt.Fprintf(w, `{"nextPageToken": %q, "messages": [{"name": "spaces/AAA/messages/m"}]}`, hostile)
			return
		}
		_, _ = fmt.Fprint(w, `{"messages": [{"name": "spaces/AAA/messages/n"}]}`)
	})

	if _, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{Space: "spaces/AAA"})); err != nil {
		t.Fatalf("Messages: %v", err)
	}

	second := r.paths()[1]

	// The token arrives as one escaped value. The ampersands in it must not have
	// become parameter separators, so no second key and no second pageSize.
	if strings.Contains(second, "key=stolen") {
		t.Errorf("a page token added a query parameter: %q", second)
	}
	if strings.Count(second, "pageSize=") != 1 {
		t.Errorf("a page token added a second pageSize: %q", second)
	}
	if !strings.HasPrefix(second, "/v1/spaces/AAA/messages?") {
		t.Errorf("a page token moved the request path: %q", second)
	}
}

// TestAPageThatWillNotDecodeIsAFailureRatherThanAnEmptyList.
//
// An empty list and an unreadable page are the same length and mean opposite
// things. Reporting the second as the first is the silent-truncation failure
// wearing a different hat.
func TestAPageThatWillNotDecodeIsAFailureRatherThanAnEmptyList(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"messages": [ this is not JSON`)
	})

	if _, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{Space: "spaces/AAA"})); err == nil {
		t.Fatal("an unreadable page was reported as the end of the list")
	}
}

// TestSpacesAndMembersWalkTheirOwnEndpoints, so that a copied decoder reading
// the wrong field is caught rather than returning an empty list forever.
func TestSpacesAndMembersWalkTheirOwnEndpoints(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.Contains(req.URL.Path, "/members"):
			_, _ = fmt.Fprint(w, `{"memberships": [{"name": "spaces/AAA/members/m", "state": "JOINED",
				"member": {"name": "users/1", "displayName": "Ada", "type": "HUMAN"}}]}`)
		default:
			_, _ = fmt.Fprint(w, `{"spaces": [{"name": "spaces/AAA", "displayName": "Ops", "spaceType": "SPACE"}]}`)
		}
	})

	spaces, err := collect(r.client.Spaces(context.Background(), ListSpacesRequest{}))
	if err != nil {
		t.Fatalf("Spaces: %v", err)
	}
	if len(spaces) != 1 || spaces[0].DisplayName != "Ops" {
		t.Errorf("spaces = %+v", spaces)
	}

	members, err := collect(r.client.Members(context.Background(), ListMembersRequest{Space: "spaces/AAA"}))
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 1 || members[0].Member == nil || members[0].Member.DisplayName != "Ada" {
		t.Errorf("members = %+v", members)
	}
}

// TestGetReadsOneResourceAndChecksItsNameFirst.
func TestGetReadsOneResourceAndChecksItsNameFirst(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "/messages/") {
			_, _ = fmt.Fprint(w, `{"name": "spaces/AAA/messages/BBB", "text": "hello"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"name": "spaces/AAA", "displayName": "Ops"}`)
	})

	space, err := r.client.GetSpace(context.Background(), "spaces/AAA")
	if err != nil {
		t.Fatalf("GetSpace: %v", err)
	}
	if space.DisplayName != "Ops" {
		t.Errorf("space = %+v", space)
	}

	message, err := r.client.GetMessage(context.Background(), "spaces/AAA/messages/BBB")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if message.Text != "hello" {
		t.Errorf("message = %+v", message)
	}

	before := r.count()
	if _, err := r.client.GetSpace(context.Background(), "not-a-space"); err == nil {
		t.Error("a bad space name was accepted")
	}
	if r.count() != before {
		t.Error("a refused name still reached the network")
	}
}

// TestAnEmptyPageIsNotTheEndOfTheWalk.
//
// Observed against the live API on 2026-08-16, which is why it is a test rather
// than a hypothetical. Chat's messages.list in ascending order returns a page
// one item short of the pageSize asked for, so pageSize=1 comes back with no
// messages at all and a nextPageToken:
//
//	pageSize=1  no orderBy               messages=0  token=yes
//	pageSize=2  no orderBy               messages=1  token=yes
//	pageSize=1  orderBy=createTime DESC  messages=1  token=yes
//
// The likely cause is a membership or space-creation event at the oldest end
// that counts against the page and is filtered out of the response. Descending
// order never reaches it in a small page, which is why the default path does not
// see this and `--order oldest --limit 1` costs two requests instead of one.
//
// What matters is the rule: a page with no items and a token means keep going.
// A pager that stopped there would return nothing for `--limit 1 --order
// oldest`, and report it as the complete answer, which is m4-07's invariant
// exactly. It is also the obvious optimisation somebody would make while
// tidying, and the reason to write it down.
func TestAnEmptyPageIsNotTheEndOfTheWalk(t *testing.T) {
	var pages atomic.Int64
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		switch pages.Add(1) {
		case 1:
			// Short by one, exactly as the real API answered.
			_, _ = fmt.Fprint(w, `{"messages":[],"nextPageToken":"second"}`)
		case 2:
			_, _ = fmt.Fprint(w, `{"messages":[{"name":"spaces/AAA/messages/BBB","text":"found"}]}`)
		default:
			t.Errorf("the walk made %d requests", pages.Load())
		}
	})

	got, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{
		Space: "spaces/AAAATestSpace",
		Limit: 1,
	}))
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(got) != 1 || got[0].Text != "found" {
		t.Fatalf("an empty first page ended the walk: got %d messages, %+v", len(got), got)
	}
	if pages.Load() != 2 {
		t.Errorf("made %d requests, want 2", pages.Load())
	}
}

// TestEveryWayAListEndsIsEitherCompleteOrSaysItIsNot.
//
// The m4-07 enumeration, as a table, because this failure is silent by nature
// and the thing that makes it silent is a case nobody thought to check. A job
// that reads fifty messages as though they were the whole conversation makes
// its decisions on a subset and reports success, and nothing downstream can
// tell.
//
// The rule the table encodes: a walk that ended because the caller said so is
// complete, and a walk that ended for any other reason says it did.
func TestEveryWayAListEndsIsEitherCompleteOrSaysItIsNot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		limit     int
		handler   func(page int, w http.ResponseWriter)
		wantRows  int
		truncated bool
	}{
		{
			name:  "the last page had no token",
			limit: 0,
			handler: func(_ int, w http.ResponseWriter) {
				_, _ = fmt.Fprint(w, `{"messages":[{"name":"spaces/AAA/messages/a"}]}`)
			},
			wantRows: 1,
		},
		{
			// The caller asked for two and got two. Nothing was cut short: a
			// limit is an instruction, not an interruption, and marking it
			// truncated would make the flag meaningless by firing on the
			// commonest possible invocation.
			name:  "the limit was reached",
			limit: 2,
			handler: func(_ int, w http.ResponseWriter) {
				_, _ = fmt.Fprint(w, `{"messages":[{"name":"spaces/AAA/messages/a"},{"name":"spaces/AAA/messages/b"}],"nextPageToken":"more"}`)
			},
			wantRows: 2,
		},
		{
			name:  "an error on the second page",
			limit: 0,
			handler: func(page int, w http.ResponseWriter) {
				if page == 1 {
					_, _ = fmt.Fprint(w, `{"messages":[{"name":"spaces/AAA/messages/a"}],"nextPageToken":"two"}`)
					return
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"nope"}}`)
			},
			wantRows:  1,
			truncated: true,
		},
		{
			name:  "the server would not advance its token",
			limit: 0,
			handler: func(_ int, w http.ResponseWriter) {
				_, _ = fmt.Fprint(w, `{"messages":[{"name":"spaces/AAA/messages/a"}],"nextPageToken":"stuck"}`)
			},
			wantRows:  2,
			truncated: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var page int
			var mu sync.Mutex
			r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				page++
				n := page
				mu.Unlock()
				tc.handler(n, w)
			})

			var rows int
			var failure error
			for _, err := range r.client.Messages(context.Background(), ListMessagesRequest{
				Space: "spaces/AAAATestSpace",
				Limit: tc.limit,
			}) {
				if err != nil {
					failure = err
					break
				}
				rows++
			}

			if rows != tc.wantRows {
				t.Errorf("got %d rows, want %d", rows, tc.wantRows)
			}

			// The whole point: a caller checking the exit code alone must not be
			// able to mistake one for the other.
			exit := output.ExitOK
			if failure != nil {
				exit = output.ExitCodeOf(failure)
			}
			if tc.truncated && exit == output.ExitOK {
				t.Errorf("a truncated list exited %d, which a script reads as the whole answer", exit)
			}
			if !tc.truncated && failure != nil {
				t.Errorf("a complete list reported %v", failure)
			}
		})
	}
}

// TestACallerThatStopsRangingIsNotATruncation.
//
// The fifth way out, and the one that cannot be tested from the table above,
// because it is the consumer's decision rather than the producer's. Breaking
// out of a range is how `--limit` is implemented one layer up and how a caller
// who has seen enough stops, and neither is a failure.
func TestACallerThatStopsRangingIsNotATruncation(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"messages":[{"name":"spaces/AAA/messages/a"}],"nextPageToken":"more"}`)
	})

	for _, err := range r.client.Messages(context.Background(), ListMessagesRequest{Space: "spaces/AAAATestSpace"}) {
		if err != nil {
			t.Fatalf("the first page failed: %v", err)
		}
		break
	}

	// One request, and no second one chasing a token nobody wanted. A producer
	// that kept fetching after the caller left would spend quota on rows that
	// are thrown away.
	if r.count() != 1 {
		t.Errorf("made %d requests after the caller stopped, want 1", r.count())
	}
}
