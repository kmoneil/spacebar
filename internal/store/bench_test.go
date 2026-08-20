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
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/rows"
)

// This file exists so that a performance argument about the index can be
// settled with a number instead of an opinion.
//
// It measures a warm index. The first iteration pulls the files into the page
// cache and every one after it reads them from there, which is deliberate: the
// question is what parsing, resolving and sorting cost, and a cold-cache number
// would mostly be a measurement of this machine's disk. A search of a space
// somebody has not touched today pays for the read on top of what is here.
//
// # What it measured, 2026-08-18
//
// linux/arm64, 12 cores, Go 1.26.6, in a container on a shared host. Six
// samples of each, one process, machine quiet:
//
//	one space,    10,000 records    48.4ms ±17%    4,839 ns/record    160k allocs
//	one space,   100,000 records   503.4ms ± 5%    5,034 ns/record   1.60M allocs
//	one space, 1,000,000 records    5.873s ± 8%    5,873 ns/record   16.0M allocs
//	50 spaces,   100,000 records   529.6ms ± 8%    5,296 ns/record   1.60M allocs
//
// So the curve is close to linear, at roughly five microseconds and sixteen
// allocations per record, bending upward by about a fifth at a million where
// the sort and the collector start to show. Fifty files cost about five per
// cent more than one holding the same records, which is at the edge of what
// this machine can resolve.
//
// Peak resident memory for one search, measured with /usr/bin/time -v rather
// than with -benchmem, because B/op is everything allocated and this is what
// has to fit at once: 122MiB at 100,000 records and 953MiB at 1,000,000. About
// a kilobyte of live memory per indexed message, which is the cost of the trade
// resolve's doc comment describes. A space of a million messages needs a
// gigabyte to search, and that is worth knowing before somebody syncs one.
//
// The README says "0.34s at 50,000 messages, crossing one second at roughly
// 175,000". Here that is 0.24s and about 199,000, so the claim is conservative
// on this machine rather than falsified. It stays as it is: replacing a number
// of unknown provenance with one measured inside a container on a shared host
// is not an improvement.
//
// # The noise floor, which is the number to read first
//
// Within one quiet run of six samples, benchstat reports ±3% to ±17% depending
// on the benchmark. Between two runs of the same binary ninety seconds apart,
// the same benchmarks moved by up to +100% at p=0.002, and the cause was
// visible in ps: another tenant's test binary at 112% of a core. The load
// average on this host went from 0.95 to 19 during one afternoon.
//
// A p-value says a difference is unlikely to be chance. It does not say the
// difference came from the code, and here it did not.
//
// So, on this machine: a wall-clock difference smaller than about 2x proves
// nothing when one variant is measured after the other, which is what -count
// does, since it runs every sample of one benchmark before starting the next.
// What works is interleaving, one sample per process:
//
//	for i in $(seq 1 10); do
//	  go test ./internal/store/ -run '^$' -bench 'TheMatchLoop/(mixed|query_folded)' -benchmem -count=1
//	done | benchstat /dev/stdin
//
// That brings the same pair to ±3% and makes a seven per cent difference
// legible. Allocation counts need none of this: across twelve samples in seven
// processes, at every load this host reached, they moved by 25 in 1.6 million,
// which benchstat rounds to ±0%. They are the metric to argue from.
//
// # What that settled about Query.matches
//
// Per record, from the match loop below. A body averages 110 bytes and 98.7%
// of them hold an upper-case letter, so nearly every one of them costs a fold,
// and the 117 B is what 110 becomes after the allocator rounds to a size class:
//
//	time bound only        13.7 ns    0 allocs
//	lower case query      435.1 ns    1 alloc    117 B
//	mixed case query      451.6 ns    2 allocs   125 B
//	query folded once     415.4 ns    1 alloc    117 B
//
// Folding the query once outside the loop is worth 31.5 ns and one 8-byte
// allocation per record, interleaved, ±3%, and it won all ten pairs. It is also
// 0.6% of a search, an order of magnitude under this machine's variance in a
// quiet window and two orders under what it does when it is not. And it is
// worth nothing at all to somebody who typed their query in lower case, which
// strings.ToLower returns unchanged without allocating.
//
// So it is measured, it is real, and it is not worth doing. The change is not
// free either: matches would take a pre-folded needle beside the Query that
// already holds one, which puts a caller in a position to pass the wrong one.
// The question is closed.
//
// The larger half of the same line is folding the body, and that is 421 of the
// 435 ns and the whole of the 117 B. Against a whole search, where a record
// costs about 5,000 ns, 1,650 B and 16 allocations, the body fold is 8% of the
// time, 7% of the bytes and one of the sixteen allocations. The other fifteen
// are decoding the line and filling the maps.
//
// So it is not worth doing either, and it is the more expensive half by a
// factor of thirteen. Removing it means writing a case-insensitive substring
// search, which is a Unicode correctness problem rather than a loop-invariant
// one, and 8% is not worth being wrong about a Turkish dotless i for.
//
// One thing this did turn up that is worth a card of its own rather than a
// paragraph here: resolve sizes its result slice at len(latest), so a query
// matching one per cent of a space still allocates a rows.Message for every
// record in it. That is 232 bytes each, about 232MB of the 953MB peak at a
// million records.

