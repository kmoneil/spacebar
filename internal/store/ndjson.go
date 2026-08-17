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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/rows"
)

// maxLine is the scanner buffer, and the number is measured rather than
// guessed.
//
// The Chat API refuses a message body over roughly 32,000 bytes: bisected on
// 2026-08-17 against a real space, where 32,000 ASCII characters were accepted
// and 32,100 were answered `400 INVALID_ARGUMENT, "Message content is too
// long"`, and 7,900 emoji at 31,617 bytes were accepted where 8,100 at 32,417
// were not. Both boundaries land at the same byte count, so the cap is on the
// encoded size and not on a character count.
//
// So a record is about 32KB of text plus a little metadata plus an attachment
// list at roughly 120 bytes each, and the 64KB default would in fact hold every
// line this API can produce today. This is raised anyway, to 8x the measured
// maximum, because a 2x margin on a limit somebody else owns and can raise is
// not a margin.
//
// The buffer is the smaller half of the defence. See scanErr.
const maxLine = 256 * 1024

// NDJSON is one file per space under DataDir (SPEC.md §12.1).
type NDJSON struct {
	root string
}

// NewNDJSON opens the index rooted at dir.
func NewNDJSON(dir string) *NDJSON { return &NDJSON{root: dir} }

// record is one line of the log.
//
// It wraps rows.Message rather than replacing it, so that what a search returns
// is the same object every other command publishes and there is no second shape
// to keep in step. The two fields beside it are the log's own.
type record struct {
	// Space is repeated on every line even though the file is per space,
	// because a line that has been copied, concatenated, or recovered from a
	// backup should still say what it is.
	Space string `json:"space"`

	// Deleted marks a tombstone. An edit is recorded as an ordinary record with
	// the new text; a delete cannot be, because there is no new text, and a log
	// that simply stopped mentioning a message would answer a search with words
	// nobody can find in the space any more.
	Deleted bool `json:"deleted,omitempty"`

	Message rows.Message `json:"message"`
}

func (s *NDJSON) path(space string) (string, error) {
	// The space name reaches a filename, so it is checked here rather than
	// trusted, on the same grounds NewCache checks a profile name: what is on
	// the other side is a create and an append, and a first layer that needs
	// the layer below it to be safe is not a first layer.
	if err := chat.CheckSpaceName(space); err != nil {
		return "", err
	}
	return filepath.Join(s.root, "spaces", strings.TrimPrefix(space, "spaces/")+".ndjson"), nil
}

// Append records messages for one space.
//
// Every record is written by a single Write under an exclusive advisory lock,
// which is what makes two `tail` processes on the same space safe. The lock
// rather than O_APPEND alone: O_APPEND makes the seek and the write one
// operation, and says nothing about a large write being interleaved with
// somebody else's. A torn line is not recoverable, because the message it
// carried may since have been deleted and the API will not answer for it again.
func (s *NDJSON) Append(ctx context.Context, space string, msgs []rows.Message) error {
	batch := make([]record, 0, len(msgs))
	for _, m := range msgs {
		batch = append(batch, record{Space: space, Message: m})
	}
	return s.write(ctx, space, batch)
}

// Delete records a tombstone, so a search stops answering with a message that
// is no longer in the space.
//
// A record rather than an edit of the file. Rewriting a line in place would
// mean holding the whole file, which is the shape that loses everything when a
// process dies half way, and this log is the only copy: the API will not answer
// for a deleted message a second time.
func (s *NDJSON) Delete(ctx context.Context, space, message string) error {
	if err := chat.CheckMessageName(message); err != nil {
		return err
	}
	return s.write(ctx, space, []record{{
		Space:   space,
		Deleted: true,
		Message: rows.Message{Name: message},
	}})
}

