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
	"bytes"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
	"github.com/kmoneil/spacebar/internal/rows"
)

// rendering builds a renderer over buffers and returns both.
func rendering(t *testing.T, asJSON bool) (*output.Renderer, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	var out, errw bytes.Buffer
	return output.NewRenderer(&out, &errw, output.Options{JSON: asJSON}), &out, &errw
}

// items is an iterator over a fixed slice, optionally ending in a failure.
func items[T any](values []T, err error) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for _, v := range values {
			if !yield(v, nil) {
				return
			}
		}
		if err != nil {
			var zero T
			yield(zero, err)
		}
	}
}

// TestARowIsWrittenAsItArrivesRatherThanAtTheEnd.
//
// The streaming half of SPEC.md §11.2, asserted from the writer's side: the
// buffer holds the first row while the iterator is still producing later ones.
// A stream helper that collected everything first would pass every other test in
// this file and fail this one.
func TestARowIsWrittenAsItArrivesRatherThanAtTheEnd(t *testing.T) {
	r, out, _ := rendering(t, true)

	seen := 0
	source := func(yield func(chat.Space, error) bool) {
		for _, name := range []string{"spaces/AAA", "spaces/BBB", "spaces/CCC"} {
			if !yield(chat.Space{Name: name}, nil) {
				return
			}
			seen++

			// The assertion. By the time the second space is being produced, the
			// first is already written.
			if seen == 1 && !strings.Contains(out.String(), "spaces/AAA") {
				t.Errorf("after one row the output was %q, so nothing is streaming", out.String())
			}
		}
	}

	if err := stream(r, source, rows.ForSpace); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; lines != 3 {
		t.Errorf("wrote %d lines, want 3:\n%s", lines, out.String())
	}
}

// TestAFailurePartWayThroughAListKeepsTheRowsAlreadyWritten.
//
// This is the one place the "a failing command writes nothing to stdout" rule
// does not hold, and it is written down rather than left to be discovered.
//
// The rule exists because a partially written document followed by an error is
// worse than no document, since the first one parses. That reasoning is about a
// wrapped array. NDJSON is not a document: every line is a complete object, and
// there is no closing bracket whose absence a parser would notice.
//
// So what is guaranteed instead, and asserted here, is narrower and true: rows
// already written stay written, no row is ever half a line, and the failure
// arrives with a non-zero exit and a message on stderr. A caller that checks the
// exit code is never misled. The alternative is to buffer every page before
// writing anything, which would trade the streaming property for the appearance
// of atomicity and make `tail` impossible.
func TestAFailurePartWayThroughAListKeepsTheRowsAlreadyWritten(t *testing.T) {
	r, out, errw := rendering(t, true)

	boom := errors.New("the third page failed")
	err := stream(r, items([]chat.Space{
		{Name: "spaces/AAA"},
		{Name: "spaces/BBB"},
	}, boom), rows.ForSpace)

	if !errors.Is(err, boom) {
		t.Fatalf("stream returned %v, want the failure to reach the caller", err)
	}

	// Both rows are there, and both are whole. A truncated final line would be
	// the failure this test exists to rule out.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines, want the 2 that had already succeeded:\n%s", len(lines), out.String())
	}
	for i, line := range lines {
		var row rows.Space
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Errorf("line %d is not a complete object: %q", i, line)
		}
	}

	// Nothing about the failure went to stdout. That half of the rule still
	// holds absolutely: the error is the caller's to report, on stderr.
	if strings.Contains(out.String(), "failed") {
		t.Errorf("the failure reached stdout:\n%s", out.String())
	}
	if errw.Len() != 0 {
		t.Errorf("stream wrote to stderr itself rather than returning the error:\n%s", errw.String())
	}
}

// TestADryRunOfAReadReportsTheRequestRatherThanFailing.
//
// This shipped broken and was found by running the command, not by a test.
// --dry-run stops every request in the client, including a GET, so a read
// command that did not handle the answer reported "dry run: the request below
// was not sent" as a generic failure at exit 1. The command had worked
// perfectly right up to the point of describing what it had built.
//
// What the test pins is both halves: the request goes to stdout, and the error
// does not survive to become an exit code.
func TestADryRunOfAReadReportsTheRequestRatherThanFailing(t *testing.T) {
	r, out, errw := rendering(t, false)

	preview := &chat.Preview{
		DryRun:  true,
		Method:  "GET",
		URL:     "https://chat.googleapis.test/v1/spaces?pageSize=25",
		Headers: map[string]string{"Authorization": "REDACTED"},
	}

	if err := finish(r, &profile.Open{Name: "work"}, &chat.DryRun{Request: preview}); err != nil {
		t.Fatalf("a dry run was reported as a failure: %v", err)
	}

	if !strings.Contains(out.String(), "GET https://chat.googleapis.test/v1/spaces?pageSize=25") {
		t.Errorf("the request did not reach stdout:\n%s", out.String())
	}

	// The profile is on stderr, because stdout carries the request and nothing
	// else. Somebody diffing a dry run against the API reference is reading the
	// same shape in both places.
	if !strings.Contains(errw.String(), `"work"`) {
		t.Errorf("stderr does not name the profile:\n%s", errw.String())
	}
	if strings.Contains(out.String(), "work") {
		t.Errorf("the profile line reached stdout:\n%s", out.String())
	}
}

// TestAnOrdinaryFailurePassesThroughTheDryRunHandler, so that adding the
// handling did not quietly swallow every other error a read can produce.
func TestAnOrdinaryFailurePassesThroughTheDryRunHandler(t *testing.T) {
	r, out, _ := rendering(t, false)

	boom := errors.New("the space does not exist")
	if err := finish(r, &profile.Open{Name: "work"}, boom); !errors.Is(err, boom) {
		t.Errorf("finish returned %v, want the failure unchanged", err)
	}
	if out.Len() != 0 {
		t.Errorf("a failure wrote to stdout:\n%s", out.String())
	}
}

// TestOrderOnlyTakesWhatItDocuments.
//
// A value passed straight through would reach the API as an orderBy it does not
// know, coming back as an INVALID_ARGUMENT naming a field the caller never
// typed. Worse, a value the API ignores would return the opposite order with a
// success code.
func TestOrderOnlyTakesWhatItDocuments(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"newest", chat.OrderNewestFirst},
		{"oldest", chat.OrderOldestFirst},
		{"NEWEST", chat.OrderNewestFirst},
	} {
		got, err := orderByFor(tc.in)
		if err != nil {
			t.Errorf("orderByFor(%q) = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("orderByFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{"sideways", "createTime DESC", "", "desc"} {
		if _, err := orderByFor(bad); err == nil {
			t.Errorf("orderByFor(%q) was accepted", bad)
		}
	}
}
