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
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
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

	// mu guards warnings. `search` over MCP is served concurrently, which is
	// the same reason internal/auth's store carries one.
	mu       sync.Mutex
	warnings []string
}

// NewNDJSON opens the index rooted at dir.
func NewNDJSON(dir string) *NDJSON { return &NDJSON{root: dir} }

// Warnings is what the caller has to print, once, deduplicated.
//
// Returned rather than printed, for the reason internal/auth returns its own:
// only internal/output writes to a process stream, so a warning built here and
// printed there is escaped by the one package that knows how.
//
// There is one warning today and it is about a record this package refused to
// answer with. Refusing silently would be the failure `search` exists not to
// have: an index is the only copy of a message that no longer exists anywhere
// else, so a message it holds and will not return is worth a sentence.
func (s *NDJSON) Warnings() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.warnings...)
}

func (s *NDJSON) warn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)

	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.warnings, msg) {
		s.warnings = append(s.warnings, msg)
	}
}

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

// Visit records that a space has been looked at, whether or not it said
// anything.
//
// "Never synced" and "synced and empty" are different facts and the difference
// is visible to a user: `search` names the spaces it did not look in, and
// without this a space that genuinely has no messages is reported as missing
// from the index, telling somebody to run the sync they just ran. Found by the
// m6-99 sweep running §18 row 6 against a real account, where two of six spaces
// were empty.
//
// An empty file rather than a marker of its own, so that everything reading the
// index keeps reading one kind of thing.
func (s *NDJSON) Visit(space string) error {
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
	return f.Close()
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
	latest, when, err := s.gather(ctx, space)
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}

	// One pass, and no ordering. This went through resolve until 2026-08-20,
	// which materialises a rows.Message for every surviving record and sorts
	// the slice into a total order, so that the two ends could be read off it.
	// A minimum, a maximum and a count need none of that.
	//
	// Measured on the reference machine, interleaved one sample per process:
	// at 100,000 records one call was 593ms to 639ms and 153.9MB against 472ms
	// to 480ms and 130.7MB, over six pairs with no overlap. The 23.2MB is the
	// slice, at 232 bytes per rows.Message. Allocation counts are identical to
	// within ten in 1.5 million, which is the number bench_test.go says to
	// argue from, so the slice is the honest claim and the clock is the
	// consequence.
	//
	// It matters because sync asks three times per space per run, and because
	// nothing was measuring it: make bench covers Search and the match loop,
	// and this is the one place a full index scan is overhead on the request
	// rather than the request itself. BenchmarkBounds exists so the next change
	// here has a number to move.
	//
	// A record whose create time will not parse is counted and does not bound
	// anything. That is what the old shape did, by sorting it to the zero time
	// and then skipping past it from the end, and it is preserved deliberately:
	// a message with an unreadable timestamp should not drag the window sync
	// resumes from back to the year zero.
	for name, r := range latest {
		if r.Deleted {
			continue
		}
		count++

		at := when[name]
		if at.After(newest) {
			newest = at
		}
		if at.IsZero() {
			continue
		}
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	return oldest, newest, count, nil
}

// Spaces is every space the index holds something for.
//
// Read off the directory rather than the API, because the whole point of the
// index is that it answers with no network at all. `search` uses it to say
// which spaces it looked in.
func (s *NDJSON) Spaces() ([]string, error) {
	found, err := s.files("")
	if err != nil {
		return nil, err
	}

	spaces := make([]string, 0, len(found))
	for _, file := range found {
		spaces = append(spaces, file.space)
	}
	sort.Strings(spaces)
	return spaces, nil
}