func (s *NDJSON) write(ctx context.Context, space string, records []record) error {
	if len(records) == 0 {
		return nil
	}
	path, err := s.path(space)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return storeErr("cannot create %s: %v", filepath.Dir(path), err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return storeErr("cannot open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	unlock, err := lockExclusive(f)
	if err != nil {
		return err
	}
	defer unlock()

	var batch []byte
	for _, r := range records {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := json.Marshal(r)
		if err != nil {
			return storeErr("cannot encode %s: %v", r.Message.Name, err)
		}
		batch = append(batch, line...)
		batch = append(batch, '\n')
	}

	if _, err := f.Write(batch); err != nil {
		return storeErr("cannot write to %s: %v", path, err)
	}
	return nil
}

// LastSeen is where a sync resumes from.
func (s *NDJSON) LastSeen(ctx context.Context, space string) (time.Time, string, error) {
	var newest time.Time
	var name string

	for m, err := range s.Search(ctx, Query{Space: space}) {
		if err != nil {
			return time.Time{}, "", err
		}
		at, ok := parseTime(m.CreateTime)
		if ok && at.After(newest) {
			newest, name = at, m.Name
		}
	}
	return newest, name, nil
}

// Bounds is the window a space's index covers, and how many messages are in it.
//
// This is what makes `sync` resumable without a cursor file of its own. The
// card that asked for this assumed one would be needed; it is not, because the
// index already knows what it holds. A catch-up fetches everything after
// newest, a backfill everything before oldest, both bounds are exclusive at the
// API as well as here, and an interrupted run resumes by asking the same
// question again.
//
// The cost of being stateless is one request per run to discover that there is
// nothing older, which is cheaper than a second file that can disagree with the
// first.
//
// Everything is zero when nothing has been indexed, which is how a sync knows
// to start from the beginning.
func (s *NDJSON) Bounds(ctx context.Context, space string) (oldest, newest time.Time, count int, err error) {
	found, err := s.resolve(ctx, Query{Space: space})
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}
	if len(found) == 0 {
		return time.Time{}, time.Time{}, 0, nil
	}

	// resolve answers newest first, so the ends are the ends. A record whose
	// create time would not parse sorts as the zero time, so oldest is read
	// from the last one that has a time rather than from the last one.
	newest = parseTimeOr(found[0].CreateTime)
	for i := len(found) - 1; i >= 0; i-- {
		if at := parseTimeOr(found[i].CreateTime); !at.IsZero() {
			oldest = at
			break
		}
	}
	return oldest, newest, len(found), nil
}

// Spaces is every space the index holds something for.
//
// Read off the directory rather than the API, because the whole point of the
// index is that it answers with no network at all. `search` uses it to say
// which spaces it looked in.
func (s *NDJSON) Spaces() ([]string, error) {
	paths, err := s.files("")
	if err != nil {
		return nil, err
	}

	spaces := make([]string, 0, len(paths))
	for _, path := range paths {
		id := strings.TrimSuffix(filepath.Base(path), ".ndjson")
		name := "spaces/" + id
		if err := chat.CheckSpaceName(name); err != nil {
			// A file somebody dropped in the directory by hand. Skipped rather
			// than fatal: it is not this tool's file, and refusing to search
			// because of it would make a stray file cost a search.
			continue
		}
		spaces = append(spaces, name)
	}
	sort.Strings(spaces)
	return spaces, nil
}

// Search yields every matching message, newest first.
//
// A message that was edited appears once, with the text it has now, and one
// that was deleted does not appear at all. That is why matches are resolved by
// name before anything is yielded rather than streamed straight out of the
// scanner: the log is append-only, so the record that supersedes an earlier one
// is later in the file, and a streaming answer would emit the stale text and
// then the fresh one.
//
// The memory this costs is one record per distinct message in the files read,
// not one per match, because supersession has to be resolved before the query
// is asked and a record that does not match may still be the one that
// supersedes a record that does. That is the trade this design makes: a full
// scan and a map the size of the space, for an index that never answers with
// text somebody removed.
func (s *NDJSON) Search(ctx context.Context, q Query) iter.Seq2[rows.Message, error] {
	return func(yield func(rows.Message, error) bool) {
		found, err := s.resolve(ctx, q)
		if err != nil {
			yield(rows.Message{}, err)
			return
		}

		for i, m := range found {
			if q.Limit > 0 && i >= q.Limit {
				return
			}
			if !yield(m, nil) {
				return
			}
		}
	}
}

// resolve reads every file the query touches and answers with the surviving
// messages, newest first.
//
// Split from Search for the complexity ceiling, and the split is where the two
// jobs already were: this one decides what the log says, and Search decides how
// much of it the caller asked for.
func (s *NDJSON) resolve(ctx context.Context, q Query) ([]rows.Message, error) {
	files, err := s.files(q.Space)
	if err != nil {
		return nil, err
	}

	latest := map[string]record{}
	when := map[string]time.Time{}
	for _, path := range files {
		if err := s.scan(ctx, path, latest, when); err != nil {
			return nil, err
		}
	}

	found := make([]rows.Message, 0, len(latest))
	for _, r := range latest {
		if !r.Deleted && q.matches(r.Message, when[r.Message.Name]) {
			found = append(found, r.Message)
		}
	}
	sort.Slice(found, func(i, j int) bool {
		return when[found[i].Name].After(when[found[j].Name])
	})
	return found, nil
}

// scan reads one file into the newest-record-per-name map.
func (s *NDJSON) scan(ctx context.Context, path string, latest map[string]record, when map[string]time.Time) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return storeErr("cannot read %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			// A record that will not decode is skipped rather than fatal. The
			// likeliest cause is a process killed mid-append leaving a partial
			// last line, and refusing to search a whole space because of one
			// torn record would make a crash cost more than it already did.
			continue
		}
		if r.Message.Name == "" {
			continue
		}

		// Every record is kept, matching or not. Filtering here was the first
		// shape of this and it was wrong: an edit whose new text does not match
		// the query would never supersede the old record that did, so a search
		// for a word somebody removed would still find it. Supersession is a
		// fact about the log and the query is a question asked afterwards.
		latest[r.Message.Name] = r
		when[r.Message.Name] = parseTimeOr(r.Message.CreateTime)
	}

	return scanErr(scanner, path)
}

