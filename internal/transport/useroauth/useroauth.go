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

// Package useroauth is the transport that can read.
//
// It acts as the person who authorized it, with the narrow scopes they granted,
// against every space that account can reach. That is the whole difference from
// a webhook, which is issued for one space, cannot read, and posts as a bot.
//
// It is also the transport with the counter-intuitive gap. A user-authenticated
// create is text-only, because a card requires app authentication and this is
// not an app, so the write-only transport has one capability this one lacks.
// SPEC.md §8.1 states it and it gets read as a typo, so Send refuses a card here
// rather than letting the API answer for it.
//
// The credential is a bearer token that expires hourly and is refreshed
// underneath. Nothing in this package holds it: it arrives as a chat.Authorizer
// built by internal/profile, which is the only place that knows both how to read
// a token out of the keyring and how to write a rotated one back.
package useroauth

import (
	"context"
	"iter"
	"time"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
)

// Options configure a user-OAuth transport.
type Options struct {
	// Profile is the name this profile has in the configuration, for a message
	// that has to say which one could not do the work.
	Profile string

	// Auth supplies the bearer token and refreshes it. Required: a nil one would
	// build a client that sends unauthenticated requests and collects a 401 per
	// command.
	Auth chat.Authorizer

	// Scopes are what the stored token was actually granted, which is not the
	// same question as what this build asks for. A token issued before a scope
	// was added holds the old set until somebody authorizes again, and the
	// capabilities have to answer for the token in hand rather than for the
	// constant in this binary.
	//
	// An empty one grants nothing, which is the safe reading: a token record
	// written before scopes were recorded refuses at exit 5 naming `auth login`,
	// rather than being assumed to hold everything and collecting a 403.
	Scopes []string

	// Timeout bounds one attempt. Zero means the client's default.
	Timeout time.Duration

	// Log is nil unless --verbose.
	Log chat.Logger

	// DryRun stops the request before it is sent and returns what would have
	// gone instead.
	DryRun bool
}

// Compile-time proof that this satisfies the interface, for the reason the
// webhook has one: a method added to Transport in a later milestone should break
// the build here rather than at the call site that first wanted it.
var _ transport.Transport = (*Transport)(nil)

// Transport reaches Chat as the authorized user.
type Transport struct {
	profile string
	client  *chat.Client
	caps    transport.Capabilities
}

// New builds the transport for one authorized profile.
func New(opts Options) (*Transport, error) {
	if opts.Auth == nil {
		// Not a usage failure: nobody typed anything wrong. It is a composition
		// failure inside this program, and it is caught here because the symptom
		// otherwise is an authentication error that sends somebody to re-run
		// auth login, which would not fix it.
		return nil, output.Errorf("NO_CREDENTIAL", output.ExitError,
			"profile %q was opened without a credential, which is a bug in this tool rather than a problem with the profile.",
			opts.Profile)
	}

	client, err := chat.New(chat.Options{
		BaseURL:   chat.BaseURL,
		Transport: config.TransportUserOAuth,
		Profile:   opts.Profile,
		Timeout:   opts.Timeout,
		Auth:      opts.Auth,
		Log:       opts.Log,
		DryRun:    opts.DryRun,
	})
	if err != nil {
		return nil, err
	}

	return &Transport{
		profile: opts.Profile,
		client:  client,
		caps:    transport.ScopedCapabilities(config.TransportUserOAuth, opts.Scopes),
	}, nil
}

// Kind is which transport this is.
func (t *Transport) Kind() config.Transport { return config.TransportUserOAuth }

// Profile is the name this profile has in the configuration.
func (t *Transport) Profile() string { return t.profile }

// Capabilities is the useroauth row of SPEC.md §8.1, narrowed to the scopes
// this profile's token was granted.
//
// Computed once in New rather than here, because the interface promises an
// answer that is fixed for the life of the transport and answerable without a
// network call. A refresh does not widen a grant: x/oauth2 sends the refresh
// token and gets the same scopes back, so the set decided at consent is the set
// for as long as this token lives.
func (t *Transport) Capabilities() transport.Capabilities { return t.caps }

