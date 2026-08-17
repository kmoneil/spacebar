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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/rows"
)

const testSpace = "spaces/AAAATestSpace"

func collect(t *testing.T, index *NDJSON, q Query) []rows.Message {
	t.Helper()

	var found []rows.Message
	for m, err := range index.Search(context.Background(), q) {
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		found = append(found, m)
	}
	return found
}

func at(minutes int) string {
	return time.Date(2026, 8, 17, 9, minutes, 0, 0, time.UTC).Format(time.RFC3339Nano)
}

// TestAMessageLargerThanTheScannerDefaultRoundTrips, the card's first
// falsifiable claim.
//
// The card's reason for it was that a Chat message can exceed 64KB. It cannot:
// the API refuses a body over roughly 32,000 bytes, bisected against a real
// space on 2026-08-17. The claim is worth holding anyway, because the failure
// it guards against is silent. A bufio.Scanner that meets a line longer than
// its buffer stops returning lines, and a loop that only ranges over Scan()
// cannot tell that from the end of the file.
//
// 200KB here rather than 32KB, so that the test exercises the buffer this
// package actually sets rather than the one the standard library defaults to.
func TestAMessageLargerThanTheScannerDefaultRoundTrips(t *testing.T) {
	index := NewNDJSON(t.TempDir())
	body := strings.Repeat("x", 200*1024)

	if err := index.Append(context.Background(), testSpace, []rows.Message{
		{Name: testSpace + "/messages/BIG", CreateTime: at(0), Text: body},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	found := collect(t, index, Query{Space: testSpace})
	if len(found) != 1 {
		t.Fatalf("a %d byte message came back as %d records", len(body), len(found))
	}
	if found[0].Text != body {
		t.Errorf("the body came back %d bytes long, want %d", len(found[0].Text), len(body))
	}
}

// TestALineTooLongIsAFailureRatherThanAnEndOfFile.
//
// This is the half of the defence that actually holds, and it is separate from
// the one above on purpose. The buffer makes an over-long line unlikely; this
// makes a silent short answer impossible. Without the scanner.Err() check the
// search below would return the first record and report success, which is a
// partial answer that looks whole.
func TestALineTooLongIsAFailureRatherThanAnEndOfFile(t *testing.T) {
	dir := t.TempDir()
	index := NewNDJSON(dir)

	if err := index.Append(context.Background(), testSpace, []rows.Message{
		{Name: testSpace + "/messages/AAA", CreateTime: at(0), Text: "findable"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// A line longer than the buffer, written past this package rather than
	// through it, which is the only way one can arise.
	path := filepath.Join(dir, "spaces", "AAAATestSpace.ndjson")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(strings.Repeat("x", maxLine+1) + "\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()

	var failed error
	for _, err := range index.Search(context.Background(), Query{Space: testSpace}) {
		if err != nil {
			failed = err
		}
	}
	if failed == nil {
		t.Fatal("a line past the buffer was reported as the end of the file")
	}
	if !strings.Contains(failed.Error(), "longer than") {
		t.Errorf("the failure does not say what happened: %v", failed)
	}
}

// TestTwoConcurrentAppendersProduceNoInterleavedLine, the card's second claim.
//
// Two `tail` processes on the same space is a real scenario, and a torn record
// is not recoverable: the message it carried may since have been deleted, and
// the API will not answer for it a second time.
//
// Goroutines with their own file handles rather than subprocesses. Each Append
// opens the file itself, so the write goes through the same kernel path a
// second process would take, and a test that shells out would be testing the
// shell as much as the code.
func TestTwoConcurrentAppendersProduceNoInterleavedLine(t *testing.T) {
	dir := t.TempDir()
	const writers, each = 8, 25

	// Large records, because a small write is atomic almost by accident and
	// would let this pass without the lock.
	body := strings.Repeat("y", 96*1024)

	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			index := NewNDJSON(dir)
			for i := range each {
				err := index.Append(context.Background(), testSpace, []rows.Message{{
					Name:       fmt.Sprintf("%s/messages/W%dN%d", testSpace, w, i),
					CreateTime: at(i),
					Text:       body,
				}})
				if err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	// Every line has to be complete JSON with the body intact. A torn write
	// shows up as a decode failure or as a short body, and Search skips a line
	// that will not decode, so the file is read directly here.
	path := filepath.Join(dir, "spaces", "AAAATestSpace.ndjson")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != writers*each {
		t.Fatalf("%d lines for %d appends", len(lines), writers*each)
	}
	for i, line := range lines {
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("line %d is not a complete record, so a write was interleaved: %v", i, err)
		}
		if len(r.Message.Text) != len(body) {
			t.Fatalf("line %d carries %d bytes of body, want %d", i, len(r.Message.Text), len(body))
		}
	}
}

// TestLastSeenAfterATornAppendReturnsTheLastCompleteRecord, the card's third
// claim.
//
// A process killed mid-append leaves a partial final line. Refusing to read the
// whole space because of it would make a crash cost more than it already did,
// so the torn record is skipped and everything before it still answers.
func TestLastSeenAfterATornAppendReturnsTheLastCompleteRecord(t *testing.T) {
	dir := t.TempDir()
	index := NewNDJSON(dir)

	if err := index.Append(context.Background(), testSpace, []rows.Message{
		{Name: testSpace + "/messages/AAA", CreateTime: at(0), Text: "first"},
		{Name: testSpace + "/messages/BBB", CreateTime: at(5), Text: "second"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	path := filepath.Join(dir, "spaces", "AAAATestSpace.ndjson")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Half a record, with no newline, which is what a kill mid-write leaves.
	if _, err := f.WriteString(`{"space":"` + testSpace + `","message":{"name":"` + testSpace + `/messages/CC`); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()

	when, name, err := index.LastSeen(context.Background(), testSpace)
	if err != nil {
		t.Fatalf("LastSeen: %v", err)
	}
	if name != testSpace+"/messages/BBB" {
		t.Errorf("LastSeen returned %q, want the last complete record", name)
	}
	if got := when.Format(time.RFC3339Nano); got != at(5) {
		t.Errorf("LastSeen time = %s, want %s", got, at(5))
	}
}

// TestAnEditIsAnsweredWithTheTextItHasNowAndADeleteNotAtAll.
//
// The card's third recon question, decided. The log is append-only, so a later
// record for the same message supersedes an earlier one and a tombstone
// suppresses it entirely. Without this a search answers with words nobody can
// find in the space any more, which is the whole reason to index.
func TestAnEditIsAnsweredWithTheTextItHasNowAndADeleteNotAtAll(t *testing.T) {
	index := NewNDJSON(t.TempDir())
	ctx := context.Background()

	edited := testSpace + "/messages/AAA"
	removed := testSpace + "/messages/BBB"

	if err := index.Append(ctx, testSpace, []rows.Message{
		{Name: edited, CreateTime: at(0), Text: "the original words"},
		{Name: removed, CreateTime: at(1), Text: "this one goes away"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := index.Append(ctx, testSpace, []rows.Message{
		{Name: edited, CreateTime: at(0), LastUpdateTime: at(9), Text: "the replacement words"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := index.Delete(ctx, testSpace, removed); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if found := collect(t, index, Query{Space: testSpace, Text: "original"}); len(found) != 0 {
		t.Errorf("the superseded text is still searchable: %+v", found)
	}
	if found := collect(t, index, Query{Space: testSpace, Text: "replacement"}); len(found) != 1 {
		t.Errorf("the edited text is not searchable: %+v", found)
	}
	if found := collect(t, index, Query{Space: testSpace, Text: "goes away"}); len(found) != 0 {
		t.Errorf("a deleted message is still searchable: %+v", found)
	}

	// And the edited message appears once, not twice.
	if found := collect(t, index, Query{Space: testSpace}); len(found) != 1 {
		t.Errorf("the space has %d messages, want 1", len(found))
	}
}

// TestASearchIsNewestFirstAndHonoursItsBounds.
func TestASearchIsNewestFirstAndHonoursItsBounds(t *testing.T) {
	index := NewNDJSON(t.TempDir())
	ctx := context.Background()

	var msgs []rows.Message
	for i := range 5 {
		msgs = append(msgs, rows.Message{
			Name:       fmt.Sprintf("%s/messages/M%d", testSpace, i),
			CreateTime: at(i * 10),
			Text:       fmt.Sprintf("message %d", i),
		})
	}
	if err := index.Append(ctx, testSpace, msgs); err != nil {
		t.Fatalf("Append: %v", err)
	}

	found := collect(t, index, Query{Space: testSpace})
	if len(found) != 5 {
		t.Fatalf("found %d", len(found))
	}
	if found[0].Name != testSpace+"/messages/M4" {
		t.Errorf("first result is %q, want the newest", found[0].Name)
	}

	// Both ends exclusive, matching what `messages list` does.
	windowed := collect(t, index, Query{
		Space: testSpace,
		Since: time.Date(2026, 8, 17, 9, 10, 0, 0, time.UTC),
		Until: time.Date(2026, 8, 17, 9, 40, 0, 0, time.UTC),
	})
	if len(windowed) != 2 {
		t.Errorf("the window returned %d messages, want 2 (20 and 30)", len(windowed))
	}

	if limited := collect(t, index, Query{Space: testSpace, Limit: 2}); len(limited) != 2 {
		t.Errorf("--limit 2 returned %d", len(limited))
	}
}

// TestASpaceNameThatIsNotOneNeverReachesAPath.
//
// The space name becomes a filename, so it is checked here rather than trusted,
// on the same grounds NewCache checks a profile name: what is on the other side
// is a create and an append.
func TestASpaceNameThatIsNotOneNeverReachesAPath(t *testing.T) {
	dir := t.TempDir()
	index := NewNDJSON(dir)

	for _, bad := range []string{
		"spaces/../../etc/passwd",
		"spaces/a/b",
		"../outside",
		"spaces/",
		"",
	} {
		if err := index.Append(context.Background(), bad, []rows.Message{{Name: "x"}}); err == nil {
			t.Errorf("Append accepted the space name %q", bad)
		}
	}

	// And nothing was created anywhere.
	if entries, err := os.ReadDir(dir); err == nil && len(entries) != 0 {
		t.Errorf("a refused name still created %v", entries)
	}
}

// FuzzAnIndexPathStaysUnderItsRoot states the same guarantee the resolver's
// cache states, because this package derives a path the same way.
//
// For any string, either the space name is refused or the file is a direct
// child of the index's own spaces directory. A list of characters somebody
// thought of is not the claim; this is.
func FuzzAnIndexPathStaysUnderItsRoot(f *testing.F) {
	f.Add("spaces/AAAATestSpace")
	f.Add("spaces/../../etc/passwd")
	f.Add("spaces/..")
	f.Add("spaces/a/b")
	f.Add("spaces/a\\b")
	f.Add("spaces/")
	f.Add("")

	root := f.TempDir()
	index := NewNDJSON(root)
	want := filepath.Join(root, "spaces")

	f.Fuzz(func(t *testing.T, space string) {
		path, err := index.path(space)
		if err != nil {
			if path != "" {
				t.Errorf("a refused space name still produced %q", path)
			}
			return
		}

		if dir := filepath.Dir(path); dir != want {
			t.Errorf("path(%q) = %q, whose directory is %q and not %q", space, path, dir, want)
		}
		if base := filepath.Base(path); base != filepath.Clean(base) || strings.ContainsAny(base, `/\`) {
			t.Errorf("path(%q) has the filename %q, which is not one name", space, base)
		}
	})
}

// TestASearchWithNoSpaceReadsEverySpaceIndexed.
//
// An empty Query.Space is how somebody asks "when did anybody say that", which
// is the question this whole milestone exists for: there is no message search
// API for an ordinary user, so across-all-spaces is the answer or there is none.
func TestASearchWithNoSpaceReadsEverySpaceIndexed(t *testing.T) {
	dir := t.TempDir()
	index := NewNDJSON(dir)
	ctx := context.Background()

	other := "spaces/AAAAOtherSpace"
	if err := index.Append(ctx, testSpace, []rows.Message{
		{Name: testSpace + "/messages/A", CreateTime: at(0), Text: "deploy finished here"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := index.Append(ctx, other, []rows.Message{
		{Name: other + "/messages/B", CreateTime: at(9), Text: "deploy finished there"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	all := collect(t, index, Query{Text: "deploy"})
	if len(all) != 2 {
		t.Fatalf("a search across every space found %d, want 2", len(all))
	}
	// Newest first still holds across files, not only within one.
	if all[0].Name != other+"/messages/B" {
		t.Errorf("first result is %q, want the newest across both spaces", all[0].Name)
	}

	if one := collect(t, index, Query{Space: testSpace, Text: "deploy"}); len(one) != 1 {
		t.Errorf("a search in one space found %d, want 1", len(one))
	}
}

// TestAnEmptyIndexAnswersRatherThanFailing.
//
// Nothing has been synced yet is the state every user starts in, so it is a
// normal answer and not an error. LastSeen returning a zero time is what tells
// a sync to start from the beginning.
func TestAnEmptyIndexAnswersRatherThanFailing(t *testing.T) {
	index := NewNDJSON(t.TempDir())
	ctx := context.Background()

	if found := collect(t, index, Query{}); len(found) != 0 {
		t.Errorf("an empty index returned %d messages", len(found))
	}
	if found := collect(t, index, Query{Space: testSpace}); len(found) != 0 {
		t.Errorf("an unindexed space returned %d messages", len(found))
	}

	when, name, err := index.LastSeen(ctx, testSpace)
	if err != nil {
		t.Fatalf("LastSeen on an empty index: %v", err)
	}
	if !when.IsZero() || name != "" {
		t.Errorf("LastSeen on an empty index = %s, %q, want the zero values", when, name)
	}

	// Appending nothing is not an error either, and creates no file: a sync
	// that found no new messages must not leave a directory behind.
	if err := index.Append(ctx, testSpace, nil); err != nil {
		t.Errorf("Append with no messages: %v", err)
	}
}

// TestADeleteRefusesAMessageNameThatIsNotOne.
func TestADeleteRefusesAMessageNameThatIsNotOne(t *testing.T) {
	index := NewNDJSON(t.TempDir())
	for _, bad := range []string{"", "spaces/AAA", "spaces/AAA/messages/..", "nonsense"} {
		if err := index.Delete(context.Background(), testSpace, bad); err == nil {
			t.Errorf("Delete accepted the message name %q", bad)
		}
	}
}

// TestASpaceWithNoMessagesStillCountsAsLookedAt.
//
// "Never synced" and "synced and empty" are different facts, and the difference
// reaches a user: `search` names the spaces it did not look in, so without this
// a space that genuinely has nothing in it is reported as missing from the
// index and somebody is told to run the sync they just ran.
//
// Found by the m6-99 sweep running §18 row 6 against a real account, where two
// of six spaces were empty and the search warned about one of them.
func TestASpaceWithNoMessagesStillCountsAsLookedAt(t *testing.T) {
	index := NewNDJSON(t.TempDir())
	ctx := context.Background()

	if spaces, err := index.Spaces(); err != nil || len(spaces) != 0 {
		t.Fatalf("a fresh index holds %v, %v", spaces, err)
	}

	if err := index.Visit(testSpace); err != nil {
		t.Fatalf("Visit: %v", err)
	}

	spaces, err := index.Spaces()
	if err != nil {
		t.Fatalf("Spaces: %v", err)
	}
	if len(spaces) != 1 || spaces[0] != testSpace {
		t.Errorf("after Visit the index holds %v, want just %s", spaces, testSpace)
	}

	// And it is still empty, rather than holding a phantom record.
	if found := collect(t, index, Query{Space: testSpace}); len(found) != 0 {
		t.Errorf("Visit invented %d messages", len(found))
	}
	if _, _, count, err := index.Bounds(ctx, testSpace); err != nil || count != 0 {
		t.Errorf("Bounds after Visit = %d, %v, want 0", count, err)
	}

	// Visiting twice is not an error and does not truncate what is there.
	if err := index.Append(ctx, testSpace, []rows.Message{
		{Name: testSpace + "/messages/AAA", CreateTime: at(0), Text: "hello"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := index.Visit(testSpace); err != nil {
		t.Fatalf("second Visit: %v", err)
	}
	if found := collect(t, index, Query{Space: testSpace}); len(found) != 1 {
		t.Errorf("a second Visit left %d messages, want 1", len(found))
	}

	// A name that is not a space is refused here too.
	if err := index.Visit("../escape"); err == nil {
		t.Error("Visit accepted a name that is not a space")
	}
}