// sink is where a benchmark's result goes, so the compiler cannot delete the
// work that produced it.
var sink int

// needleWord is the rare word a search looks for, and it is capitalised while
// the query is not, so that a match only happens after case folding. That is
// what makes these benchmarks measure the fold rather than skip it.
const needleWord = "Rollback"

// vocabulary is what a message body is built from.
//
// Real words at real lengths, and mixed case, because strings.ToLower returns
// an all-lower ASCII string unchanged without allocating. A body of
// strings.Repeat("x", n) would fold for free and the benchmark would measure
// something the index never does.
var vocabulary = []string{
	"the", "a", "and", "for", "with", "on", "to", "in", "of", "is", "it",
	"deploy", "deployed", "shipped", "merged", "reverted", "staging", "prod",
	"build", "green", "red", "flake", "retry", "timeout", "latency", "quota",
	"Kevin", "Sam", "Priya", "Alex", "Jordan", "Chen",
	"PR", "CI", "API", "SLO", "OAuth", "JSON", "HTTP", "TLS",
	"Looks", "good", "thanks", "sure", "no", "yes", "maybe", "later",
	"can", "you", "check", "this", "one", "again", "before", "after",
	"Monday", "Tuesday", "Friday", "morning", "afternoon", "tomorrow",
	"released", "tagged", "rolled", "forward", "back", "out", "over",
}

// senders is who a message is from.
//
// The same six names are in the vocabulary as well, because people's names turn
// up in message bodies, and the repetition is deliberate: this was one slice
// and an index into the middle of the other, so adding a word to the vocabulary
// silently changed who every message was from and quietly moved every number
// this file records.
var senders = []string{"Kevin", "Sam", "Priya", "Alex", "Jordan", "Chen"}

// prng is a xorshift64, written out rather than taken from math/rand.
//
// The fixture has to be the same on every machine and in every Go release, and
// what has to be deterministic is the generator rather than the bytes: a
// million records is not a file this repository should carry. math/rand/v2's
// top-level functions make no such promise, so the eight lines are cheaper than
// the dependence on somebody else's sequence.
type prng struct{ state uint64 }

func newPRNG(seed uint64) *prng { return &prng{state: seed} }

func (p *prng) next() uint64 {
	p.state ^= p.state << 13
	p.state ^= p.state >> 7
	p.state ^= p.state << 17
	return p.state
}

func (p *prng) intn(n int) int { return int(p.next() % uint64(n)) }

// body is one message, eight to thirty-two words, with the needle in roughly
// one in a hundred.
func (p *prng) body() string {
	var out strings.Builder
	for i, words := 0, 8+p.intn(25); i < words; i++ {
		if i > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(vocabulary[p.intn(len(vocabulary))])
	}
	if p.intn(100) == 0 {
		out.WriteByte(' ')
		out.WriteString(needleWord)
	}
	return out.String()
}

// idAlphabet is the character set a Chat message identifier is drawn from. The
// API's own look base64url, and that alphabet contains - and _.
const idAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"

