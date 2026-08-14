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
	"time"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
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
		return &Open{Name: name}, err
	}

	built, err := open(name, profile, store, opts)
	if err != nil {
		return &Open{Name: name, Warnings: store.Warnings()}, err
	}
	return &Open{Transport: built, Name: name, Warnings: store.Warnings()}, nil
}

func open(name string, profile config.Profile, store *auth.Store, opts Options) (transport.Transport, error) {
	switch profile.Transport {
	case config.TransportWebhook:
		return openWebhook(name, profile, store, opts)

	case config.TransportUserOAuth:
		// Named rather than falling through to "unknown transport", because
		// this one is configured, correct, and simply not built yet. Somebody
		// who set it up deserves to be told that rather than to be told their
		// configuration is wrong.
		return nil, output.Errorf("UNSUPPORTED", output.ExitUnsupported,
			"profile %q uses %s, which this build does not have yet.\n"+
				"Milestone 3 adds it. Until then, a profile with %s is the way to send.",
			name, config.TransportUserOAuth, config.TransportWebhook)
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
	})
}
