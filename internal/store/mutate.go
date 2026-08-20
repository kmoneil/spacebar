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

package store

import (
	"context"
	"fmt"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/rows"
)

// A change this tool made to a space, recorded in the index as well as sent
// (SPEC.md §12.1).
//
// The index is a copy of what `sync` fetched, and sync walks createTime in two
// windows: everything newer than the newest message held and everything older
// than the oldest. An edit does not change createTime and a deletion produces
// no message at all, so neither is ever re-read, and the copy keeps the text it
// had at the moment it was taken for as long as the file lives. Three documents
// said otherwise until 2026-08-20, when they were corrected to describe the
// snapshot rather than the tool being fixed to match them.
//
// This closes the half of that gap this tool can close without spending a
// request: when this tool is the one making the change, the API has just
// answered, and what happened is in hand. An edit or a deletion made by
// anybody else still needs a pass over the space's events, which is a request
// per space per run on a quota shared with every other app in that space, and
// is a separate decision.
//
// Here rather than in either adapter, because the ordering, the failure rule
// and the never-create rule are decisions, and internal/cli and internal/mcpsrv
// are both thin adapters over this package. A rule two adapters each keep is a
// rule neither keeps: `delete_message` and `edit_message` have no tool yet, and
// the day they arrive they get this behaviour rather than a second copy of it.
//
// It is also the only way any of this can be tested. Both mutations need a
// user-OAuth profile, and nothing can point one at a test server, because
// chat.BaseURL is a constant so that no environment variable can redirect where
// a credential goes. The consumer-side interfaces below are what MessageLister
// already is for the sync walk.

// MessageEditor and MessageDeleter are the parts of a transport a recorded
// mutation goes through, one method each.
//
// Narrow for the reason MessageLister is: this edits, or it deletes, and a
// parameter that could also send is a parameter one bug away from sending.
type MessageEditor interface {
	EditMessage(ctx context.Context, req chat.EditRequest) (*chat.Message, error)
}

// MessageDeleter deletes one message.
type MessageDeleter interface {
	DeleteMessage(ctx context.Context, message string) error
}

// Recorder is the part of the index a change this tool made is written
// through.
//
// Neither method creates a space's file, which is the property that makes this
// safe to call from a command that has nothing to do with the index. See
// NDJSON.writeHeld.
type Recorder interface {
	Update(ctx context.Context, space string, msg rows.Message) error
	Delete(ctx context.Context, space, message string) error
}

// EditMessage replaces a message's text and records the new text locally.
//
// The API answers with the message as it now is, so recording it costs no
// request: the row written here is the same rows.Message the command prints,
// and the index reads newest-record-per-name, so it supersedes the copy that
// was there.
//
// Recording an edit cannot move where the next sync resumes. Both watermarks
// are createTime, an edit does not change it, and the record written here
// carries the message's own createTime, so the windows sync asks for are
// exactly the windows it would have asked for.
func EditMessage(ctx context.Context, index Recorder, messages MessageEditor,
	req chat.EditRequest,
) (*chat.Message, []string, error) {
	// Before the request, so that a name which could never be recorded is
	// refused rather than edited and then quietly not recorded. It is the same
	// check chat.EditMessage makes on the way to the path, and it produces the
	// same error, so nothing about the failure changes by asking here first.
	if _, err := chat.SpaceOfMessage(req.Message); err != nil {
		return nil, nil, err
	}

	edited, err := messages.EditMessage(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	// The space comes from the name the API answered with rather than from the
	// name that was asked for. They are the same message, and this is the one
	// that names the record being written.
	space, err := chat.SpaceOfMessage(edited.Name)
	if err != nil {
		return edited, unrecorded("edited in the space", req.Message, err), nil
	}

	row, _ := rows.ForMessage(*edited)
	if err := index.Update(ctx, space, row); err != nil {
		return edited, unrecorded("edited in the space", edited.Name, err), nil
	}
	return edited, nil, nil
}

// DeleteMessage removes a message from its space and records the removal
// locally.
//
// The order is the whole of it. The message is deleted first and the tombstone
// is written only once the API has said so, so the index records what happened
// rather than what was attempted. A --dry-run never reaches the second half,
// because the client returns *chat.DryRun in place of sending and a caller
// cannot mistake that for a success: it is an error.
//
// A tombstone can move where the next sync resumes, and only ever backwards.
// Bounds skips a deleted record, so removing the newest message held lowers the
// forward watermark and the next run re-fetches from the message before it.
// That costs requests and can never lose anything: a repeat supersedes by name,
// and no window closes over messages that were never fetched.
func DeleteMessage(ctx context.Context, index Recorder, messages MessageDeleter,
	message string,
) ([]string, error) {
	space, err := chat.SpaceOfMessage(message)
	if err != nil {
		return nil, err
	}

	if err := messages.DeleteMessage(ctx, message); err != nil {
		return nil, err
	}

	if err := index.Delete(ctx, space, message); err != nil {
		return unrecorded("deleted from the space", message, err), nil
	}
	return nil, nil
}

// unrecorded is what to say when the change happened and the index did not take
// it.
//
// A warning and not a failure, and that is the decision this file turns on. By
// the time the index is written the message has already been edited or deleted
// for everybody who can see the space, so a non-zero exit would report that the
// change did not happen when it did, which is the false report this project
// puts above everything. What is actually wrong is a local copy that will
// answer a later search with a message that is not there, or with text nobody
// can find, and that is worth a sentence.
//
// It carries no warning code, deliberately. The rule beside the codes is that a
// code means the answer above is narrower than it looks; this answer is
// complete and correct, and what has gone stale is a different command's answer
// on a later day. The codes are frozen the way the exit codes are, so a fifth
// one is easier to add than to withdraw.
//
// Returned rather than printed, because only internal/output writes to a
// process stream.
func unrecorded(what, message string, err error) []string {
	return []string{fmt.Sprintf(
		"%s was %s, and the local index could not record it: %v\n"+
			"The index still holds the copy it had, and nothing else will correct it, "+
			"so a search may answer with a message that is no longer there.",
		message, what, err)}
}