// messageID is a counter written in that alphabet, twice, separated by a dot,
// which is the shape the API returns: an identifier and the thread it opened.
// A counter rather than a random draw so that no two records collide, because
// two records under one name are one message and the fixture would be smaller
// than it says it is.
func messageID(n int) string {
	var out [11]byte
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = idAlphabet[n%len(idAlphabet)]
		n /= len(idAlphabet)
	}
	return string(out[:]) + "." + string(out[:])
}

func benchSpace(i int) string { return fmt.Sprintf("spaces/AAAA%07d", i) }

// fixtureKey is what makes two fixtures the same fixture.
type fixtureKey struct{ records, spaces int }

var (
	fixturesMu sync.Mutex
	fixtures   = map[fixtureKey]string{}
)

// fixture is a directory holding that many records, spread evenly over that
// many files, built once and reused.
//
// b.TempDir cannot hold this. It is removed when the benchmark that asked for
// it returns, and the runner calls a benchmark function more than once: once to
// find b.N and again to measure it, and again for every -count. A million
// records take longer to write than to search, so rebuilding would be most of
// what got measured.
func fixture(b *testing.B, records, spaces int) string {
	b.Helper()

	fixturesMu.Lock()
	defer fixturesMu.Unlock()

	key := fixtureKey{records: records, spaces: spaces}
	if dir, ok := fixtures[key]; ok {
		return dir
	}

	dir, err := os.MkdirTemp("", "bench-index-")
	if err != nil {
		b.Fatalf("cannot make a fixture directory: %v", err)
	}
	if err := buildFixture(dir, records, spaces); err != nil {
		b.Fatalf("cannot build a fixture of %d records: %v", records, err)
	}
	fixtures[key] = dir
	return dir
}

// buildFixture writes the records through Append, rather than writing the files
// directly, so that what is measured is the shape this package really produces.
func buildFixture(dir string, records, spaces int) error {
	index := NewNDJSON(dir)
	p := newPRNG(1)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := 0

	for space := range spaces {
		count := records / spaces
		if space < records%spaces {
			count++
		}
		name := benchSpace(space)

		// In batches, because Append encodes the whole slice into one buffer
		// before it writes, and a million of them at once is a fixture that
		// costs more memory than the search does.
		const batch = 5000
		for written := 0; written < count; written += batch {
			size := min(batch, count-written)
			msgs := make([]rows.Message, 0, size)
			for range size {
				at = at.Add(time.Duration(1+p.intn(5000)) * time.Microsecond)
				msgs = append(msgs, rows.Message{
					Name:        name + "/messages/" + messageID(id),
					CreateTime:  at.Format(time.RFC3339Nano),
					Sender:      fmt.Sprintf("users/%011d", p.intn(200)),
					DisplayName: senders[p.intn(len(senders))],
					Thread:      name + "/threads/" + messageID(id/8),
					Text:        p.body(),
				})
				id++
			}
			if err := index.Append(context.Background(), name, msgs); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeFixtures() {
	fixturesMu.Lock()
	defer fixturesMu.Unlock()
	for _, dir := range fixtures {
		_ = os.RemoveAll(dir)
	}
}

// TestMain is here to remove the benchmark fixtures, which outlive the
// benchmark that built them on purpose. See fixture.
func TestMain(m *testing.M) {
	code := m.Run()
	removeFixtures()
	os.Exit(code)
}

// BenchmarkSearchOverOneSpace is the cost of the whole path: read the file,
// decode every line, resolve supersession, ask the query, sort the survivors.
//
// Three sizes rather than one, because the interesting question is the shape of
// the curve. The ns/record metric is what says whether it is linear.
func BenchmarkSearchOverOneSpace(b *testing.B) {
	for _, records := range []int{10_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("%d", records), func(b *testing.B) {
			dir := fixture(b, records, 1)
			searchBench(b, dir, Query{Space: benchSpace(0), Text: "rollback"}, records)
		})
	}
}

// BenchmarkBounds is what sync pays to find out what it already holds.
//
// It exists because nothing measured this path. make bench covered Search and
// the match loop, and Bounds is the one place in the tree where a full index
// scan is overhead on the request rather than the request itself: syncOne asks
// three times per space per run, before anything reaches the network.
//
// What it measured, 2026-08-20, on the machine and by the protocol this file's
// header describes, one space, six interleaved pairs, one sample per process:
//
//	through resolve, with the sort   593ms to 639ms   153.9MB   1,501,105 allocs
//	one pass for min, max and count  472ms to 480ms   130.7MB   1,501,095 allocs
//
// No overlap across the six pairs. The allocation counts are identical to
// within ten in 1.5 million, so the honest claim is the 23.2MB, which is the
// rows.Message slice at 232 bytes each, and the clock is its consequence.
//
// Two sizes rather than three, because the shape of the curve is Search's
// question and this one only has to stay comparable to it.
func BenchmarkBounds(b *testing.B) {
	for _, records := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("%d", records), func(b *testing.B) {
			dir := fixture(b, records, 1)
			b.ReportAllocs()

			count := 0
			for b.Loop() {
				_, _, n, err := NewNDJSON(dir).Bounds(context.Background(), benchSpace(0))
				if err != nil {
					b.Fatalf("Bounds: %v", err)
				}
				count = n
			}
			if count != records {
				b.Fatalf("Bounds counted %d of %d records, so this measured the wrong thing", count, records)
			}
			sink = count
		})
	}
}

