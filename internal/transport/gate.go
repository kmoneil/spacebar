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
	"errors"
	"fmt"
	"iter"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// ErrUnsupported is a capability the active profile's transport does not have.
//
// A sentinel so that a caller can tell "this profile cannot" from "this
// failed", which are different enough that they have different exit codes: the
// first is answered by using another profile and the second by looking at a
// network.
var ErrUnsupported = errors.New("this profile's transport cannot do that")

// Capability is one thing a transport may be able to do.
//
// An index into Capabilities rather than a copy of it. The struct is the data,
// per SPEC.md §8; this is how a caller names one of its fields in a sentence.
type Capability int

const (
	CanSend Capability = iota
	CanSendCards
	CanRead
	CanEdit
	CanDelete
	CanReact
	CanThread
	CanUpload
	CanListSpaces
	CanResolveDM
	CanReadMembers

	// numCapabilities is the count, and it is last so that adding a capability
	// above it moves it. capabilityTable is held to it by a test, so a
	// capability added without a description fails the build rather than
	// producing an error message with a blank in it.
	numCapabilities
)

// capability is how one capability reads to somebody who just hit it.
type capability struct {
	// need completes "'tail' needs ...". A phrase rather than a field name,
	// because CanResolveDM is not a sentence.
	need string

	// has reads the field out of a Capabilities.
	has func(Capabilities) bool

	// clear turns the field off, for ScopedCapabilities, which narrows a
	// transport kind's ceiling to what its credential was granted. Paired with
	// has in the same row so that the reader and the writer cannot name
	// different fields, which is the whole failure mode of two parallel tables.
	clear func(*Capabilities)
}

// capabilityTable is indexed by Capability. A slice rather than a map so that
// the compiler's own bounds are part of the check, and so that the order here
// has to match the order above.
var capabilityTable = [numCapabilities]capability{
	CanSend: {
		"the ability to send",
		func(c Capabilities) bool { return c.CanSend },
		func(c *Capabilities) { c.CanSend = false },
	},
	CanSendCards: {
		"card support",
		func(c Capabilities) bool { return c.CanSendCards },
		func(c *Capabilities) { c.CanSendCards = false },
	},
	CanRead: {
		"read access",
		func(c Capabilities) bool { return c.CanRead },
		func(c *Capabilities) { c.CanRead = false },
	},
	CanEdit: {
		"the ability to edit a message",
		func(c Capabilities) bool { return c.CanEdit },
		func(c *Capabilities) { c.CanEdit = false },
	},
	CanDelete: {
		"the ability to delete a message",
		func(c Capabilities) bool { return c.CanDelete },
		func(c *Capabilities) { c.CanDelete = false },
	},
	CanReact: {
		"the ability to react to a message",
		func(c Capabilities) bool { return c.CanReact },
		func(c *Capabilities) { c.CanReact = false },
	},
	CanThread: {
		"threading",
		func(c Capabilities) bool { return c.CanThread },
		func(c *Capabilities) { c.CanThread = false },
	},
	CanUpload: {
		"attachment upload",
		func(c Capabilities) bool { return c.CanUpload },
		func(c *Capabilities) { c.CanUpload = false },
	},
	CanListSpaces: {
		"the ability to list spaces",
		func(c Capabilities) bool { return c.CanListSpaces },
		func(c *Capabilities) { c.CanListSpaces = false },
	},
	CanResolveDM: {
		"the ability to find a direct message",
		func(c Capabilities) bool { return c.CanResolveDM },
		func(c *Capabilities) { c.CanResolveDM = false },
	},
	CanReadMembers: {
		"the ability to read who is in a space",
		func(c Capabilities) bool { return c.CanReadMembers },
		func(c *Capabilities) { c.CanReadMembers = false },
	},
}

// String is what this capability is called in a sentence, so that a %s or a %v
// anywhere reads as something rather than as an integer. It is the same phrase
// the refusal uses, from the same table, because two names for one capability
// is one more than anybody needs.
func (c Capability) String() string { return describe(c) }

// Has reports whether these capabilities include one.
func (c Capabilities) Has(want Capability) bool {
	if want < 0 || want >= numCapabilities {
		// An unknown capability is not held. Failing closed matters more than
		// failing loudly here: the alternative reading, that anything we cannot
		// identify is permitted, is how a gate stops being one.
		return false
	}
	return capabilityTable[want].has(c)
}

// clear turns one capability off. Unexported: narrowing happens in
// ScopedCapabilities and nowhere else, because a capability a caller could
// switch off is a gate a caller could switch off.
func (c *Capabilities) clear(want Capability) {
	if want < 0 || want >= numCapabilities {
		return
	}
	capabilityTable[want].clear(c)
}

// Require refuses, before anything is built or sent, when this profile's
// transport cannot do what the command needs (SPEC.md §8.2).
//
// The exit code is 5 and the message names both the profile and the fix. What
// makes it worth having, given that a transport refuses from the inside anyway,
// is the command name: a transport cannot know which one was run, and "'tail'
// needs read access" sends somebody somewhere useful in a way that "this
// profile cannot read" does not.
//
// The first missing capability is reported rather than all of them. Somebody
// whose profile cannot read also cannot edit, delete, or react, and listing
// four consequences of one fact reads as four problems.
// Profiled is the part of a Transport a refusal needs: what kind it is, which
// profile it belongs to, and what it can do.
//
// Smaller than Transport so that a caller which only resolves a name can be
// gated without also being handed the ability to send. internal/resolve is the
// first such caller, and a resolver that could send would be one bug away from
// sending.
type Profiled interface {
	Kind() config.Transport
	Profile() string
	Capabilities() Capabilities
}