// scanErr is the half of the long-line defence that actually holds.
//
// A bufio.Scanner that meets a line longer than its buffer stops returning
// lines and records ErrTooLong, and a loop that only ranges over Scan() sees
// exactly what it would see at a clean end of file. So a search would answer
// from the first half of a file and report success, which is this project's
// worst failure shape: a partial answer that looks whole.
//
// The buffer size makes that unlikely. Checking here is what makes it
// impossible, and it is why the size is the smaller half of the defence.
func scanErr(scanner *bufio.Scanner, path string) error {
	err := scanner.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, bufio.ErrTooLong) {
		return storeErr("%s has a line longer than %d bytes, so the rest of it was not searched.\n"+
			"Nothing here writes a line that long, so the file has been altered or corrupted.", path, maxLine)
	}
	return storeErr("cannot read %s: %v", path, err)
}

// files is the set of files a query has to read.
func (s *NDJSON) files(space string) ([]string, error) {
	if space != "" {
		path, err := s.path(space)
		if err != nil {
			return nil, err
		}
		return []string{path}, nil
	}

	dir := filepath.Join(s.root, "spaces")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, storeErr("cannot list %s: %v", dir, err)
	}

	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ndjson") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	return paths, nil
}

// parseTimeOr is parseTime for the places a zero time is the right answer for
// something unparseable, which is every place a record's own create time is
// read: the API always sends one, and a record without one sorts oldest rather
// than failing a search over a whole space.
func parseTimeOr(value string) time.Time {
	at, _ := parseTime(value)
	return at
}

func parseTime(value string) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339Nano, value)
	return at, err == nil
}

func storeErr(format string, a ...any) error {
	return output.Errorf("STORE", output.ExitError, format, a...)
}
