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
	"fmt"
	"strings"
	"time"
)

// ParseWhen turns what somebody typed for --since or --until into a time.
//
// Exported and living here rather than in internal/cli for the reason
// CheckInterval does: the MCP server takes the same argument and must not grow
// a second copy of the rule, and a flag parsed one way in one adapter and
// another way in the other is a feature that works in one of them.
//
// Two forms and no others.
//
// RFC 3339 is what the API itself speaks, and an offset is accepted as well as
// Z, measured against the live API rather than assumed.
//
// A Go duration means how long ago, because `--since 1h` is what somebody
// writing a script or an agent driving this will reach for, and the
// alternative is making them compute a timestamp, which is arithmetic with a
// clock in it. It is subtracted from the now that is passed in rather than from
// time.Now, so that a caller with an injected clock keeps it and a test can say
// what "an hour ago" means.
//
// A bare date is refused. "2026-08-16" has no timezone, so honouring it means
// choosing one on somebody's behalf, and the two candidates are a day apart at
// the edges. Refusing names both forms it does take.
func ParseWhen(value string, now time.Time) (time.Time, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, whenErr(value)
	}

	if ago, err := time.ParseDuration(text); err == nil {
		if ago <= 0 {
			return time.Time{}, clientErr(
				"%q is not a length of time in the past.\n"+
					"A duration says how long ago, so it has to be positive: %q, not %q.",
				value, "1h", "-1h")
		}
		return now.Add(-ago), nil
	}

	if at, err := time.Parse(time.RFC3339, text); err == nil {
		return at, nil
	}
	return time.Time{}, whenErr(value)
}

// whenErr is the one refusal, so the two ways of getting it wrong produce the
// same sentence.
func whenErr(value string) error {
	return clientErr("%q is not a time.\n"+
		"Give an RFC 3339 timestamp, as in %q or %q, or how long ago, as in %q, %q or %q.\n"+
		"There is no day unit in a duration, so a week is %q.",
		value,
		"2026-08-16T09:00:00Z", "2026-08-16T09:00:00-05:00",
		"90m", "2h", "36h",
		"168h")
}

// messageFilter is the filter a message list actually sends.
//
// The window is expressed as createTime clauses because that is what the
// endpoint takes, and both comparisons are strict: > and < are accepted and >=
// is answered with 400 "Invalid filter query", measured on 2026-08-16.
//
// A caller's own filter is parenthesized before anything is anded onto it. This
// tool does not parse that expression, so it cannot know whether the top-level
// operator is an OR, and without the parentheses an AND would bind tighter and
// quietly mean something else. Parentheses, AND, and a thread.name clause
// beside a createTime one were all measured against the live API on the same
// day.
//
// With no window asked for, the caller's filter is passed through byte for
// byte, exactly as it was before this existed. A request that adds nothing
// should look like it added nothing, in a dry run as much as on the wire.
func messageFilter(req ListMessagesRequest) string {
	if req.Since.IsZero() && req.Until.IsZero() {
		return req.Filter
	}

	clauses := make([]string, 0, 3)
	if req.Filter != "" {
		clauses = append(clauses, "("+req.Filter+")")
	}
	if !req.Since.IsZero() {
		clauses = append(clauses, fmt.Sprintf("createTime > %q", wireTime(req.Since)))
	}
	if !req.Until.IsZero() {
		clauses = append(clauses, fmt.Sprintf("createTime < %q", wireTime(req.Until)))
	}
	return strings.Join(clauses, " AND ")
}

// wireTime is how a time is written into a filter.
//
// UTC, matching what the tail loop already sends, so that one representation
// appears in a dry run whatever the caller typed. That is a change of
// representation and not of value: an offset and its UTC equivalent name the
// same instant, and the API accepts both.
func wireTime(at time.Time) string {
	return at.UTC().Format(time.RFC3339Nano)
}