func Require(t Profiled, command string, needed ...Capability) error {
	caps := t.Capabilities()
	for _, want := range needed {
		if caps.Has(want) {
			continue
		}
		return unsupported(t, command, want)
	}
	return nil
}

// Unsupported is the refusal a transport returns from inside a capability it
// does not have.
//
// Exported because the implementations live in their own packages and have to
// refuse in the same words as Require does, from the same table. A webhook that
// wrote its own sentence for "cannot read" would be a second wording of one
// fact, and the two would drift the first time either was improved.
func Unsupported(t Profiled, command string, want Capability) error {
	return unsupported(t, command, want)
}

// Refused is Unsupported for a read path that returns an iterator.
//
// The refusal arrives through the same channel as any other failure, as the
// error half of the first pair, so a caller that ranges over the result handles
// it in the place it already handles a 403. The alternative, a second return
// value on every list method, is one every caller has to check before starting
// the range and one of them eventually would not.
func Refused[T any](t Profiled, command string, want Capability) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		yield(zero, Unsupported(t, command, want))
	}
}

func unsupported(t Profiled, command string, want Capability) error {
	message := fmt.Sprintf("%q needs %s, and profile %q %s\n%s",
		command, describe(want), t.Profile(), explainKind(t.Kind()), fix(t.Kind(), want))

	// A capability the transport kind has but this profile does not is a grant
	// that was never asked for, not a limit of the transport. Saying "a webhook
	// is write-only" to somebody holding a user-OAuth token would be false, and
	// saying "use a profile whose transport is useroauth" to somebody already on
	// one is the kind of advice that reads as a bug in the tool.
	if CapabilitiesFor(t.Kind()).Has(want) {
		message = fmt.Sprintf("%q needs %s, and profile %q was not granted it.\n%s",
			command, describe(want), t.Profile(), reauthorize(t.Profile()))
	}

	return &output.Error{
		Code:    "UNSUPPORTED",
		Exit:    output.ExitUnsupported,
		Message: message,
		Err:     ErrUnsupported,
	}
}

// reauthorize names the command that widens a grant.
//
// The scopes are not listed. What somebody has to do about it is identical
// whichever one is missing, and a scope URL in an error message is forty
// characters that answer a question nobody asked: `auth status` prints the
// granted scopes for anybody who wants them.
func reauthorize(profile string) string {
	return "Consent to the scope it needs by authorizing again:\n" +
		"  " + meta.AppName + " auth login --profile " + profile + "\n" +
		"The scopes this build asks for have grown since that token was issued."
}

func describe(want Capability) string {
	if want < 0 || want >= numCapabilities {
		return "a capability this build does not have a name for"
	}
	return capabilityTable[want].need
}

// explainKind says what the profile is, in the terms that explain the refusal.
//
// The explanation is here rather than left to the reader because the reader has
// no way to work it out. Somebody who was handed a webhook URL by a colleague
// does not necessarily know that a webhook is write-only, and being told their
// profile "uses a webhook" answers nothing.
func explainKind(kind config.Transport) string {
	switch kind {
	case config.TransportWebhook:
		return "is an incoming webhook, which is fixed to one space, write-only, and posts as a bot."
	case config.TransportUserOAuth:
		return "is authorized as you, and a send made that way is text-only, because a card requires app authentication."
	}
	return "uses a transport this build does not recognize."
}

// fix names something to do about it.
//
// Every branch here names a concrete next step, because a refusal that only
// says no is a refusal somebody has to go and research. The webhook case is the
// common one and it is the one where the answer costs real effort, so it says
// what setting up the alternative involves rather than only naming it.
func fix(kind config.Transport, want Capability) string {
	if kind == config.TransportWebhook {
		// This named no command until m3-04, on the grounds that sending
		// somebody to `auth login` before the binary had it would be a second
		// dead end on top of the first. The transport exists now, so the
		// refusal can name the whole path instead of only the destination.
		//
		// Both commands, in order, because the first is the one that fails
		// otherwise. A build from source has no OAuth client linked into it by
		// design, so `auth login` on a fresh checkout has nothing to authorize
		// against, and being sent straight to it would be the third dead end.
		return "Use a profile whose transport is " + string(config.TransportUserOAuth) + ":\n" +
			"  " + meta.AppName + " auth setup --profile NAME < client_secret.json\n" +
			"  " + meta.AppName + " auth login --profile NAME\n" +
			"Run '" + meta.AppName + " auth setup' on its own to see how to create the client."
	}

	if kind == config.TransportUserOAuth && want == CanSendCards {
		return "Use a profile whose transport is " + string(config.TransportWebhook) +
			". A webhook posts as an app, which is what a card needs."
	}
	return "Use a profile whose transport is " + string(config.TransportUserOAuth) + "."
}
