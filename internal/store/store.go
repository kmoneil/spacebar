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

// Package store keeps a local copy of what a space said (SPEC.md §12).
//
// It exists because there is no message search API for an ordinary user.
// `spaces.search` is admin-only and searches spaces rather than messages, so a
// local index is the only way to answer "when did somebody say that", and this
// milestone is that rather than a convenience on top of one.
//
// The index is also the only place a deleted message survives. The API will not
// answer for one a second time, which is why this lives under DataDir rather
// than CacheDir: a cache can be thrown away and rebuilt, and this cannot.
package store

import (
	"strings"
	"time"

	"github.com/kmoneil/spacebar/internal/rows"
)

// Query is what to look for.
type Query struct {
	// Space narrows the search to one space. Empty searches every space that
	// has been indexed.
	Space string

	// Text is matched case-folded against the message body. Empty matches
	// every message, which is how a caller asks for a whole space back.
	Text string

	// Since and Until bound the create time. Zero means unbounded. Both ends
	// are exclusive, matching what `messages list` does, because the API is
	// exclusive at both ends and two different meanings for one word is worse
	// than either meaning.
	Since time.Time
	Until time.Time

	// Limit is how many to return. Zero or less means every one.
	Limit int
}

// matches reports whether a record satisfies the query.
//
// Case folding is strings.Contains over strings.ToLower rather than a regexp,
// because this is a substring search over one person's own history and a
// regexp would be a second syntax somebody has to know. m6-02 owns whatever
// query language this grows, if it grows one.
func (q Query) matches(m rows.Message, createdAt time.Time) bool {
	if q.Text != "" && !strings.Contains(strings.ToLower(m.Text), strings.ToLower(q.Text)) {
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
