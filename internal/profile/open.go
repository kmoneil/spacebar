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

// Package profile turns a profile name into something that can talk to Chat.
//
// It exists because that is three packages' worth of work and none of them can
// do it. internal/config knows which profile is active and what its transport
// is; internal/auth turns the reference in the file into the secret behind it;
// internal/transport/webhook turns the secret into something that sends. The
// composition has to live above all three, and it cannot live in
// internal/transport itself, because the implementations import that for the
// capability matrix and a factory there would close the cycle.
//
// It is deliberately not in internal/cli. SPEC.md §4 makes that an
// architectural rule: internal/cli and internal/mcpsrv are both thin adapters
// over the same internal packages, and "which transport does this profile get"
// is a decision the MCP server would otherwise have to make a second time and
// make differently.
package profile

import (
	"context"
	"time"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/resolve"
	"github.com/kmoneil/spacebar/internal/transport"
	"github.com/kmoneil/spacebar/internal/transport/useroauth"
	"github.com/kmoneil/spacebar/internal/transport/webhook"
)

// Options are what the caller knows that this package cannot work out.
type Options struct {
	// Name is the --profile flag value. Empty means resolve it the usual way.
	Name string

	// Timeout bounds one attempt. Zero means the client's default.
	Timeout time.Duration

	// Log is nil unless --verbose.
	Log chat.Logger

	// DryRun stops every request before it is sent. Threaded through rather
	// than set by each transport, so that a transport added later cannot forget
	// it: the flag reaches the one place that would do the sending.
	DryRun bool
}

// Open is a transport for one profile, and the warnings that came with reading
// its credential.
//
// Transport is nil when there was an error. Name and Warnings are filled in as
// far as they were known, so that a failure can still say which profile it was
// and still report that a credential came off a disk in plain text.
//
// The warnings are returned rather than printed, because only internal/output
// writes to a stream. They matter here more than almost anywhere: the loudest
// of them says the credential was read from a plain-text file rather than from
// a keyring, and a caller that dropped it would be hiding the one thing about
// this machine somebody needs to know.
type Open struct {
	Transport transport.Transport
	Name      string
	Warnings  []string

	// Aliases is this profile's own map of name to spaces/XXXX, carried so that
	// resolution does not have to load the configuration a second time. Read
	// only: internal/resolve substitutes from it and checks what comes out,
	// because config.json is a file somebody may have been handed.
	Aliases map[string]string

	// Profiles is every configured profile name, carried for the same reason
	// and used for one thing: internal/resolve explains a target that named a
	// profile rather than answering it with "no space is called that". Nothing
	// resolves against it. See resolve.Options.Profiles for why not.
	Profiles []string
}

// For resolves the active profile and opens its transport.
//
// It never returns a nil Open, even when it returns an error.
//
// Every caller reads the warnings off it before deciding what to do with the
// error, because a credential read from a plain-text file is worth saying
// whether or not the send then worked. Returning nil on some paths and not on
// others makes that correct code panic, which is what it did.
func For(opts Options) (*Open, error) {
	cfg, err := config.Load()
	if err != nil {
		return &Open{}, err
	}

	name, profile, err := cfg.Active(opts.Name)
	if err != nil {
		return &Open{}, err
	}

	store, err := auth.New()
	if err != nil {
		return &Open{Name: name, Profiles: cfg.Names()}, err
	}

	built, err := open(name, profile, store, opts)
	if err != nil {
		return &Open{Name: name, Warnings: store.Warnings(), Profiles: cfg.Names()}, err
	}
	return &Open{
		Transport: built,
		Name:      name,
		Warnings:  store.Warnings(),
		Aliases:   profile.Aliases,
		Profiles:  cfg.Names(),
	}, nil
}

func open(name string, profile config.Profile, store *auth.Store, opts Options) (transport.Transport, error) {
	switch profile.Transport {
	case config.TransportWebhook:
		return openWebhook(name, profile, store, opts)

	case config.TransportUserOAuth:
		return openUserOAuth(name, profile, store, opts)
	}

	return nil, output.Errorf("CONFIG", output.ExitUsage,
		"profile %q has transport %q, which is not one this tool has.", name, profile.Transport)
}

