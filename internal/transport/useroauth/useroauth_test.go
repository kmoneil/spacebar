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

package useroauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
)

// bearer stands in for the token source internal/auth builds.
type bearer struct{}

func (bearer) Authorization(context.Context) (string, error) { return "Bearer test-access", nil }
func (bearer) Refresh(context.Context) (bool, error)         { return false, nil }

// wired builds a transport pointed at a test server, granted the scopes an
// ordinary `auth login` asks for.
//
// The client is constructed here and assigned directly rather than through New,
// because New hard-codes chat.BaseURL and that is the correct production
// behaviour. A settable base URL on Options would be a knob where the bearer
// token goes, offered to every caller so that one test could use it. This is a
// white-box test in the same package instead, which reaches the seam without
// creating one.
func wired(t *testing.T, handler http.HandlerFunc) (*Transport, *atomic.Int64) {
	t.Helper()
	return wiredWithScopes(t, auth.DefaultScopes, handler)
}

// wiredWithScopes is wired for a test that cares which scopes were granted.
func wiredWithScopes(t *testing.T, scopes []string, handler http.HandlerFunc) (*Transport, *atomic.Int64) {
	t.Helper()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	client, err := chat.New(chat.Options{
		BaseURL:   server.URL + "/v1",
		Transport: config.TransportUserOAuth,
		Profile:   "work",
		Auth:      bearer{},
		HTTP:      server.Client(),
	})
	if err != nil {
		t.Fatalf("chat.New: %v", err)
	}

	return &Transport{
		profile: "work",
		client:  client,
		caps:    transport.ScopedCapabilities(config.TransportUserOAuth, scopes),
	}, &requests
}

// TestItCanDoEverythingTheWebhookCannot, which is the row of SPEC.md §8.1 this
// transport exists to fill in.
//
// Against the scopes an ordinary authorization asks for, rather than against the
// bare matrix, because the matrix is a ceiling and what a profile can do is the
// ceiling narrowed by the grant. CanResolveDM is absent from the list for that
// reason: the matrix claims it, chat.spaces is what grants it, and the default
// set deliberately does not ask for it until a command needs one.
func TestItCanDoEverythingTheWebhookCannot(t *testing.T) {
	caps := transport.ScopedCapabilities(config.TransportUserOAuth, auth.DefaultScopes)

	for _, want := range []transport.Capability{
		transport.CanSend,
		transport.CanRead,
		transport.CanEdit,
		transport.CanDelete,
		transport.CanReact,
		transport.CanThread,
		transport.CanUpload,
		transport.CanListSpaces,
		transport.CanReadMembers,
	} {
		if !caps.Has(want) {
			t.Errorf("a user-OAuth profile cannot %v, and it should be able to", want)
		}
	}

	// The row everybody reads as a typo. A card needs app authentication and
	// this transport is a person, so the write-only transport has one capability
	// this one lacks.
	if caps.Has(transport.CanSendCards) {
		t.Error("a user-authenticated send is text-only, so cards must not be a capability here")
	}
}

// TestACardIsRefusedBeforeTheNetworkRatherThanByTheAPI.
//
// The API would answer this with an error about the request body, which reads
// like a malformed card rather than like a transport that cannot send one. Exit
// 5 naming the capability sends somebody to the profile that can.
func TestACardIsRefusedBeforeTheNetworkRatherThanByTheAPI(t *testing.T) {
	tr, requests := wired(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a card reached the network on a transport that cannot send one")
	})

	_, err := tr.Send(context.Background(), chat.SendRequest{
		Space: "spaces/AAA",
		Message: chat.Message{
			Text:    "deploy done",
			CardsV2: []json.RawMessage{json.RawMessage(`{"cardId":"c","card":{}}`)},
		},
	})
	if err == nil {
		t.Fatal("a card was accepted")
	}
	if got := output.ExitCodeOf(err); got != output.ExitUnsupported {
		t.Errorf("exit code = %d, want %d: %v", got, output.ExitUnsupported, err)
	}
	if requests.Load() != 0 {
		t.Errorf("made %d requests before refusing", requests.Load())
	}

	// The message has to name the fix, and for this one the fix is the other
	// transport, which is the counter-intuitive direction.
	if !strings.Contains(err.Error(), string(config.TransportWebhook)) {
		t.Errorf("the refusal does not point at the transport that can send a card:\n%v", err)
	}
}

