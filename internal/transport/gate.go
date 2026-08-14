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
}

// capabilityTable is indexed by Capability. A slice rather than a map so that
// the compiler's own bounds are part of the check, and so that the order here
// has to match the order above.
var capabilityTable = [numCapabilities]capability{
	CanSend:       {"the ability to send", func(c Capabilities) bool { return c.CanSend }},
	CanSendCards:  {"card support", func(c Capabilities) bool { return c.CanSendCards }},
	CanRead:       {"read access", func(c Capabilities) bool { return c.CanRead }},
	CanEdit:       {"the ability to edit a message", func(c Capabilities) bool { return c.CanEdit }},
	CanDelete:     {"the ability to delete a message", func(c Capabilities) bool { return c.CanDelete }},
	CanReact:      {"the ability to react to a message", func(c Capabilities) bool { return c.CanReact }},
	CanThread:     {"threading", func(c Capabilities) bool { return c.CanThread }},
	CanUpload:     {"attachment upload", func(c Capabilities) bool { return c.CanUpload }},
	CanListSpaces: {"the ability to list spaces", func(c Capabilities) bool { return c.CanListSpaces }},
	CanResolveDM:  {"the ability to find a direct message", func(c Capabilities) bool { return c.CanResolveDM }},
}

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
func Require(t Transport, command string, needed ...Capability) error {
	caps := t.Capabilities()
	for _, want := range needed {
		if caps.Has(want) {
			continue
		}
		return unsupported(t, command, want)
	}
	return nil
}

func unsupported(t Transport, command string, want Capability) error {
	message := fmt.Sprintf("%q needs %s, and profile %q %s\n%s",
		command, describe(want), t.Profile(), explainKind(t.Kind()), fix(t.Kind(), want))

	return &output.Error{
		Code:    "UNSUPPORTED",
		Exit:    output.ExitUnsupported,
		Message: message,
		Err:     ErrUnsupported,
	}
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
		return "Use a profile whose transport is " + string(config.TransportUserOAuth) +
			", or set one up with: " + meta.AppName + " auth login --profile NAME"
	}

	if kind == config.TransportUserOAuth && want == CanSendCards {
		return "Use a profile whose transport is " + string(config.TransportWebhook) +
			". A webhook posts as an app, which is what a card needs."
	}
	return "Use a profile whose transport is " + string(config.TransportUserOAuth) + "."
}