// BenchmarkAppend is what a sync pays to write a batch, and it exists because
// this change made that number bigger.
//
// batchFor validates every message on the way in, so that the index can never
// write a record its own reader calls foreign. That check is two regular
// expressions per message: CheckMessageName on the name, and CheckSpaceName on
// the space read back out of it, both inside chat.SpaceOfMessage. Append had no
// measurement at all before, so there was no number to move.
//
// What it measured, 2026-08-20, on the machine and by the protocol this file's
// header describes, six samples each, one batch of 500 at fetchInto's own batch
// size:
//
//	Append, whole call         656-722us    878KB    1,030 allocs
//	batchFor alone             301-318us    131KB        1 alloc
//	the wrapping loop it replaced   30-104us    131KB        1 alloc
//
// So the check adds about 0.53us a message: roughly 265us of a 690us Append,
// which is 53ms on a first sync of a hundred thousand messages and nothing at
// all on an incremental one. That is a ninth of a single Bounds scan, on a path
// that is otherwise network-bound.
//
// The first version of this comment carried numbers four times larger, taken
// while a fuzz sweep had ten workers on the same twelve cores. They are left
// out rather than kept as a second row, because a number measured against an
// unstated load is not a measurement of anything. The wrapping-loop row is the
// noisiest here for the same reason at a smaller scale: it is tens of
// microseconds, which is where this machine stops resolving.
//
// It is kept at that price rather than trimmed. Both halves of a record have to
// agree with the file it is in or the reader skips it, counts it, and warns
// that the file was copied or edited by hand, which would be untrue and would
// name no message. The obvious trim is to check the space once for the batch
// instead of once per message, and it is deliberately not taken: it turns a
// direct question, is this message in this space, into a prefix comparison that
// is only equivalent because a space name cannot itself contain "/messages/".
func BenchmarkAppend(b *testing.B) {
	batch := make([]rows.Message, 0, 500)
	for i := range cap(batch) {
		batch = append(batch, rows.Message{
			Name:       benchSpace(0) + "/messages/" + messageID(i),
			CreateTime: time.Date(2026, 8, 17, 9, 0, i, 0, time.UTC).Format(time.RFC3339Nano),
			Text:       "deploy done, and then enough words to make this a realistic body",
		})
	}

	ctx := context.Background()
	index := NewNDJSON(b.TempDir())
	b.ReportAllocs()

	for b.Loop() {
		if err := index.Append(ctx, benchSpace(0), batch); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
}

// BenchmarkSearchOverEverySpace is the unscoped search, which reads every file
// in the directory.
//
// The same hundred thousand records as the one-space case above, so the two are
// directly comparable and the difference between them is what fifty files cost
// over one.
func BenchmarkSearchOverEverySpace(b *testing.B) {
	const records, spaces = 100_000, 50

	dir := fixture(b, records, spaces)
	searchBench(b, dir, Query{Text: "rollback"}, records)
}

func searchBench(b *testing.B, dir string, q Query, records int) {
	b.Helper()
	b.ReportAllocs()

	// No ResetTimer: b.Loop resets the timer the first time it is called, which
	// is what a ResetTimer after the fixture would have been for.
	found, failed := 0, error(nil)
	for b.Loop() {
		found = 0
		for m, err := range NewNDJSON(dir).Search(context.Background(), q) {
			if err != nil {
				failed = err
				break
			}
			found++
			sink += len(m.Name)
		}
	}
	b.StopTimer()

	if failed != nil {
		b.Fatalf("Search: %v", failed)
	}
	if found == 0 {
		b.Fatal("the query matched nothing, so this measured a search that did no work")
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*records), "ns/record")
}

