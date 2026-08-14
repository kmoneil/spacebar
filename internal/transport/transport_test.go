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

package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// TestTheCapabilityMatrix writes SPEC.md §8.1 out cell by cell.
//
// Every cell, both transports, stated rather than derived. The matrix is the
// contract that decides what fails at exit 5 and what reaches the network, and
// a test that computed it from the same function it is checking would agree
// with any answer.
func TestTheCapabilityMatrix(t *testing.T) {
	for _, tc := range []struct {
		capability Capability
		name       string
		webhook    bool
		useroauth  bool
	}{
		{CanSend, "send", true, true},

		// The row that reads as a typo, twice over. A card needs app
		// authentication, and a webhook is an app: it posts as a bot. A
		// user-authenticated send is the person talking, and is text-only. So
		// the write-only transport has a capability the full one lacks.
		{CanSendCards, "send cards", true, false},

		{CanRead, "read", false, true},
		{CanEdit, "edit", false, true},
		{CanDelete, "delete", false, true},
		{CanReact, "react", false, true},
		{CanThread, "thread", true, true},
		{CanUpload, "upload", false, true},
		{CanListSpaces, "list spaces", false, true},
		{CanResolveDM, "resolve a DM", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CapabilitiesFor(config.TransportWebhook).Has(tc.capability); got != tc.webhook {
				t.Errorf("webhook can %s = %v, want %v", tc.name, got, tc.webhook)
			}
			if got := CapabilitiesFor(config.TransportUserOAuth).Has(tc.capability); got != tc.useroauth {
				t.Errorf("useroauth can %s = %v, want %v", tc.name, got, tc.useroauth)
			}
		})
	}
}

// TestAnUnknownTransportCanDoNothing. internal/config refuses one on load, so
// this should be unreachable; the reason it is asserted is that the other
// reading, where anything unrecognized is permitted, is how a gate stops being
// one.
func TestAnUnknownTransportCanDoNothing(t *testing.T) {
	caps := CapabilitiesFor(config.Transport("something-else"))

	for want := range numCapabilities {
		if caps.Has(want) {
			t.Errorf("an unrecognized transport claims %s", describe(want))
		}
	}
}

// TestEveryCapabilityHasADescription. The table is indexed by the enum, so a
// capability added above numCapabilities without a row here would produce a
// refusal with a blank in the middle of the sentence.
func TestEveryCapabilityHasADescription(t *testing.T) {
	for want := range numCapabilities {
		entry := capabilityTable[want]
		if entry.need == "" {
			t.Errorf("capability %d has no description", want)
		}
		if entry.has == nil {
			t.Errorf("capability %d cannot be read out of a Capabilities", want)
		}
	}

	// A value off either end is not held, rather than panicking on a slice
	// index or reading a neighbour's field.
	full := CapabilitiesFor(config.TransportUserOAuth)
	for _, bad := range []Capability{-1, numCapabilities, numCapabilities + 10} {
		if full.Has(bad) {
			t.Errorf("capability %d was reported as held", bad)
		}
		if describe(bad) == "" {
			t.Errorf("capability %d has no description at all, so a message would have a gap in it", bad)
		}
	}
}

// TestARefusalNamesTheProfileAndTheFix, per SPEC.md §8.2. Somebody running a
// script across four profiles needs to know which one could not do the work,
// and what to do instead.
func TestARefusalNamesTheProfileAndTheFix(t *testing.T) {
	err := Require(stub{kind: config.TransportWebhook, profile: "alerts"}, "tail", CanRead)
	if err == nil {
		t.Fatal("a webhook profile was allowed to read")
	}

	message := err.Error()
	for _, want := range []string{
		"tail",                            // the command, which the transport cannot know.
		"read access",                     // what was missing.
		"alerts",                          // the profile.
		"write-only",                      // why, for somebody who was handed a URL.
		string(config.TransportUserOAuth), // the alternative.
		meta.AppName + " auth login",      // the command that sets one up.
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal is missing %q:\n%s", want, message)
		}
	}

	if got := output.ExitCodeOf(err); got != output.ExitUnsupported {
		t.Errorf("exit code = %d, want %d", got, output.ExitUnsupported)
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("the refusal is not ErrUnsupported: %v", err)
	}
}

// TestTheCardRefusalPointsTheOtherWay. Every other refusal sends somebody
// towards user OAuth. This one sends them towards a webhook, because it is the
// one capability the full transport does not have, and a message that told them
// to authorize harder would be advice that cannot work.
func TestTheCardRefusalPointsTheOtherWay(t *testing.T) {
	err := Require(stub{kind: config.TransportUserOAuth, profile: "work"}, "send", CanSendCards)
	if err == nil {
		t.Fatal("a user-OAuth profile was allowed to send a card")
	}

	message := err.Error()
	if !strings.Contains(message, string(config.TransportWebhook)) {
		t.Errorf("the refusal does not name the transport that can do it:\n%s", message)
	}
	if strings.Contains(message, meta.AppName+" auth login") {
		t.Errorf("the refusal tells an authorized user to authorize again:\n%s", message)
	}
	if !strings.Contains(message, "text-only") {
		t.Errorf("the refusal does not say why:\n%s", message)
	}
}

