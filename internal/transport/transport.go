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
	"iter"

	"github.com/kmoneil/spacebar/internal/auth"
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

	// CanReadMembers is separate from CanRead because Chat scopes them
	// separately: reading messages is chat.messages.readonly and reading a
	// membership list is chat.memberships.readonly. Folding the two together is
	// what made `spaces members` answer 403 on every profile this tool could
	// create, since the capability said yes and the grant said no.
	CanReadMembers bool
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

	// The read paths. Every one of them returns a refusal on a transport whose
	// Capabilities say it cannot read, per SPEC.md §8, rather than being absent
	// from that implementation.
	//
	// Present on the interface rather than behind a second optional one like
	// Fixed, and the difference is deliberate. Fixed answers a question only one
	// transport has an answer to, so putting Space() here would force the other
	// to invent one. Reading is not like that: both transports have a real
	// answer, and for the webhook the answer is no. Making that a compile-time
	// obligation is what stops a third transport from quietly having no opinion.

	// Spaces lists the spaces this profile can reach.
	Spaces(ctx context.Context, req chat.ListSpacesRequest) iter.Seq2[chat.Space, error]

	// GetSpace reads one space.
	GetSpace(ctx context.Context, space string) (*chat.Space, error)

	// Members lists who is in a space.
	Members(ctx context.Context, req chat.ListMembersRequest) iter.Seq2[chat.Membership, error]

	// Messages lists messages in a space.
	Messages(ctx context.Context, req chat.ListMessagesRequest) iter.Seq2[chat.Message, error]

	// GetMessage reads one message by resource name.
	GetMessage(ctx context.Context, message string) (*chat.Message, error)

	// Watch follows what happens in a space: edits, deletions and reactions as
	// well as new messages. A read, and on the far side of the Chat app
	// requirement rather than of the read/write line, which is the one thing
	// about this endpoint worth remembering.
	Watch(ctx context.Context, req chat.WatchRequest) iter.Seq2[chat.SpaceEvent, error]

	// WatchMany follows several spaces at once, on one budgeted request rate.
	// The same capability as Watch, and a separate method rather than a slice
	// on WatchRequest because the rate rule and the dropping of a space that
	// goes away belong to it and to nothing else.
	WatchMany(ctx context.Context, req chat.WatchManyRequest) iter.Seq2[chat.SpaceEvent, error]

	// Upload sends an attachment's bytes and returns the handle a send attaches
	// by. Download fetches them back.
	Upload(ctx context.Context, req chat.UploadRequest) (*chat.AttachmentDataRef, error)
	Download(ctx context.Context, resourceName string) ([]byte, error)

	// The mutation paths. Each is gated on its own capability rather than on a
	// single "can write", because Chat scopes them separately and because a
	// profile that may post is not necessarily one that may delete.
	EditMessage(ctx context.Context, req chat.EditRequest) (*chat.Message, error)
	DeleteMessage(ctx context.Context, message string) error
	React(ctx context.Context, req chat.ReactRequest) (*chat.Reaction, error)

	// FindDirectMessage returns the direct message space shared with one user.
	FindDirectMessage(ctx context.Context, user string) (*chat.Space, error)

	// Tail follows a space, yielding each new message as it arrives. It ends
	// when the context is cancelled, which is how a caller stops it.
	Tail(ctx context.Context, req chat.TailRequest) iter.Seq2[chat.Message, error]
}

// CapabilitiesFor is the matrix in SPEC.md §8.1, in one place.
//
// One function rather than a table each implementation copies, so that the
// matrix cannot disagree with itself. An unrecognized transport can do nothing:
// internal/config already refuses one on load, and if it ever arrives here the
// safe answer is to refuse everything rather than to guess a capability.
//
// This is a ceiling and not an answer. It says what the transport kind could do
// if it were granted everything, which for a webhook is the whole story and for
// user OAuth is half of it: the other half is which scopes the person actually
// consented to. Ask ScopedCapabilities for what a particular profile can do.
// The distinction is what a live check found: this row claimed CanReadMembers
// while no token this tool issues had the scope for it, so the gate passed and
// the API refused, with a message blaming the account rather than the grant.
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

			CanRead:        true,
			CanEdit:        true,
			CanDelete:      true,
			CanReact:       true,
			CanThread:      true,
			CanUpload:      true,
			CanListSpaces:  true,
			CanResolveDM:   true,
			CanReadMembers: true,
		}
	}
	return Capabilities{}
}