// benchMessages is the same bodies without the disk, for the match loop.
func benchMessages(n int) []rows.Message {
	p := newPRNG(1)
	msgs := make([]rows.Message, 0, n)
	for i := range n {
		msgs = append(msgs, rows.Message{
			Name: benchSpace(0) + "/messages/" + messageID(i),
			Text: p.body(),
		})
	}
	return msgs
}

// matchesPrefolded is Query.matches with the query folded once by the caller
// rather than once per record.
//
// It lives here rather than in the package because it is a measurement and not
// a proposal, and the measurement has been taken: 31.5 ns and one allocation
// per record, which is 0.6% of a search and below anything this machine can
// resolve. The header of this file has the numbers and the reason the answer is
// to leave Query.matches alone. It stays because the next person to ask will
// want to re-run it rather than take a date's word for it.
func matchesPrefolded(q Query, needle string, m rows.Message, createdAt time.Time) bool {
	if needle != "" && !strings.Contains(strings.ToLower(m.Text), needle) {
		return false
	}
	if !q.Since.IsZero() && !createdAt.After(q.Since) {
		return false
	}
	if !q.Until.IsZero() && !createdAt.Before(q.Until) {
		return false
	}
	return true
}

// BenchmarkTheMatchLoop decomposes what a match costs per record.
//
// Four cases, and the differences between them are the answers:
//
//	time bound only     the loop with no folding in it at all.
//	lower case query    what somebody typing in lower case pays. ToLower
//	                    returns an all-lower ASCII string unchanged without
//	                    allocating, so this is the body fold and a scan of
//	                    the query.
//	mixed case query    what Query.matches does today when the query holds an
//	                    upper-case letter, so both folds allocate.
//	query folded once   the same, with the query's fold hoisted out.
//
// Mixed case minus query folded once is what the loop-invariant fold costs.
// Either of those minus time bound only is what folding the body costs.
func BenchmarkTheMatchLoop(b *testing.B) {
	msgs := benchMessages(50_000)
	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Each case takes the whole slice and loops itself, which is the only way
	// the hoisted case can hoist anything: a closure called once per record
	// would fold the query once per record whatever it looked like.
	cases := []struct {
		name string
		run  func(msgs []rows.Message) int
	}{
		{"time_bound_only", func(msgs []rows.Message) int {
			q, hits := Query{Since: since}, 0
			for _, m := range msgs {
				if q.matches(m, createdAt) {
					hits++
				}
			}
			return hits
		}},
		{"lower_case_query", func(msgs []rows.Message) int {
			q, hits := Query{Since: since, Text: "rollback"}, 0
			for _, m := range msgs {
				if q.matches(m, createdAt) {
					hits++
				}
			}
			return hits
		}},
		{"mixed_case_query", func(msgs []rows.Message) int {
			q, hits := Query{Since: since, Text: needleWord}, 0
			for _, m := range msgs {
				if q.matches(m, createdAt) {
					hits++
				}
			}
			return hits
		}},
		{"query_folded_once", func(msgs []rows.Message) int {
			q, hits := Query{Since: since, Text: needleWord}, 0
			needle := strings.ToLower(q.Text)
			for _, m := range msgs {
				if matchesPrefolded(q, needle, m, createdAt) {
					hits++
				}
			}
			return hits
		}},
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			hits := 0
			for b.Loop() {
				hits = c.run(msgs)
			}
			b.StopTimer()

			sink += hits
			if hits == 0 {
				b.Fatal("nothing matched, so this measured a loop that did no work")
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(msgs)), "ns/record")
		})
	}
}