// Send posts a message as the authorized user.
//
// There is no fixed space to fall back on, unlike a webhook, so a send with no
// target is refused rather than guessed at. Guessing would mean picking a space
// out of the ones this account can reach, and the failure mode of picking wrong
// is a message arriving in front of people who were not meant to see it.
func (t *Transport) Send(ctx context.Context, req chat.SendRequest) (*chat.Message, error) {
	if len(req.Message.CardsV2) > 0 {
		return nil, transport.Unsupported(t, "send --card", transport.CanSendCards)
	}
	if !t.caps.Has(transport.CanSend) {
		return nil, transport.Unsupported(t, "send", transport.CanSend)
	}
	if err := chat.CheckSpaceName(req.Space); err != nil {
		return nil, err
	}
	return t.client.SendMessage(ctx, req)
}

// The read paths each check the grant before reaching the client, for the
// reason the webhook implements all of them as refusals: the check that matters
// is the one no caller can forget. transport.Require is on the command's path
// and produces the better message because it knows which command was run, but a
// command that forgot to call it must not become a request that leaves the
// process and comes back a 403.

// Spaces lists the spaces this profile can reach.
func (t *Transport) Spaces(ctx context.Context, req chat.ListSpacesRequest) iter.Seq2[chat.Space, error] {
	if !t.caps.Has(transport.CanListSpaces) {
		return transport.Refused[chat.Space](t, "spaces list", transport.CanListSpaces)
	}
	return t.client.Spaces(ctx, req)
}

// GetSpace reads one space.
func (t *Transport) GetSpace(ctx context.Context, space string) (*chat.Space, error) {
	if !t.caps.Has(transport.CanRead) {
		return nil, transport.Unsupported(t, "spaces get", transport.CanRead)
	}
	return t.client.GetSpace(ctx, space)
}

// Members lists who is in a space.
//
// Gated on its own capability rather than on CanRead. Chat scopes a membership
// list separately from a message body, and folding the two together is what let
// this command ship against a grant that could never satisfy it.
func (t *Transport) Members(ctx context.Context, req chat.ListMembersRequest) iter.Seq2[chat.Membership, error] {
	if !t.caps.Has(transport.CanReadMembers) {
		return transport.Refused[chat.Membership](t, "spaces members", transport.CanReadMembers)
	}
	return t.client.Members(ctx, req)
}

// Messages lists messages in a space.
func (t *Transport) Messages(ctx context.Context, req chat.ListMessagesRequest) iter.Seq2[chat.Message, error] {
	if !t.caps.Has(transport.CanRead) {
		return transport.Refused[chat.Message](t, "messages list", transport.CanRead)
	}
	return t.client.Messages(ctx, req)
}

// GetMessage reads one message by resource name.
func (t *Transport) GetMessage(ctx context.Context, message string) (*chat.Message, error) {
	if !t.caps.Has(transport.CanRead) {
		return nil, transport.Unsupported(t, "messages get", transport.CanRead)
	}
	return t.client.GetMessage(ctx, message)
}

// FindDirectMessage returns the direct message space shared with one user.
//
// Gated on CanResolveDM, which chat.spaces.readonly grants. It was gated on
// chat.spaces until the m4-01 recon probed the live endpoint, which would have
// refused this on every profile this tool creates for want of a scope the
// operation does not need.
func (t *Transport) FindDirectMessage(ctx context.Context, user string) (*chat.Space, error) {
	if !t.caps.Has(transport.CanResolveDM) {
		return nil, transport.Unsupported(t, "resolving a direct message", transport.CanResolveDM)
	}
	return t.client.FindDirectMessage(ctx, user)
}

// Tail follows a space. Gated on CanRead, which is what its polls are.
func (t *Transport) Tail(ctx context.Context, req chat.TailRequest) iter.Seq2[chat.Message, error] {
	if !t.caps.Has(transport.CanRead) {
		return transport.Refused[chat.Message](t, "tail", transport.CanRead)
	}
	return t.client.Tail(ctx, req)
}