// grants maps one capability to the scopes that permit it, any one of which is
// enough.
//
// A table rather than a chain of conditions, so that adding a capability
// without saying what grants it is visible here as a missing row rather than
// invisible as a condition nobody wrote. A capability absent from this table is
// one no scope gates, which is the honest answer for CanSendCards: no scope
// grants it, because what it needs is app authentication rather than permission.
var grants = map[Capability][]string{
	CanSend:       {auth.ScopeSendOnly, auth.ScopeMessages},
	CanRead:       {auth.ScopeReadOnly, auth.ScopeMessages},
	CanEdit:       {auth.ScopeMessages},
	CanDelete:     {auth.ScopeMessages},
	CanReact:      {auth.ScopeReactions, auth.ScopeMessages},
	CanThread:     {auth.ScopeSendOnly, auth.ScopeMessages},
	CanUpload:     {auth.ScopeSendOnly, auth.ScopeMessages},
	CanListSpaces: {auth.ScopeSpacesRO, auth.ScopeSpaces},
	// spaces:findDirectMessage answers on chat.spaces.readonly. Measured against
	// the live API on 2026-08-16, not read from the reference: with a token
	// holding chat.messages, chat.spaces.readonly and chat.memberships.readonly
	// and not chat.spaces, the endpoint returned 200 and the DM's resource name.
	//
	// It was {ScopeSpaces} until that probe, which would have refused DM
	// resolution at exit 5 on every profile this tool creates, for want of a
	// scope the operation does not need. That is the `spaces members` bug
	// inverted: there the capability was claimed and ungranted, here the grant
	// was real and the capability said otherwise. Both refuse work that would
	// have succeeded, and neither is visible to a test that answers its own
	// requests.
	CanResolveDM:   {auth.ScopeSpacesRO, auth.ScopeSpaces},
	CanReadMembers: {auth.ScopeMembers},
}

// ScopedCapabilities is what a profile can actually do: the transport kind's
// ceiling, narrowed to what its credential was granted.
//
// The narrowing only applies where a scope is what stands in the way. A webhook
// has no scopes at all, because it is a URL rather than a token, so its row
// passes through untouched; asking whether a webhook was granted
// chat.memberships.readonly is a category error, not a refusal.
//
// The point of doing this before the network rather than after it is the
// message. A capability missing here is refused at exit 5 naming the command
// that re-authorizes, and the same capability missing at the far end is a 403
// that says the account is not allowed, which sends somebody to an
// administrator to fix something that `auth login` fixes.
func ScopedCapabilities(kind config.Transport, scopes []string) Capabilities {
	caps := CapabilitiesFor(kind)
	if kind != config.TransportUserOAuth {
		return caps
	}

	held := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		held[scope] = true
	}

	for want, permitting := range grants {
		if !caps.Has(want) {
			continue
		}
		if !anyHeld(held, permitting) {
			caps.clear(want)
		}
	}
	return caps
}

// anyHeld reports whether the grant holds any one of the scopes that permit a
// capability. Any rather than all: chat.messages subsumes the narrow write
// scopes, and requiring both would refuse a profile that holds the broader one.
func anyHeld(held map[string]bool, permitting []string) bool {
	for _, scope := range permitting {
		if held[scope] {
			return true
		}
	}
	return false
}

// Fixed is a transport that can reach exactly one space.
//
// An optional interface rather than a method on Transport, because it is only
// true of one of the two: a webhook is issued for one space and is the only
// thing that authenticates the request, while a user-authorized profile reaches
// every space that account can. A Space() on the interface would force the
// second one to answer a question it has no answer to.
//
// What it buys is the whole of the zero-ceremony case. A caller whose profile
// can only reach one space should not have to name it, and a command can find
// out whether that is so without knowing which transports exist.
type Fixed interface {
	Transport

	// Space is where this transport posts, in spaces/AAAA form.
	Space() string
}

// SpaceOf returns the one space t can reach, and whether there is one.
func SpaceOf(t Transport) (string, bool) {
	fixed, ok := t.(Fixed)
	if !ok {
		return "", false
	}
	return fixed.Space(), true
}
