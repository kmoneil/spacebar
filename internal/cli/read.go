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

package cli

import (
	"errors"
	"iter"
	"time"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
)

// defaultLimit is how many items a list returns when nobody said.
//
// Twenty-five rather than everything, because the common case is a person or an
// agent looking at a space to find out what is going on, and the uncommon case
// is an export. A default of everything would make the common case slow, spend a
// per-space quota shared with every other app acting in that space, and put an
// unbounded document on somebody's terminal.
const defaultLimit = 25

// limitHelp is the same sentence everywhere a limit is registered, because three
// wordings of one flag is how two of them end up meaning different things.
const limitHelp = "how many to return; 0 means every one, fetched a page at a time"

// The window flags, worded once for the same reason.
//
// "strictly" is in both because the API put it there: createTime accepts > and
// <, and answers >= with 400 "Invalid filter query". A caller who reads "since
// 09:00" and does not see the message posted at 09:00 exactly should have been
// told, and the place to tell them is the line they read before typing it.
const (
	sinceHelp = "only messages created strictly after this: an RFC 3339 time, or how long ago, as in 90m or 2h"
	untilHelp = "only messages created strictly before this, in the same two forms as --since"
)

// intervalHelp is the same flag on tail and on watch, and one constant because
// the two poll the same way. Two copies of it drifted apart once already, in
// the loop rather than in the help: watch --all had no backoff at all while
// tail had one.
//
// It says the direction rather than the number, because the number depends on
// what was asked for. A quiet space doubles up to a minute, or stays where it
// is if a minute is more often than the interval given.
const intervalHelp = "how often to poll, at least 2s; a space with nothing new is polled less often, never more"

// watchSinceHelp is the same flag on watch, worded separately because it bounds
// a different thing, which is the one reason worth breaking a shared wording
// for.
//
// The three callers above drive messages.list, where the comparison is on
// createTime. watch drives spaceEvents.list, where it is on eventTime, and the
// two come apart on exactly the events watch exists to show. Measured on
// 2026-08-18 against a real space, and by the query rather than by reading the
// field: a --since more than two minutes after a message was created returned
// the event for that message's deletion, because the deletion is what happened
// after the bound. Somebody who read "only messages created strictly after
// this" before typing it was told the wrong thing by the line they read.
// watch also yields reactions and memberships, which are not messages at all.
//
// "strictly" survives the move and is now measured here rather than carried
// across from a claim about the other endpoint: start_time excludes its own
// instant. See eventFilter in internal/chat.
const watchSinceHelp = "only events that happened strictly after this: an RFC 3339 time, or how long ago, as in 90m or 2h"

// parseSince turns a window flag into a time, treating unset as unbounded.
//
// The parsing itself lives in internal/chat beside CheckInterval, so that the
// MCP server takes the same argument by the same rule. What is here is the one
// thing that is genuinely the flag's: an empty string is not a bad time, it is
// nobody having asked for a bound.
func parseSince(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return chat.ParseWhen(value, time.Now())
}

// openProfile resolves the active profile and reports its credential warnings.
//
// The warnings are read off the result before the error is examined, which is
// the order internal/profile documents and the reason it never returns a nil
// Open. A credential that came off a disk in plain text is worth saying whether
// or not the command then worked, and a caller that checked the error first
// would drop that on exactly the runs where it matters most.
func openProfile(opts *Options, r *output.Renderer) (*profile.Open, error) {
	opened, err := profile.For(profile.Options{
		Name:    opts.Profile,
		Timeout: opts.Timeout,
		Log:     verboseLog(opts, r),
		DryRun:  opts.DryRun,
	})
	r.Warnings(opened.Warnings)
	if err != nil {
		return nil, err
	}
	return opened, nil
}

// stream writes an iterator's items to stdout, one at a time, and stops at the
// first failure.
//
// This is where the streaming promise in SPEC.md §11.2 is actually kept: an item
// is written as it arrives, so a caller piping --json into jq sees the first
// object before the last page has been fetched.
//
// It carries the one honest cost of streaming, and it is worth stating rather
// than discovering. A failure on page four arrives after three pages have
// already been written, and those lines cannot be recalled. What holds the
// contract together instead is that the exit code is non-zero, the failure is on
// stderr, and every line already written is a complete object rather than half
// of one. A caller that checks the exit code is never misled; a caller that
// ignores it was going to be misled by any incomplete answer.
//
// The alternative is to buffer every page before writing anything, which trades
// the streaming property for the appearance of atomicity and would make `tail`
// impossible in Milestone 4.
// finish turns a read's failure into a dry-run report when that is what it is.
//
// --dry-run stops every request in the client, on the line before the send, so
// that no command can forget it. The consequence is that a read gets stopped
// too, and a read that did not handle the answer would report "dry run: the
// request below was not sent" as a generic failure at exit 1, which is what this
// did before the handling existed.
//
// A dry run of a read is worth having rather than worth suppressing. It is how
// somebody checks which space a command resolved to, what page size it asked
// for, and that the Authorization header is present and redacted, without
// spending a request on a quota shared with every other app in the space.
//
// Nothing has been written to stdout when this fires on a list, because the
// client refuses the very first page, so the preview is the whole of the output
// rather than an addition to a partial one.
func finish(r *output.Renderer, opened *profile.Open, err error) error {
	dry, ok := errors.AsType[*chat.DryRun](err)
	if !ok {
		return err
	}

	// The profile goes to stderr for the reason it does on send: stdout carries
	// the request and nothing else, and a profile line is not part of one.
	r.Note("through profile %q. Nothing was sent.", opened.Name)
	return r.Block(dry.Request, dry.Request.Text())
}

// stream is generic over the row shape as well as the item, so a projection in
// internal/rows can be passed by name rather than wrapped in a closure at every
// call site. The renderer takes an any for the structured half, and one that
// arrives as a concrete type is exactly as encodable.
func stream[T, R any](r *output.Renderer, items iter.Seq2[T, error], row func(T) (R, []string)) error {
	for item, err := range items {
		if err != nil {
			return err
		}
		data, cells := row(item)
		if err := r.Item(data, cells...); err != nil {
			return err
		}
	}
	return nil
}