// TestASendWithNoSpaceIsRefusedRatherThanGuessed.
//
// A webhook knows its one space, so `send 'text'` is complete on one. This
// transport reaches every space the account can, and picking one would mean a
// message arriving in front of people who were not meant to see it.
func TestASendWithNoSpaceIsRefusedRatherThanGuessed(t *testing.T) {
	tr, requests := wired(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a send with no target reached the network")
	})

	if _, err := tr.Send(context.Background(), chat.SendRequest{
		Message: chat.Message{Text: "deploy done"},
	}); err == nil {
		t.Fatal("a send with no space was accepted")
	}
	if requests.Load() != 0 {
		t.Errorf("made %d requests before refusing", requests.Load())
	}
}

// TestItIsNotFixedToOneSpace.
//
// The optional interface a webhook satisfies and this one must not. Answering
// it would mean inventing a space, and `send` uses the answer to decide whether
// a single argument is a message or a target.
func TestItIsNotFixedToOneSpace(t *testing.T) {
	tr, _ := wired(t, func(http.ResponseWriter, *http.Request) {})

	if space, fixed := transport.SpaceOf(tr); fixed {
		t.Errorf("SpaceOf = %q, %v, but this transport reaches every space the account can", space, fixed)
	}
}

// TestTheReadPathsReachTheAPIAndComeBackWithData.
//
// A delegation test, and it is worth having for exactly that reason: five
// methods that forward to five others is where a copied line sends `members` to
// the messages endpoint, which no type checker catches.
func TestTheReadPathsReachTheAPIAndComeBackWithData(t *testing.T) {
	tr, _ := wired(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/members"):
			_, _ = fmt.Fprint(w, `{"memberships":[{"name":"spaces/AAA/members/m","state":"JOINED"}]}`)
		case strings.Contains(r.URL.Path, "/messages/"):
			_, _ = fmt.Fprint(w, `{"name":"spaces/AAA/messages/BBB","text":"one message"}`)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = fmt.Fprint(w, `{"messages":[{"name":"spaces/AAA/messages/BBB","text":"listed"}]}`)
		case strings.HasSuffix(r.URL.Path, "/spaces"):
			_, _ = fmt.Fprint(w, `{"spaces":[{"name":"spaces/AAA","displayName":"Ops"}]}`)
		default:
			_, _ = fmt.Fprint(w, `{"name":"spaces/AAA","displayName":"Ops"}`)
		}
	})

	ctx := context.Background()

	for space, err := range tr.Spaces(ctx, chat.ListSpacesRequest{Limit: 1}) {
		if err != nil {
			t.Fatalf("Spaces: %v", err)
		}
		if space.DisplayName != "Ops" {
			t.Errorf("space = %+v", space)
		}
	}

	for member, err := range tr.Members(ctx, chat.ListMembersRequest{Space: "spaces/AAA", Limit: 1}) {
		if err != nil {
			t.Fatalf("Members: %v", err)
		}
		if member.State != "JOINED" {
			t.Errorf("member = %+v", member)
		}
	}

	for message, err := range tr.Messages(ctx, chat.ListMessagesRequest{Space: "spaces/AAA", Limit: 1}) {
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if message.Text != "listed" {
			t.Errorf("message = %+v", message)
		}
	}

	space, err := tr.GetSpace(ctx, "spaces/AAA")
	if err != nil {
		t.Fatalf("GetSpace: %v", err)
	}
	if space.DisplayName != "Ops" {
		t.Errorf("space = %+v", space)
	}

	message, err := tr.GetMessage(ctx, "spaces/AAA/messages/BBB")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if message.Text != "one message" {
		t.Errorf("message = %+v", message)
	}
}

