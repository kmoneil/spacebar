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

package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/output"
)

// when is the clock these tests reckon "ago" against.
var when = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

// TestParseWhenTakesTwoFormsAndRefusesTheRest.
//
// The forms are the API's own timestamp and a duration meaning ago, and the
// pair is deliberate: RFC 3339 is what a filter carries, and `--since 1h` is
// what somebody writing a script reaches for. Everything else is refused with
// one sentence naming both, rather than half-understood.
func TestParseWhenTakesTwoFormsAndRefusesTheRest(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"2026-08-16T09:00:00Z", time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)},
		{"2026-08-16T09:00:00.5Z", time.Date(2026, 8, 16, 9, 0, 0, 500000000, time.UTC)},

		// An offset is accepted by the API and therefore here, and it names the
		// same instant as 14:00Z.
		{"2026-08-16T09:00:00-05:00", time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)},

		{"1h", when.Add(-time.Hour)},
		{"90m", when.Add(-90 * time.Minute)},
		{"36h", when.Add(-36 * time.Hour)},
		{"1h30m", when.Add(-90 * time.Minute)},

		// Whitespace is trimmed rather than refused: it is what a shell leaves
		// behind when a value is built up in a variable.
		{"  2h  ", when.Add(-2 * time.Hour)},
	} {
		got, err := ParseWhen(tc.in, when)
		if err != nil {
			t.Errorf("ParseWhen(%q) = %v", tc.in, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("ParseWhen(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{
		"",
		"   ",
		"yesterday",

		// No timezone, so honouring it means choosing one, and the candidates
		// are a day apart at the edges.
		"2026-08-16",

		// Go durations have no day unit, and this is the mistake somebody will
		// make. The refusal says so.
		"1d",
		"7d",

		// A duration says how long ago, so the past is the only direction.
		"-1h",
		"0s",

		"5",
		"09:00",
		"1754000000",
	} {
		if got, err := ParseWhen(bad, when); err == nil {
			t.Errorf("ParseWhen(%q) was accepted as %s", bad, got)
		}
	}
}

// TestARefusedTimeSaysBothFormsAndIsAUsageFailure, because the person reading
// it typed something and needs to know what to type instead.
func TestARefusedTimeSaysBothFormsAndIsAUsageFailure(t *testing.T) {
	_, err := ParseWhen("1d", when)
	if err == nil {
		t.Fatal("a day unit was accepted")
	}

	out, ok := errors.AsType[*output.Error](err)
	if !ok {
		t.Fatalf("the refusal is not an output.Error: %T", err)
	}
	if out.Exit != output.ExitUsage {
		t.Errorf("exit %d, want %d: a mistyped flag value is the caller's mistake", out.Exit, output.ExitUsage)
	}
	for _, want := range []string{"RFC 3339", "90m", "168h", "1d"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, out.Message)
		}
	}
}

// TestAWindowBecomesTheFilterTheAPIAccepts.
//
// Both comparisons are strict because the endpoint refuses >=, measured on
// 2026-08-16, and the caller's own expression is parenthesized because this
// tool does not parse it and cannot know whether its top-level operator binds
// looser than the AND being added.
func TestAWindowBecomesTheFilterTheAPIAccepts(t *testing.T) {
	since := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	until := time.Date(2026, 8, 16, 17, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name string
		req  ListMessagesRequest
		want string
	}{
		{
			// Byte for byte what it was before the window existed. A request
			// that adds nothing should look like it added nothing.
			name: "no window passes the filter through untouched",
			req:  ListMessagesRequest{Filter: `thread.name = "spaces/AAA/threads/T"`},
			want: `thread.name = "spaces/AAA/threads/T"`,
		},
		{
			name: "since alone",
			req:  ListMessagesRequest{Since: since},
			want: `createTime > "2026-08-16T09:00:00Z"`,
		},
		{
			name: "until alone",
			req:  ListMessagesRequest{Until: until},
			want: `createTime < "2026-08-16T17:00:00Z"`,
		},
		{
			name: "both ends",
			req:  ListMessagesRequest{Since: since, Until: until},
			want: `createTime > "2026-08-16T09:00:00Z" AND createTime < "2026-08-16T17:00:00Z"`,
		},
		{
			name: "a caller's filter keeps its meaning under an OR",
			req: ListMessagesRequest{
				Filter: `thread.name = "spaces/AAA/threads/T" OR thread.name = "spaces/AAA/threads/U"`,
				Since:  since,
			},
			want: `(thread.name = "spaces/AAA/threads/T" OR thread.name = "spaces/AAA/threads/U")` +
				` AND createTime > "2026-08-16T09:00:00Z"`,
		},
		{
			// An offset names the same instant as its UTC form, and one
			// representation on the wire means one representation in a dry run
			// whatever was typed.
			name: "an offset is written as the instant it names",
			req:  ListMessagesRequest{Since: since.In(time.FixedZone("CDT", -5*60*60))},
			want: `createTime > "2026-08-16T09:00:00Z"`,
		},
	} {
		if got := messageFilter(tc.req); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// TestTheWindowReachesTheRequestAndNothingElseDoes, because a filter built
// correctly and sent nowhere is the same as no filter at all.
func TestTheWindowReachesTheRequestAndNothingElseDoes(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"messages": []}`)
	})

	if _, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{
		Space: "spaces/AAA",
		Since: time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 8, 16, 17, 0, 0, 0, time.UTC),
	})); err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if _, err := collect(r.client.Messages(context.Background(), ListMessagesRequest{
		Space: "spaces/AAA",
	})); err != nil {
		t.Fatalf("Messages: %v", err)
	}

	paths := r.paths()
	if len(paths) != 2 {
		t.Fatalf("paths = %q", paths)
	}

	// url.Values escapes it, which is the second layer: the value is the
	// caller's and never reaches the path.
	for _, want := range []string{"createTime+%3E", "createTime+%3C", "2026-08-16T09%3A00%3A00Z"} {
		if !strings.Contains(paths[0], want) {
			t.Errorf("the window is not in the request:\n%s\nwant %s", paths[0], want)
		}
	}
	if strings.Contains(paths[1], "filter") {
		t.Errorf("a request with no window still carried a filter:\n%s", paths[1])
	}
}

// TestSinceAndBackfillAreRefusedBeforeAnyRequest.
//
// Counted rather than read out of the error, for the reason the dry-run tests
// count: a refusal that arrives after the fetch carries the same message as one
// that arrives before it, and only one of them is the claim.
func TestSinceAndBackfillAreRefusedBeforeAnyRequest(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"messages": []}`)
	})

	_, err := collect(r.client.Tail(context.Background(), TailRequest{
		Space:    "spaces/AAA",
		Since:    when,
		Backfill: 5,
	}))
	if err == nil {
		t.Fatal("--since and --backfill were accepted together")
	}
	if r.count() != 0 {
		t.Errorf("the refusal cost %d requests", r.count())
	}

	out, ok := errors.AsType[*output.Error](err)
	if !ok {
		t.Fatalf("the refusal is not an output.Error: %T", err)
	}
	if out.Exit != output.ExitUsage {
		t.Errorf("exit %d, want %d", out.Exit, output.ExitUsage)
	}

	// Each on its own is fine, which is what makes the refusal about the
	// combination rather than about either flag.
	if err := CheckTailWindow(when, 0); err != nil {
		t.Errorf("--since alone was refused: %v", err)
	}
	if err := CheckTailWindow(time.Time{}, 5); err != nil {
		t.Errorf("--backfill alone was refused: %v", err)
	}
}