// TestTheFirstMissingCapabilityIsReported. A profile that cannot read also
// cannot edit, delete, or react. Listing four consequences of one fact reads as
// four problems.
func TestTheFirstMissingCapabilityIsReported(t *testing.T) {
	err := Require(stub{kind: config.TransportWebhook, profile: "alerts"}, "react", CanRead, CanReact)
	if err == nil {
		t.Fatal("a webhook profile was allowed to react")
	}
	if !strings.Contains(err.Error(), "read access") {
		t.Errorf("the refusal did not report the first missing capability:\n%v", err)
	}
	if strings.Contains(err.Error(), "react to a message") {
		t.Errorf("the refusal listed a second consequence of the same fact:\n%v", err)
	}
}

// TestACapableTransportPasses, and passes for several at once, because a
// command needing two capabilities is the case where an accidental early return
// would refuse something valid.
func TestACapableTransportPasses(t *testing.T) {
	full := stub{kind: config.TransportUserOAuth, profile: "work"}
	if err := Require(full, "tail", CanRead, CanListSpaces, CanThread); err != nil {
		t.Errorf("a user-OAuth profile was refused: %v", err)
	}

	// And nothing at all is a pass rather than a refusal: a command with no
	// capability requirement has none, not an empty unmet one.
	if err := Require(stub{kind: config.TransportWebhook, profile: "alerts"}, "version"); err != nil {
		t.Errorf("a command needing nothing was refused: %v", err)
	}
}

// TestARefusalMakesNoNetworkCall is the assertion this card exists for.
//
// The error is not the point. A refusal that arrives after the POST carries the
// same exit code and the same message as one that arrives before it, and the
// difference between them is a message somebody's colleagues can see. So this
// counts requests rather than reading what came back.
//
// The transport is wired to a real client pointed at a real server, and the
// last two assertions are what stop the test passing for the wrong reason: the
// capability this profile does have goes through, and the send it authorizes
// actually reaches the server. Without them a gate that refused everything, or
// a transport that dialled nothing, would both look like success.
func TestARefusalMakesNoNetworkCall(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	}))
	t.Cleanup(server.Close)

	client, err := chat.New(chat.Options{
		BaseURL:   server.URL + "/v1/spaces/AAAATestSpace/messages?key=notARealKeyValue",
		Transport: config.TransportWebhook,
		Profile:   "alerts",
		HTTP:      server.Client(),
	})
	if err != nil {
		t.Fatalf("chat.New: %v", err)
	}
	dialing := stub{kind: config.TransportWebhook, profile: "alerts", client: client}

	for _, want := range []Capability{CanRead, CanEdit, CanDelete, CanReact, CanUpload, CanListSpaces, CanResolveDM} {
		if err := Require(dialing, "some-command", want); err == nil {
			t.Errorf("a webhook profile was allowed %s", describe(want))
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("%d requests were made by refused commands, want 0", got)
	}

	if err := Require(dialing, "send", CanSend); err != nil {
		t.Fatalf("a webhook profile was refused a send: %v", err)
	}
	if _, err := dialing.Send(context.Background(), chat.SendRequest{Message: chat.Message{Text: "hello"}}); err != nil {
		t.Fatalf("the send this profile can do failed: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("the wired transport made %d requests when allowed through, want 1; "+
			"this test would otherwise pass with the gate removed", got)
	}
}

// stub is a Transport whose capabilities come from the matrix, standing in for
// the implementations that arrive in m2-08 and m3-04.
type stub struct {
	kind    config.Transport
	profile string
	client  *chat.Client
}

func (s stub) Kind() config.Transport     { return s.kind }
func (s stub) Profile() string            { return s.profile }
func (s stub) Capabilities() Capabilities { return CapabilitiesFor(s.kind) }

func (s stub) Send(ctx context.Context, req chat.SendRequest) (*chat.Message, error) {
	return s.client.SendMessage(ctx, req)
}

// TestARefusalForAnUnrecognizedTransportStillReadsAsASentence.
//
// internal/config refuses an unknown transport on load, so reaching this needs
// a configuration that was never read through it. The reason it is covered is
// that the fallback is on the path that produces a message, and a fallback that
// produces "and profile 'x' " with nothing after it is a bug somebody hits on
// their worst day.
func TestARefusalForAnUnrecognizedTransportStillReadsAsASentence(t *testing.T) {
	err := Require(stub{kind: config.Transport("carrier-pigeon"), profile: "odd"}, "send", CanSend)
	if err == nil {
		t.Fatal("an unrecognized transport was allowed to send")
	}

	message := err.Error()
	for _, want := range []string{"send", "odd", "does not recognize", string(config.TransportUserOAuth)} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal is missing %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, "  ") || strings.Contains(message, " \n") {
		t.Errorf("the refusal has a gap where a phrase should be:\n%q", message)
	}
}