// TestADryRunStopsAReadBeforeItLeavesTheProcess.
//
// --dry-run is set on the client rather than at each command, so it stops a GET
// as well as a POST, and that is the behaviour rather than an accident of where
// the check sits. A read spends a request on a per-space quota shared with every
// other app in the space, and somebody checking what a command resolved to
// should not have to spend one to find out.
func TestADryRunStopsAReadBeforeItLeavesTheProcess(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
		t.Error("a dry run reached the network")
	}))
	t.Cleanup(server.Close)

	client, err := chat.New(chat.Options{
		BaseURL:   server.URL + "/v1",
		Transport: config.TransportUserOAuth,
		Profile:   "work",
		Auth:      bearer{},
		HTTP:      server.Client(),
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("chat.New: %v", err)
	}
	tr := &Transport{
		profile: "work",
		client:  client,
		caps:    transport.ScopedCapabilities(config.TransportUserOAuth, auth.DefaultScopes),
	}

	// Every read path, because the stop is in the client and a path that reached
	// its own client differently would not get it.
	for name, err := range map[string]error{
		"spaces":   firstErr(tr.Spaces(context.Background(), chat.ListSpacesRequest{})),
		"members":  firstErr(tr.Members(context.Background(), chat.ListMembersRequest{Space: "spaces/AAA"})),
		"messages": firstErr(tr.Messages(context.Background(), chat.ListMessagesRequest{Space: "spaces/AAA"})),
	} {
		if _, ok := errors.AsType[*chat.DryRun](err); !ok {
			t.Errorf("%s under --dry-run returned %v, want the request it would have made", name, err)
		}
	}

	if _, err := tr.GetSpace(context.Background(), "spaces/AAA"); !isDryRun(err) {
		t.Errorf("GetSpace under --dry-run returned %v", err)
	}
	if _, err := tr.GetMessage(context.Background(), "spaces/AAA/messages/BBB"); !isDryRun(err) {
		t.Errorf("GetMessage under --dry-run returned %v", err)
	}

	if requests.Load() != 0 {
		t.Errorf("a dry run made %d requests", requests.Load())
	}
}

// firstErr drains an iterator for the failure it yields.
func firstErr[T any](items func(func(T, error) bool)) error {
	var out error
	for _, err := range items {
		out = err
		break
	}
	return out
}

func isDryRun(err error) bool {
	_, ok := errors.AsType[*chat.DryRun](err)
	return ok
}

// TestBuildingOneWithNoCredentialFailsLoudly.
//
// A nil Authorizer would build a client that sends unauthenticated requests and
// collects a 401 per command, which reads as an expired authorization and sends
// somebody to re-run auth login. It is a bug in this program rather than a
// problem with the profile, and it says so.
func TestBuildingOneWithNoCredentialFailsLoudly(t *testing.T) {
	_, err := New(Options{Profile: "work"})
	if err == nil {
		t.Fatal("a transport was built with no credential")
	}
	if !strings.Contains(err.Error(), "bug in this tool") {
		t.Errorf("the failure blames the profile rather than the program:\n%v", err)
	}
}

// TestItNamesItselfAndItsProfile, which every refusal quotes.
func TestItNamesItselfAndItsProfile(t *testing.T) {
	tr, _ := wired(t, func(http.ResponseWriter, *http.Request) {})

	if tr.Kind() != config.TransportUserOAuth {
		t.Errorf("kind = %q", tr.Kind())
	}
	if tr.Profile() != "work" {
		t.Errorf("profile = %q", tr.Profile())
	}
}

