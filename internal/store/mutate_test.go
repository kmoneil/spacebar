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
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/rows"
)

// The mutation half of the index, tested the way the sync walk is: one faked
// method, and everything else real.
//
// Neither of these can be driven through a command. Both need CanEdit or
// CanDelete, which only a user-OAuth profile has, and nothing here can point
// one at a test server, because chat.BaseURL is a constant so that no
// environment variable can redirect where a credential goes. So the seam is the
// transport method, and what is exercised below is the real walk against a real
// index on a real temporary directory.

// fakeMutations is the transport half of a recorded mutation.
//
// It records what it was asked to do, because half of these claims are about
// what reached the transport rather than about what came back: a name refused
// before the request and one refused after it produce the same error, and only
// the count tells them apart.
type fakeMutations struct {
	edited  []chat.EditRequest
	deleted []string

	// reply is what EditMessage answers with. Nil means the request echoed
	// back with a last update time, which is what the API does.
	reply *chat.Message

	// fail is returned in place of doing anything, standing in for the 403
	// that editing somebody else's message gets.
	fail error

	// before runs at the moment the transport is entered, which is how the
	// ordering claim is settled: whatever the index says then, it says before
	// the change has happened.
	before func()
}

func (f *fakeMutations) EditMessage(_ context.Context, req chat.EditRequest) (*chat.Message, error) {
	if f.before != nil {
		f.before()
	}
	f.edited = append(f.edited, req)
	if f.fail != nil {
		return nil, f.fail
	}
	if f.reply != nil {
		return f.reply, nil
	}
	return &chat.Message{
		Name:           req.Message,
		CreateTime:     at(1),
		LastUpdateTime: at(9),
		Text:           req.Text,
	}, nil
}

func (f *fakeMutations) DeleteMessage(_ context.Context, message string) error {
	if f.before != nil {
		f.before()
	}
	f.deleted = append(f.deleted, message)
	return f.fail
}

// refusingIndex is a Recorder that cannot write, which is the one case a real
// index on a temporary directory cannot be made to produce on every platform
// this builds for: a permission bit is not a permission bit on Windows, and a
// test that quietly passes there is worse than one that is not written.
type refusingIndex struct{ err error }

func (r refusingIndex) Update(context.Context, string, rows.Message) error { return r.err }
func (r refusingIndex) Delete(context.Context, string, string) error       { return r.err }

