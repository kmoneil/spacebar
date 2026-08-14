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

// Package transport is the seam between what was asked for and what this
// profile can actually do.
//
// Three auth models, and most users will be in the worst one. A locked-down
// Workspace organization blocks the full API entirely and leaves an incoming
// webhook: write-only, fixed to one space, posting as a bot. Everything here
// follows from taking that population seriously rather than treating them as a
// degraded case, and the first consequence is that a command which cannot do
// what was asked has to say so before it does anything, with an exit code that
// says which capability was missing and a message naming the fix.
//
// The capability check is on the path to the network rather than beside it.
// Only internal/chat may build a request and only a transport owns a client, so
// a transport that refuses before reaching its client cannot be bypassed by a
// command that forgot to ask. Require exists for the message rather than for
// the guarantee: a transport does not know which command was run, and "'tail'
// needs read access" is a better sentence than "this profile cannot read".
package transport

import (
	"context"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
)

// Capabilities is what a transport can do (SPEC.md §8).
//
// A plain struct of booleans rather than an interface somebody implements,
// because the answers are facts about a transport rather than behaviour: they
// are known before anything is built, which is the whole point of checking them
// before anything is built.
type Capabilities struct {
	CanSend       bool
	CanSendCards  bool
	CanRead       bool
	CanEdit       bool
	CanDelete     bool
	CanReact      bool
	CanThread     bool
	CanUpload     bool
	CanListSpaces bool
	CanResolveDM  bool
}

// Transport is one way of reaching Chat.
//
// Deliberately small. SPEC.md §8 sketches an interface with a method per
// endpoint, and writing that now would be a compile-time to-do list: eleven
// methods returning "not implemented" until the milestone that fills each one
// in. Milestone 2 sends, so this sends. Growing it a method at a time costs one
// small diff per milestone and never has a stub in it.
//
// Kind returns the typed transport rather than SPEC.md §8's Name() string. A
// caller comparing a string against "webhook" is a caller who can typo it, and
// the typed value already exists in internal/config, where the configuration
// that chose it is parsed.
type Transport interface {
	// Kind is which of the two this is.
	Kind() config.Transport

	// Profile is the name the user gave this profile. Carried because a
	// refusal has to name it: somebody running a script across four profiles
	// needs to know which one could not do the work (SPEC.md §8.2).
	Profile() string

	// Capabilities is what this transport can do. Fixed for the life of the
	// transport, and answerable without a network call.
	Capabilities() Capabilities

	// Send posts a message.
	Send(ctx context.Context, req chat.SendRequest) (*chat.Message, error)
}

// CapabilitiesFor is the matrix in SPEC.md §8.1, in one place.
//
// One function rather than a table each implementation copies, so that the
// matrix cannot disagree with itself. An unrecognized transport can do nothing:
// internal/config already refuses one on load, and if it ever arrives here the
// safe answer is to refuse everything rather than to guess a capability.
func CapabilitiesFor(kind config.Transport) Capabilities {
	switch kind {
	case config.TransportWebhook:
		return Capabilities{
			CanSend: true,

			// This is the row that will be read as a typo, so it gets the
			// reason. A card requires app authentication, and an incoming
			// webhook *is* an app: the webhook is the app, posting as a bot.
			// So the write-only transport has one capability the full one does
			// not, which is the opposite of how the rest of the matrix reads.
			CanSendCards: true,

			// Only by threadKey. A webhook cannot name an existing thread,
			// because naming one means having read the space first. Milestone 4
			// may have to split this into two capabilities when replying to a
			// thread by name arrives; today both transports can thread and the
			// distinction has nothing to gate.
			CanThread: true,
		}

	case config.TransportUserOAuth:
		return Capabilities{
			CanSend: true,

			// Not a typo either. A user-authenticated create is text-only:
			// cards need app authentication, and this transport is acting as
			// the person rather than as an app.
			CanSendCards: false,

			CanRead:       true,
			CanEdit:       true,
			CanDelete:     true,
			CanReact:      true,
			CanThread:     true,
			CanUpload:     true,
			CanListSpaces: true,
			CanResolveDM:  true,
		}
	}
	return Capabilities{}
}
