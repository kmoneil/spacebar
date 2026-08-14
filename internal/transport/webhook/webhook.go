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

// Package webhook is the transport most users of this tool will actually have.
//
// An incoming webhook is what a locked-down Workspace organization leaves
// somebody with: write-only, fixed to one space, posting as a bot, and needing
// no OAuth, no administrator approval, and no Cloud project. The whole project
// takes that population seriously rather than treating them as a degraded case,
// and this package is where that stops being a stated intention.
//
// The URL is the entire configuration and the entire credential. It carries key
// and token query parameters that are the whole of the authentication for one
// space, so it is held here and handed to internal/chat, which is the only
// package that may build a request out of it and the only one that redacts.
// Nothing in this package prints it, and the space it names is derived once at
// construction so that no later code has to go looking in it.
package webhook

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
)

// Options configure a webhook transport.
type Options struct {
	// Profile is the name this profile has in the configuration, for a message
	// that has to say which one could not do the work.
	Profile string

	// URL is the credential. It never leaves this struct except into
	// internal/chat.
	URL string

	// Timeout bounds one attempt. Zero means the client's default.
	Timeout time.Duration

	// Log is nil unless --verbose.
	Log chat.Logger
}

// Compile-time proof that this satisfies the interface. Milestone 3 and
// Milestone 4 each add a method to Transport, and this is what turns "the
// webhook implementation was not updated" into a build failure rather than
// something found when a command reaches for a method that is not there.
var _ transport.Transport = (*Transport)(nil)

// Transport posts to one space through one incoming webhook.
type Transport struct {
	profile string
	space   string
	client  *chat.Client
}

// New builds the transport for one webhook profile.
//
// The URL is validated again here even though the command that stored it
// validated it first. The two checks guard different things: that one catches a
// truncated paste at the moment somebody can still fix it, and this one catches
// a credential that reached the keyring by some other route, which is every
// route once a keyring is a thing other programs can write to.
func New(opts Options) (*Transport, error) {
	if err := auth.CheckWebhookURL(opts.URL); err != nil {
		return nil, err
	}

	space, err := spaceFromURL(opts.URL)
	if err != nil {
		return nil, err
	}

	client, err := chat.New(chat.Options{
		// The whole URL is the base. A webhook URL is already the messages
		// endpoint for exactly one space, so there is no path to join onto it
		// and nowhere else a request could go.
		BaseURL:   opts.URL,
		Transport: config.TransportWebhook,
		Profile:   opts.Profile,
		Timeout:   opts.Timeout,
		Log:       opts.Log,
	})
	if err != nil {
		return nil, err
	}

	return &Transport{profile: opts.Profile, space: space, client: client}, nil
}

// Kind is which transport this is.
func (t *Transport) Kind() config.Transport { return config.TransportWebhook }

// Profile is the name this profile has in the configuration.
func (t *Transport) Profile() string { return t.profile }

// Space is where this webhook posts, and the only place it can.
//
// Derived from the URL rather than configured separately, because they cannot
// disagree: the URL is what the request goes to. Safe to print, and printed:
// internal/auth's redaction deliberately keeps the path of a webhook URL,
// because the space is the part somebody checking a dry run is looking for.
func (t *Transport) Space() string { return t.space }

// Capabilities is the webhook row of SPEC.md §8.1, from the one place that
// holds the matrix.
func (t *Transport) Capabilities() transport.Capabilities {
	return transport.CapabilitiesFor(config.TransportWebhook)
}

// Send posts a message.
//
// A space that is not this webhook's own is a usage failure rather than a
// silent send to the right place. The alternative readings are both worse:
// sending anyway means somebody who typed the wrong target watched their
// message arrive somewhere, in a space full of people, with a success exit code
// saying it went where they asked.
func (t *Transport) Send(ctx context.Context, req chat.SendRequest) (*chat.Message, error) {
	if err := t.checkSpace(req.Space); err != nil {
		return nil, err
	}

	// Cleared because the base URL already names it. Left set, it would be
	// appended to a URL that ends in /messages.
	req.Space = ""
	return t.client.SendMessage(ctx, req)
}

func (t *Transport) checkSpace(space string) error {
	if space == "" || space == t.space {
		return nil
	}
	if err := chat.CheckSpaceName(space); err != nil {
		return err
	}
	return output.Usagef(
		"profile %q is a webhook for %s, and it cannot post to %s.\n"+
			"A webhook is issued for one space and is the only thing that authenticates the request, so "+
			"there is no version of this that reaches another one.\n"+
			"Use a profile whose webhook is for %s, or leave the target off to post to %s.",
		t.profile, t.space, space, space, t.space)
}

// spaceFromURL reads the space out of the webhook URL's path.
//
// The path of an incoming webhook URL is /v1/spaces/SPACE/messages, confirmed
// against the current webhook guide rather than assumed. The space is taken
// from it rather than stored beside it because two copies of the same fact can
// disagree and only one of them is what the request will actually reach.
func spaceFromURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		// Unreachable in practice: CheckWebhookURL has already parsed this.
		// Kept because "unreachable" is a claim about today's call order.
		return "", output.Usagef("this profile's webhook URL will not parse.")
	}

	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	for i, segment := range segments {
		if segment != "spaces" || i+1 >= len(segments) {
			continue
		}
		space := "spaces/" + segments[i+1]
		if err := chat.CheckSpaceName(space); err != nil {
			return "", err
		}
		return space, nil
	}

	return "", output.Usagef("this profile's webhook URL has no /spaces/ segment, so there is no way to tell which space it posts to.\n" +
		"It may have been truncated when it was copied.")
}