// indexWith is an index holding two messages in testSpace, which is what a
// space that has been synced looks like.
func indexWith(t *testing.T) *NDJSON {
	t.Helper()

	index := NewNDJSON(t.TempDir())
	if err := index.Append(context.Background(), testSpace, []rows.Message{
		{Name: testSpace + "/messages/AAA", CreateTime: at(1), Text: "the original words"},
		{Name: testSpace + "/messages/BBB", CreateTime: at(5), Text: "this one goes away"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	return index
}

// TestAnEditThisToolMadeIsFoundByTheTextItHasNow.
//
// The claim three documents made and nothing implemented. sync walks createTime
// in two windows and an edit does not change createTime, so a message this tool
// edited was never re-read and the copy kept the words it had when it was taken.
// The API answers an edit with the message as it now is, so recording it costs
// no request at all.
func TestAnEditThisToolMadeIsFoundByTheTextItHasNow(t *testing.T) {
	index := indexWith(t)
	messages := &fakeMutations{}

	edited, warnings, err := EditMessage(context.Background(), index, messages, chat.EditRequest{
		Message: testSpace + "/messages/AAA",
		Text:    "the replacement words",
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("an edit that was recorded warned: %v", warnings)
	}
	if edited == nil || edited.Text != "the replacement words" {
		t.Fatalf("the edited message did not come back: %+v", edited)
	}

	if found := collect(t, index, Query{Text: "original"}); len(found) != 0 {
		t.Errorf("the text that was replaced is still searchable: %+v", found)
	}
	found := collect(t, index, Query{Text: "replacement"})
	if len(found) != 1 {
		t.Fatalf("the new text is not searchable: %+v", found)
	}
	if found[0].Name != testSpace+"/messages/AAA" {
		t.Errorf("the record is for %s, want the message that was edited", found[0].Name)
	}
}

// TestADeletionThisToolMadeIsNotFoundAtAll.
//
// The other half of the same claim, and the worse one to get wrong: a search
// that answers with a message somebody deleted is answering with words nobody
// can find in the space, which is the whole reason to keep an index that agrees
// with what a person would see.
func TestADeletionThisToolMadeIsNotFoundAtAll(t *testing.T) {
	index := indexWith(t)
	messages := &fakeMutations{}
	removed := testSpace + "/messages/BBB"

	warnings, err := DeleteMessage(context.Background(), index, messages, removed)
	if err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("a deletion that was recorded warned: %v", warnings)
	}
	if !slices.Equal(messages.deleted, []string{removed}) {
		t.Errorf("the transport was asked to delete %v, want %s", messages.deleted, removed)
	}

	if found := collect(t, index, Query{Text: "goes away"}); len(found) != 0 {
		t.Errorf("a deleted message is still searchable: %+v", found)
	}
	if found := collect(t, index, Query{Text: "original"}); len(found) != 1 {
		t.Errorf("the message that was not deleted is gone too: %+v", found)
	}
}

// TestNothingIsRecordedWhenTheChangeDidNotHappen.
//
// The index records what happened rather than what was attempted. Editing
// somebody else's message is a 403, measured against a real space, and a copy
// updated on the strength of a request that was refused would be a local
// history of edits nobody made.
func TestNothingIsRecordedWhenTheChangeDidNotHappen(t *testing.T) {
	refused := errors.New("403 PERMISSION_DENIED")

	t.Run("edit", func(t *testing.T) {
		index := indexWith(t)

		_, _, err := EditMessage(context.Background(), index, &fakeMutations{fail: refused},
			chat.EditRequest{Message: testSpace + "/messages/AAA", Text: "the replacement words"})
		if !errors.Is(err, refused) {
			t.Fatalf("EditMessage = %v, want the transport's own failure", err)
		}
		if found := collect(t, index, Query{Text: "original"}); len(found) != 1 {
			t.Errorf("the copy changed on a refused edit: %+v", found)
		}
		if found := collect(t, index, Query{Text: "replacement"}); len(found) != 0 {
			t.Errorf("text from a refused edit reached the index: %+v", found)
		}
	})

	t.Run("delete", func(t *testing.T) {
		index := indexWith(t)

		_, err := DeleteMessage(context.Background(), index, &fakeMutations{fail: refused},
			testSpace+"/messages/BBB")
		if !errors.Is(err, refused) {
			t.Fatalf("DeleteMessage = %v, want the transport's own failure", err)
		}
		if found := collect(t, index, Query{Text: "goes away"}); len(found) != 1 {
			t.Errorf("a refused delete wrote a tombstone: %+v", found)
		}
	})
}

// TestAChangeIsRecordedOnlyAfterTheAPIHasSaidSo.
//
// The ordering, asserted from inside the transport call rather than inferred
// from the result. Writing the record first would look identical afterwards in
// every test above, and would be wrong in exactly the case that matters: a
// request that fails after the index was written leaves a copy describing a
// change that never happened, and nothing later corrects it.
func TestAChangeIsRecordedOnlyAfterTheAPIHasSaidSo(t *testing.T) {
	t.Run("edit", func(t *testing.T) {
		index := indexWith(t)
		messages := &fakeMutations{before: func() {
			if found := collect(t, index, Query{Text: "original"}); len(found) != 1 {
				t.Errorf("the index was written before the edit was made: %+v", found)
			}
		}}

		if _, _, err := EditMessage(context.Background(), index, messages, chat.EditRequest{
			Message: testSpace + "/messages/AAA",
			Text:    "the replacement words",
		}); err != nil {
			t.Fatalf("EditMessage: %v", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		index := indexWith(t)
		messages := &fakeMutations{before: func() {
			if found := collect(t, index, Query{Text: "goes away"}); len(found) != 1 {
				t.Errorf("the tombstone was written before the delete was made: %+v", found)
			}
		}}

		if _, err := DeleteMessage(context.Background(), index, messages,
			testSpace+"/messages/BBB"); err != nil {
			t.Fatalf("DeleteMessage: %v", err)
		}
	})
}

// TestARecordedChangeNeverBringsASpaceIntoTheIndex.
//
// The finding that shaped this code, and the one nothing else here would have
// caught. A space's file is what says the space has been looked at: Spaces
// reads the directory, Coverage compares that against the profile's cached
// space list, and `search` names what is missing so that a narrow answer is
// never reported as a whole one.
//
// So recording a deletion in a space nobody has synced would move that space
// from "never synced" to "synced and empty", and a later search would stop
// naming it while holding nothing from it at all. That is the truncation rule,
// reached from a command that never mentions the index.
func TestARecordedChangeNeverBringsASpaceIntoTheIndex(t *testing.T) {
	const never = "spaces/AAAANeverSynced"

	root := t.TempDir()
	index := NewNDJSON(root)
	ctx := context.Background()

	if _, err := DeleteMessage(ctx, index, &fakeMutations{}, never+"/messages/AAA"); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if _, _, err := EditMessage(ctx, index, &fakeMutations{}, chat.EditRequest{
		Message: never + "/messages/BBB",
		Text:    "the replacement words",
	}); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}

	spaces, err := index.Spaces()
	if err != nil {
		t.Fatalf("Spaces: %v", err)
	}
	if len(spaces) != 0 {
		t.Errorf("recording a change made the index claim %v", spaces)
	}

	searched, missing, err := index.Coverage([]string{never})
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	if len(searched) != 0 || !slices.Equal(missing, []string{never}) {
		t.Errorf("Coverage = searched %v, missing %v, want a space nobody has synced to still be missing",
			searched, missing)
	}

	// And nothing is on disk, which is the fact the two answers above rest on.
	if entries, err := os.ReadDir(filepath.Join(root, "spaces")); err == nil && len(entries) != 0 {
		t.Errorf("recording a change left %d file(s) in the index directory", len(entries))
	}
}

// TestAChangeTheIndexCouldNotRecordIsAWarningAndNotAFailure.
//
// By the time the index is written the message has already changed for
// everybody who can see the space, so a non-zero exit would report that the
// change did not happen when it did. That is the false report this project puts
// above everything, and it would be produced by the code that exists to keep
// the copy honest.
//
// The warning has to name the message, because the point of it is that
// somebody can go and look.
func TestAChangeTheIndexCouldNotRecordIsAWarningAndNotAFailure(t *testing.T) {
	broken := refusingIndex{err: errors.New("cannot open the index: read-only file system")}
	message := testSpace + "/messages/AAA"

	t.Run("edit", func(t *testing.T) {
		edited, warnings, err := EditMessage(context.Background(), broken, &fakeMutations{},
			chat.EditRequest{Message: message, Text: "the replacement words"})
		if err != nil {
			t.Fatalf("EditMessage = %v, want the edit to be reported as the success it was", err)
		}
		if edited == nil || edited.Text != "the replacement words" {
			t.Errorf("the edited message did not come back: %+v", edited)
		}
		assertNamesTheMessage(t, warnings, message, "read-only file system")
	})

	t.Run("delete", func(t *testing.T) {
		warnings, err := DeleteMessage(context.Background(), broken, &fakeMutations{}, message)
		if err != nil {
			t.Fatalf("DeleteMessage = %v, want the delete to be reported as the success it was", err)
		}
		assertNamesTheMessage(t, warnings, message, "read-only file system")
	})
}

// assertNamesTheMessage holds the shape of an unrecorded warning: one line,
// naming the message and quoting why.
func assertNamesTheMessage(t *testing.T, warnings []string, message, because string) {
	t.Helper()

	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want one: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], message) {
		t.Errorf("the warning does not name the message:\n%s", warnings[0])
	}
	if !strings.Contains(warnings[0], because) {
		t.Errorf("the warning does not say why:\n%s", warnings[0])
	}
}

// TestAMessageNameThatIsNotOneNeverReachesTheTransport.
//
// Counted rather than read off the error, which is the house pattern for a
// refusal. The name is checked here before the request because a name this
// index could never record is a change that would happen in the space and be
// invisible locally, and finding that out afterwards is finding it out too late.
func TestAMessageNameThatIsNotOneNeverReachesTheTransport(t *testing.T) {
	index := indexWith(t)
	ctx := context.Background()

	for _, bad := range []string{"", "spaces/AAAATestSpace", "spaces/AAAATestSpace/messages/..", "nonsense"} {
		t.Run(bad, func(t *testing.T) {
			messages := &fakeMutations{}

			if _, err := DeleteMessage(ctx, index, messages, bad); err == nil {
				t.Errorf("DeleteMessage accepted %q", bad)
			}
			if len(messages.deleted) != 0 {
				t.Errorf("%q reached the transport as a delete", bad)
			}

			if _, _, err := EditMessage(ctx, index, messages, chat.EditRequest{
				Message: bad,
				Text:    "the replacement words",
			}); err == nil {
				t.Errorf("EditMessage accepted %q", bad)
			}
			if len(messages.edited) != 0 {
				t.Errorf("%q reached the transport as an edit", bad)
			}
		})
	}
}

// TestRecordingAnEditDoesNotMoveWhereTheNextSyncResumes.
//
// Both of sync's watermarks are createTime, and an edit does not change it. The
// record written for an edit therefore has to carry the message's own create
// time and not the time it was edited, or the forward window would start after
// messages that were never fetched and the next run would step over them.
//
// That is a silent gap rather than a visible failure, which is why it is
// asserted on Bounds rather than left to the search tests: nothing about the
// answer to a search would look wrong.
func TestRecordingAnEditDoesNotMoveWhereTheNextSyncResumes(t *testing.T) {
	index := indexWith(t)
	ctx := context.Background()

	oldest, newest, count, err := index.Bounds(ctx, testSpace)
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}

	if _, _, err := EditMessage(ctx, index, &fakeMutations{}, chat.EditRequest{
		Message: testSpace + "/messages/AAA",
		Text:    "the replacement words",
	}); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}

	after, latest, total, err := index.Bounds(ctx, testSpace)
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	if !after.Equal(oldest) || !latest.Equal(newest) || total != count {
		t.Errorf("recording an edit moved the window from %s..%s (%d) to %s..%s (%d)",
			formatBound(oldest), formatBound(newest), count,
			formatBound(after), formatBound(latest), total)
	}
}