// Coverage is what a search over this index would and would not look in.
//
// searched is every space the index holds something for. missing is the ones in
// known that it does not, which is what a caller has to say out loud: a search
// that quietly skipped an unsynced space answers a narrower question than the
// one that was asked, and nothing in a list of results says so.
//
// known is passed in rather than discovered, because where it comes from is the
// caller's business and neither answer belongs here. Today both adapters read
// it from the resolver's cached space list, so the comparison costs no request;
// a caller that has no such list passes nothing and gets an empty missing,
// which is not the same fact as "nothing is missing" and is why the caller is
// told how many it compared against.
//
// Here rather than in either adapter, because both need it and only one had it.
// The CLI named the unsynced spaces on stderr and search_messages said nothing
// at all, so the same question asked over MCP was answered narrowly with
// nothing to say it was narrow, at the one consumer that reports its answer to
// a person as fact.
func (s *NDJSON) Coverage(known []string) (searched, missing []string, err error) {
	searched, err = s.Spaces()
	if err != nil {
		return nil, nil, err
	}

	for _, space := range known {
		if !slices.Contains(searched, space) {
			missing = append(missing, space)
		}
	}
	sort.Strings(missing)
	return searched, missing, nil
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

// gather reads every file a query touches into the newest-record-per-name maps.
//
// Split out of resolve so that Bounds can stop going through it. The two want
// different things from the same scan: resolve wants an ordering, and Bounds
// wants a minimum, a maximum and a count, which is one pass and no slice.
func (s *NDJSON) gather(ctx context.Context, space string) (map[string]record, map[string]time.Time, error) {
	files, err := s.files(space)
	if err != nil {
		return nil, nil, err
	}

	latest := map[string]record{}
	when := map[string]time.Time{}
	for _, file := range files {
		if err := s.scan(ctx, file, latest, when); err != nil {
			return nil, nil, err
		}
	}
	return latest, when, nil
}

// resolve reads every file the query touches and answers with the surviving
// messages, newest first.
//
// Split from Search for the complexity ceiling, and the split is where the two
// jobs already were: this one decides what the log says, and Search decides how
// much of it the caller asked for.
func (s *NDJSON) resolve(ctx context.Context, q Query) ([]rows.Message, error) {
	latest, when, err := s.gather(ctx, q.Space)
	if err != nil {
		return nil, err
	}

	found := make([]rows.Message, 0, len(latest))
	for _, r := range latest {
		if !r.Deleted && q.matches(r.Message, when[r.Message.Name]) {
			found = append(found, r.Message)
		}
	}
	// Create time first, and the resource name to break a tie, which makes this
	// a total order rather than an almost-total one.
	//
	// Without the second clause this was not deterministic, and the reason is
	// two steps away from the sort. found is built by ranging latest, which is
	// a map, so the runtime randomizes the order the records arrive in; then
	// sort.Slice is not stable and a comparator that answers false in both
	// directions leaves tied records wherever the map put them. Measured: six
	// records sharing a createTime came back in six different orders over fifty
	// runs, and four records in four orders over forty. Those are the numbers
	// TestASearchOrdersTiedCreateTimesTheSameWayEveryRun is sized against.
	//
	// That is worse than untidy. --limit cuts the sorted list, so with more
	// ties than the limit at the boundary, two runs of one query over an
	// unchanged index return different messages rather than the same messages
	// in a different order. The output shape is a public API here and the
	// golden files record it, so an order nothing can pin is a contract nobody
	// can hold.
	//
	// Ties are ordinary rather than exotic. createTime has microsecond
	// resolution, and this index also holds files that were restored, copied
	// between machines, or written by a bulk import, which is the same
	// provenance the foreign-record check exists for.
	//
	// The name is the tiebreaker because latest is keyed by it, so exactly one
	// record carries each one and the pair can never tie. Descending, to match
	// the direction of the key it is breaking a tie in; the direction is
	// otherwise arbitrary, because a message id is opaque and says nothing
	// about time.
	//
	// sort.SliceStable instead would not fix this. It would make the sort
	// stable with respect to an input order that is itself randomized.
	sort.Slice(found, func(i, j int) bool {
		at, other := when[found[i].Name], when[found[j].Name]
		if at.Equal(other) {
			return found[i].Name > found[j].Name
		}
		return at.After(other)
	})
	return found, nil
}

// belongs reports whether a record was read from the file that holds its space.
//
// Both halves of a record say where it came from, and they have to agree with
// the file as well as with each other: the Space field, which Append writes,
// and the space inside the message's own resource name, which the API assigned.
// The file name is the one with checked provenance, because path derives it
// from a space that has been through chat.CheckSpaceName.
//
// A record that does not belong is not a hypothetical. It arrives from a
// restored backup, a file copied between machines, or a directory somebody
// tidied by hand, which is exactly why every line carries its own space: the
// comment on that field says a line "that has been copied, concatenated, or
// recovered from a backup should still say what it is". Nothing read it until
// now.
//
// What it cost is more than a wrong row in a search. `--space` selects a file
// rather than filtering, so a foreign record answered a search scoped to a
// space it was never in. And Bounds reads the same file to decide where `sync`
// resumes, so a foreign record with a later timestamp moves the watermark
// forward and the next sync silently skips every real message before it. That
// is the truncation rule broken by a stray line.
func belongs(r record, space string) bool {
	if r.Space != space {
		return false
	}
	within, err := chat.SpaceOfMessage(r.Message.Name)
	return err == nil && within == space
}

// scan reads one file into the newest-record-per-name map.
func (s *NDJSON) scan(ctx context.Context, file indexed, latest map[string]record, when map[string]time.Time) error {
	path := file.path

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return storeErr("cannot read %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	foreign := 0
	defer func() {
		if foreign > 0 {
			s.warn("%s holds %d record(s) belonging to another space, which were not searched.\n"+
				"Nothing here writes one, so the file has been copied, restored, or edited. "+
				"They are skipped rather than answered with, because a record read from the wrong "+
				"file would answer for a space it was never in and would move where a sync resumes.",
				path, foreign)
		}
	}()

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
		if absorb(line, file.space, latest, when) {
			foreign++
		}
	}

	return scanErr(scanner, path)
}

// absorb reads one line into the newest-record-per-name maps, and reports
// whether it was skipped for belonging to another space.
//
// Split from scan for the complexity ceiling, and the split is where the two
// jobs already were: scan opens a file and decides what a failure to read it
// means, and this decides what one line of it is worth.
//
// The two skips it does not count are not the same as the one it does. A line
// that will not decode, or that names no message, is damage this cannot
// describe; a line that belongs to another space is intact and says so, which
// is why that one is worth telling somebody about.
func absorb(line []byte, space string, latest map[string]record, when map[string]time.Time) (foreign bool) {
	var r record
	if err := json.Unmarshal(line, &r); err != nil {
		// A record that will not decode is skipped rather than fatal. The
		// likeliest cause is a process killed mid-append leaving a partial
		// last line, and refusing to search a whole space because of one
		// torn record would make a crash cost more than it already did.
		return false
	}
	if r.Message.Name == "" {
		return false
	}
	if !belongs(r, space) {
		return true
	}

	// Every record is kept, matching or not. Filtering here was the first
	// shape of this and it was wrong: an edit whose new text does not match
	// the query would never supersede the old record that did, so a search
	// for a word somebody removed would still find it. Supersession is a
	// fact about the log and the query is a question asked afterwards.
	latest[r.Message.Name] = r
	when[r.Message.Name] = parseTimeOr(r.Message.CreateTime)
	return false
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

// indexed is one space and the file this index keeps it in.
//
// The pair travels together because a file's name is what says which space it
// holds, and scan needs that to tell a record that belongs here from one that
// does not. Reading the space back off each record instead would be trusting
// the thing being checked.
type indexed struct {
	space string
	path  string
}

// files is the set of files a query has to read.
//
// The unscoped case is the one that was wrong. It took every *.ndjson in the
// directory, while Spaces ran chat.CheckSpaceName over the same listing and
// skipped what did not pass, so a search read files it would not name: a stray
// file was searched and its records answered, and the count on stderr said one
// space when two files had been opened. A search that reports its own coverage
// has to be right about it in both directions, and the honest half was the only
// one anybody had written.
//
// One filter now, in one place, and Spaces is a projection of this.
func (s *NDJSON) files(space string) ([]indexed, error) {
	if space != "" {
		path, err := s.path(space)
		if err != nil {
			return nil, err
		}
		return []indexed{{space: space, path: path}}, nil
	}

	dir := filepath.Join(s.root, "spaces")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, storeErr("cannot list %s: %v", dir, err)
	}

	var found []indexed
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		name := "spaces/" + strings.TrimSuffix(e.Name(), ".ndjson")
		if err := chat.CheckSpaceName(name); err != nil {
			// A file somebody dropped in the directory by hand. Skipped rather
			// than fatal: it is not this tool's file, and refusing to search
			// because of it would make a stray file cost a search.
			continue
		}
		found = append(found, indexed{space: name, path: filepath.Join(dir, e.Name())})
	}
	return found, nil
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
