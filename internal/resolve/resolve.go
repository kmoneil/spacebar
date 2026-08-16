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

// Package resolve turns what somebody typed into a space resource name.
//
// Four steps in a fixed order, and the last one never guesses. A literal
// spaces/XXXX passes through, an alias substitutes, anything with an @ is a
// direct message lookup, and anything else is matched against the display names
// of the spaces this profile can reach.
//
// The order matters more than any one step. Each is cheaper and more certain
// than the next, so the expensive ambiguous one only runs when nothing else
// could answer, and a caller who typed an exact resource name never pays for a
// network call or a cache read.
//
// Everything this package returns has been through chat.CheckSpaceName, which
// is the point of it existing rather than of each command doing its own
// substitution. A display name came from the API and an alias came from a file
// somebody may have been sent; both are values from elsewhere on their way into
// a request path (SPEC.md §15.8).
package resolve

import (
	"context"
	"errors"
	"iter"
	"strings"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
)

// Reader is the part of a transport this package needs.
//
// An interface of two methods rather than the whole transport, because a
// resolver that could send is a resolver one bug away from sending. It also
// makes the test for "a webhook refuses steps three and four" a test of this
// package rather than of a transport.
type Reader interface {
	Spaces(ctx context.Context, req chat.ListSpacesRequest) iter.Seq2[chat.Space, error]
	FindDirectMessage(ctx context.Context, user string) (*chat.Space, error)
	Capabilities() transport.Capabilities
	Profile() string
	Kind() config.Transport
}

// Options configure one resolution.
type Options struct {
	// Aliases is the profile's own map of name to spaces/XXXX. Read before any
	// network call, and before the display-name pass, so that somebody who has
	// named a space can rely on that name meaning what they said it means.
	Aliases map[string]string

	// Cache holds the display names this profile last saw. Nil disables both
	// reading and writing it, which is what a --refresh with no writable cache
	// directory falls back to.
	Cache *Cache

	// Refresh discards whatever the cache holds and lists spaces again.
	Refresh bool
}

// Resolve turns a target into a space resource name.
//
// The empty string resolves to itself. A command that takes an optional target
// uses the empty value to mean "the profile's own space", and turning that into
// a lookup would make `send 'text'` on a webhook fail.
func Resolve(ctx context.Context, r Reader, target string, opts Options) (string, error) {
	if target == "" {
		return "", nil
	}

	// 1. A literal resource name. Checked rather than trusted: a caller can type
	// spaces/../.. as easily as a real one, and this is the branch where a value
	// reaches a request path without passing anything else.
	if strings.HasPrefix(target, "spaces/") {
		if err := chat.CheckSpaceName(target); err != nil {
			return "", err
		}
		return target, nil
	}

	// 2. An alias. A local map, so it needs no capability and works on a
	// webhook profile, where the two steps below cannot. Its value is checked
	// because config.json is a file somebody may have been handed.
	if space, ok := opts.Aliases[target]; ok {
		if err := chat.CheckSpaceName(space); err != nil {
			return "", aliasErr(target, space, err)
		}
		return space, nil
	}

	// 3. An address is a person, not a space. Decided by the @ rather than by
	// trying the lookup and falling through on failure: a fallthrough would send
	// a mistyped address to the display-name matcher, which would either find
	// nothing or, worse, find something.
	if strings.Contains(target, "@") {
		return directMessage(ctx, r, target)
	}

	// 4. A display name. Last because it is the only step that can be
	// ambiguous, and the only one whose answer depends on what somebody else
	// named their space.
	return byDisplayName(ctx, r, target, opts)
}

// directMessage resolves an address to the direct message space with that
// person.
func directMessage(ctx context.Context, r Reader, address string) (string, error) {
	if err := transport.Require(r, "resolving "+address, transport.CanResolveDM); err != nil {
		return "", err
	}

	user := "users/" + address
	if err := chat.CheckUserName(user); err != nil {
		return "", err
	}

	space, err := r.FindDirectMessage(ctx, user)
	if err != nil {
		return "", directMessageErr(address, err)
	}
	if space == nil || space.Name == "" {
		// A 200 with no name is not something the API is documented to do. It is
		// handled because the alternative is returning "" as though it were a
		// space, and the empty target means "the profile's own space" to every
		// caller of this package.
		return "", output.Errorf("API_ERROR", output.ExitAPI,
			"the direct message lookup for %q succeeded and named no space.", address)
	}

	// The API's own answer, on its way into a request path.
	if err := chat.CheckSpaceName(space.Name); err != nil {
		return "", err
	}
	return space.Name, nil
}

// directMessageErr explains a failed lookup in the terms the API distinguishes.
//
// Measured on 2026-08-16, and not what a reader of the reference would predict:
// a user reference that does not resolve to anybody is 400 INVALID_ARGUMENT,
// while a real person with no direct message is 404 NOT_FOUND. So the two are
// tellable apart, and saying "there is no such user" when the truth is "you
// have never spoken to them" sends somebody to check their spelling for no
// reason.
func directMessageErr(address string, err error) error {
	if errors.Is(err, chat.ErrNotFound) {
		return output.Errorf("NOT_FOUND", output.ExitAPI,
			"there is no direct message with %q yet.\n"+
				"Chat creates one when somebody sends the first message, and this tool does not "+
				"create spaces. Open the conversation in Chat once, or name a space directly.", address)
	}

	// 400 rather than 404, which is the API saying it could not resolve the
	// address to anybody at all. The exit code is the same, so the sentence is
	// the only thing that tells somebody whether to check their spelling.
	if errors.Is(err, chat.ErrInvalidRequest) {
		return output.Errorf("NOT_FOUND", output.ExitAPI,
			"%q is not a user this account can look up.\n"+
				"The address has to be one the directory knows. A guest, an external address, "+
				"or a typo all arrive here. Check it, or name a space directly.", address)
	}
	return err
}

// aliasErr reports an alias whose value is not a space.
//
// It names the alias as well as the value, because the thing to fix is the
// entry in the configuration file and not what was typed on the command line.
//
// It named no command until m4-02 added one, on the grounds that a refusal
// pointing at a command this build does not have is the second dead end on top
// of the first. That milestone landed, so it names the command that fixes it.
func aliasErr(name, value string, err error) error {
	return output.Usagef("alias %q points at %q, which is not a space name.\n%v\n"+
		"Point it somewhere real with: %s alias set %s spaces/AAAAAAA",
		name, value, err, meta.AppName, name)
}