// TestADeletedNewestMessageDoesNotHideWhatCameAfterIt.
//
// A tombstone can move the forward watermark, and only ever backwards: Bounds
// skips a deleted record, so deleting the newest message held lowers it to the
// one before. That costs a re-fetch and can never lose anything, and this is
// the run that proves the second half rather than asserting the first.
//
// The wrong fix is the reason it is written this way. Keeping the deleted
// message's time as the watermark, so that the re-fetch does not happen, would
// make the next forward window start after a message that arrived in between,
// and the index would step over it in silence. So the sequence here is the one
// that tells the two apart: sync, delete the newest, let another message
// arrive, sync again.
//
// Nothing today could produce that watermark, and the assertion on the request
// is here anyway. A tombstone carries no create time, so it cannot be a bound
// even if Bounds stopped skipping deleted records, and the two changes that
// would make it one are both plausible: recording the deleted message's own row
// so that a search could show what was removed, and counting a tombstone so
// that a space of nothing but deletions is not read as empty. Either on its own
// is harmless, and the pair is a silent gap.
func TestADeletedNewestMessageDoesNotHideWhatCameAfterIt(t *testing.T) {
	index := NewNDJSON(t.TempDir())
	ctx := context.Background()

	space := &fakeMessages{messages: []chat.Message{
		{Name: testSpace + "/messages/AAA", CreateTime: at(1), Text: "the first thing"},
		{Name: testSpace + "/messages/BBB", CreateTime: at(5), Text: "the second thing"},
		{Name: testSpace + "/messages/CCC", CreateTime: at(9), Text: "the newest thing"},
	}}

	if _, err := Sync(ctx, index, space, testSpace, 0); err != nil {
		t.Fatalf("the first Sync: %v", err)
	}

	// Deleted here and gone from the space, which is what the API would then
	// answer with.
	if _, err := DeleteMessage(ctx, index, &fakeMutations{}, testSpace+"/messages/CCC"); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	space.messages = space.messages[:2]

	// And something arrives after the message that was deleted was created.
	space.messages = append(space.messages, chat.Message{
		Name: testSpace + "/messages/DDD", CreateTime: at(7), Text: "the thing that came after",
	})

	// The watermark the delete left behind, read before the second sync spends
	// it: the newest message held is the one before the deleted one.
	_, newest, _, err := index.Bounds(ctx, testSpace)
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	if got := formatBound(newest); got != at(5) {
		t.Errorf("the forward watermark is %s, want %s, the newest message still held", got, at(5))
	}

	asked := len(space.requests)
	result, err := Sync(ctx, index, space, testSpace, 0)
	if err != nil {
		t.Fatalf("the second Sync: %v", err)
	}
	if result.Held != 3 {
		t.Errorf("the index holds %d messages, want 3", result.Held)
	}

	// And the request it made says the same thing, which is the half a count of
	// held messages cannot: a forward window opening at the deleted message
	// would skip what arrived before it and after the one still held.
	forward := space.requests[asked]
	if got := formatBound(forward.Since); got != at(5) {
		t.Errorf("the second sync fetched from %s, want %s", got, at(5))
	}

	if found := collect(t, index, Query{Text: "came after"}); len(found) != 1 {
		t.Errorf("a message created before the one that was deleted was stepped over: %+v", found)
	}
	if found := collect(t, index, Query{Text: "newest thing"}); len(found) != 0 {
		t.Errorf("the deleted message is still searchable: %+v", found)
	}
}