// TestAScopeTheTokenLacksIsRefusedBeforeTheNetwork.
//
// The bug this test exists for shipped: `spaces members` was gated on CanRead,
// chat.memberships.readonly was in no grant this tool issued, and the command
// answered 403 PERMISSION_DENIED on every profile it could create. A 403 says
// the account is not allowed, which sends somebody to an administrator to fix
// something `auth login` fixes.
//
// Requests are counted rather than the error read, because a refusal that
// arrives after the GET carries the same code as one that arrives before it and
// only one of them is worth having.
func TestAScopeTheTokenLacksIsRefusedBeforeTheNetwork(t *testing.T) {
	// Everything the default set asks for except the membership scope, which is
	// the state of every token issued before that scope was added.
	granted := []string{auth.ScopeMessages, auth.ScopeSpacesRO}

	tr, requests := wiredWithScopes(t, granted, func(http.ResponseWriter, *http.Request) {
		t.Error("a membership list reached the network on a token without the scope for it")
	})

	var err error
	for _, e := range tr.Members(context.Background(), chat.ListMembersRequest{
		Space: "spaces/AAAAAAA",
		Limit: 10,
	}) {
		err = e
		break
	}

	if err == nil {
		t.Fatal("a membership list succeeded on a token that was never granted the scope")
	}
	if requests.Load() != 0 {
		t.Errorf("requests = %d, and a refusal that made one is not a refusal", requests.Load())
	}
	if !errors.Is(err, transport.ErrUnsupported) {
		t.Errorf("the refusal is not an unsupported-capability error:\n%v", err)
	}

	var out *output.Error
	if !errors.As(err, &out) {
		t.Fatalf("the refusal carries no exit code:\n%v", err)
	}
	if out.Exit != output.ExitUnsupported {
		t.Errorf("exit = %d, want %d", out.Exit, output.ExitUnsupported)
	}

	// The message has to send somebody to the command that fixes it, and must
	// not blame the transport: this profile is already the one that can read.
	if !strings.Contains(out.Message, "auth login --profile work") {
		t.Errorf("the refusal does not name the command that widens the grant:\n%s", out.Message)
	}
	if strings.Contains(out.Message, "incoming webhook") {
		t.Errorf("the refusal blames the transport on a profile that has the right one:\n%s", out.Message)
	}
}

// TestReadingIsRefusedWithoutTheScopeThatGrantsIt, for the same reason, on the
// paths that return a value rather than an iterator.
func TestReadingIsRefusedWithoutTheScopeThatGrantsIt(t *testing.T) {
	tr, requests := wiredWithScopes(t, []string{auth.ScopeSpacesRO}, func(http.ResponseWriter, *http.Request) {
		t.Error("a read reached the network on a token granted no message scope")
	})

	if _, err := tr.GetMessage(context.Background(), "spaces/AAAAAAA/messages/BBB"); err == nil {
		t.Error("messages get succeeded without a message scope")
	}
	if _, err := tr.GetSpace(context.Background(), "spaces/AAAAAAA"); err == nil {
		t.Error("spaces get succeeded without a message scope")
	}
	if requests.Load() != 0 {
		t.Errorf("requests = %d, and a refusal that made one is not a refusal", requests.Load())
	}
}

// TestATokenRecordWithNoScopesGrantsNothing.
//
// A record written before scopes were stored has an empty list, and the safe
// reading of "we do not know what this was granted" is nothing rather than
// everything. The cost is one `auth login` on upgrade; the alternative is a
// binary that believes an old token holds a scope invented after it.
func TestATokenRecordWithNoScopesGrantsNothing(t *testing.T) {
	caps := transport.ScopedCapabilities(config.TransportUserOAuth, nil)

	for _, want := range []transport.Capability{
		transport.CanSend,
		transport.CanRead,
		transport.CanListSpaces,
		transport.CanReadMembers,
	} {
		if caps.Has(want) {
			t.Errorf("a token with no recorded scopes was credited with %v", want)
		}
	}
}