func openWebhook(name string, profile config.Profile, store *auth.Store, opts Options) (transport.Transport, error) {
	if profile.WebhookURLRef == "" {
		return nil, output.Errorf("CREDENTIAL_MISSING", output.ExitAuthRequired,
			"profile %q is a webhook and has no webhook URL.\nGive it one with: %s profile set-webhook %s",
			name, meta.AppName, name)
	}

	url, err := store.Get(profile.WebhookURLRef)
	if err != nil {
		return nil, err
	}

	return webhook.New(webhook.Options{
		Profile: name,
		URL:     url,
		Timeout: opts.Timeout,
		Log:     opts.Log,
		DryRun:  opts.DryRun,
	})
}

// openUserOAuth assembles the transport that acts as the authorized user.
//
// This is the composition the package exists for, and it is three packages
// deep. internal/config says the profile is a user-OAuth one and which OAuth
// client it uses; internal/auth turns the reference into the stored token and
// wraps it in a source that refreshes and writes rotations back;
// internal/chat supplies the HTTP client the refresh goes out through, because
// it is the only package permitted to name net/http. None of the three can do
// it alone and none of them should try.
//
// The token is loaded here rather than lazily inside the transport so that an
// unauthorized profile fails before a command starts work. The alternative is a
// command that prints a header, opens a stream, and then discovers there is no
// credential.
func openUserOAuth(name string, profile config.Profile, store *auth.Store, opts Options) (transport.Transport, error) {
	token, err := store.LoadToken(name)
	if err != nil {
		return nil, err
	}

	clientID, clientSecret, err := store.ClientCredentials(profile)
	if err != nil {
		return nil, err
	}
	if clientID == "" {
		// A build from source has no linked client, by design: an OAuth client
		// committed to an open repository is a client every fork shares. So this
		// is the ordinary state for anybody who cloned this, not an edge case,
		// and it names the command that fixes it.
		return nil, output.Errorf("NO_CLIENT", output.ExitAuthRequired,
			"profile %q has no OAuth client, so there is nothing to authorize against.\n"+
				"Create one and store it with: %s auth setup --profile %s < client_secret.json\n"+
				"Run '%s auth setup' with nothing on stdin to see how to create it.",
			name, meta.AppName, name, meta.AppName)
	}

	// The context here bounds the refresh that x/oauth2 may do on the first
	// request, and it is deliberately context.Background rather than a request
	// context: the source outlives any one call and is reused for every request
	// this command makes.
	source := auth.NewSource(context.Background(), store, name, token,
		clientID, clientSecret, chat.TokenHTTPClient(opts.Timeout))

	built, err := useroauth.New(useroauth.Options{
		Profile: name,
		Auth:    source,
		Timeout: opts.Timeout,
		Log:     opts.Log,
		DryRun:  opts.DryRun,

		// From the stored token rather than from auth.DefaultScopes, which is
		// what this build would ask for today and says nothing about what the
		// person consented to when this token was issued. A binary that grew a
		// scope would otherwise believe every existing token had it.
		Scopes: token.Scopes,
	})
	if err != nil {
		return nil, err
	}

	// The seven-day warning rides out with the credential warnings, through the
	// same channel, because both are things about this machine's authorization
	// that the caller has to be told exactly once.
	store.AddWarnings(source.Warnings())
	return built, nil
}

// Resolve turns what somebody typed into a space resource name.
//
// On Open rather than in an adapter, because both adapters need it and the one
// that had it was internal/cli. The four steps, the matching, and the refusals
// live in internal/resolve; what is here is the wiring that knows a profile has
// a name, an alias map, and a transport that may or may not be able to read.
//
// The cache is per profile. Two profiles authorized as different accounts reach
// different spaces, and one shared file would let one account's space list
// answer the other's lookup.
func (o *Open) Resolve(ctx context.Context, target string, refresh bool) (string, error) {
	reader, ok := o.Transport.(resolve.Reader)
	if !ok {
		// Unreachable while every transport implements the full interface, and
		// asserted rather than assumed because that is a claim about today's
		// interface and not about tomorrow's.
		return target, nil
	}

	return resolve.Resolve(ctx, reader, target, resolve.Options{
		Aliases:  o.Aliases,
		Cache:    resolve.NewCache(o.Name),
		Refresh:  refresh,
		Profiles: o.Profiles,
	})
}
