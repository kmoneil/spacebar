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
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/spacebar/internal/output"
)

// maxPageSize is the largest page any of these endpoints will return.
//
// The API documents 1000 for all three lists and clamps a larger request down
// rather than refusing it. Asking for exactly the cap would therefore work
// either way; it is written as a constant so that a future endpoint with a
// smaller cap has somewhere to disagree rather than silently returning short
// pages that read like the end of the list.
const maxPageSize = 1000

// The orderings messages.list accepts.
//
// Newest first is this tool's default and the reason is in m3-04: a caller who
// runs `messages list` with no flags and gets the default limit wants the latest
// messages, so the limit has to cut from the recent end. Reading a conversation
// in the order it happened is what `tail` is for, and it can buffer a bounded
// window to do it.
//
// UNVERIFIED, and it is the one guess in this file. That orderBy accepts
// "createTime DESC" comes from the API reference and cannot be probed without a
// credential, because every path answers 401 before it parses a query. If the
// API turns out to accept only ASC, the fix is to fetch ASC and reverse a
// bounded window, and the streaming property of this command changes with it.
const (
	OrderNewestFirst = "createTime DESC"
	OrderOldestFirst = "createTime ASC"
)

// ListSpacesRequest asks for the spaces this profile can reach.
type ListSpacesRequest struct {
	// Filter is the API's own filter expression, passed through unaltered. This
	// tool does not build one yet and does not parse one: inventing a syntax
	// here would mean translating it back, and the translation is where a
	// silently wrong filter would live.
	Filter string

	// Limit is how many spaces the caller wants. Zero or less means every one,
	// fetched a page at a time.
	Limit int
}

// ListMessagesRequest asks for messages in one space.
type ListMessagesRequest struct {
	// Space is spaces/AAA. Checked before it reaches a path.
	Space string

	Filter string

	// Since and Until bound the window, and both are exclusive.
	//
	// Exclusive because the API says so rather than because it reads better:
	// createTime accepts > and <, and >= is answered with 400 "Invalid filter
	// query", measured on 2026-08-16. A --since that meant "at or after" would
	// have to subtract a nanosecond from what the caller typed, which is a value
	// altered to make it representable, so the flag means strictly after and the
	// help says so.
	//
	// Zero means unbounded on that side. They compose with Filter rather than
	// replacing it: this endpoint's filter takes createTime and thread.name and
	// nothing else, so a caller who passes both is filtering by thread and by
	// time, which is one question.
	Since time.Time
	Until time.Time

	// OrderBy is one of the constants above. Empty means OrderNewestFirst,
	// filled in here rather than left to the API, whose own default is oldest
	// first and would make a default limit return the start of the space's
	// history.
	OrderBy string

	// ShowDeleted includes tombstones for deleted messages.
	ShowDeleted bool

	// Limit is how many messages the caller wants. Zero or less means every one.
	Limit int
}

// ListMembersRequest asks who is in one space.
type ListMembersRequest struct {
	Space string

	// ShowInvited includes people who have been invited and have not joined.
	//
	// The API returns joined memberships only unless this is set, so a State
	// column can otherwise never say anything but JOINED. Off by default because
	// that is the API's own default and because an invited person is not in the
	// space yet, and the flag that turns it on is what makes the difference
	// visible rather than silent.
	ShowInvited bool

	// ShowGroups includes memberships held by a Google Group rather than by a
	// person.
	//
	// A space can grant access to a group, and everybody in that group is then in
	// the space without a membership of their own. Without this the answer to
	// "who can see what I post here" omits them silently.
	ShowGroups bool

	// Limit is how many memberships the caller wants. Zero or less means all.
	Limit int
}

// Spaces lists the spaces this profile can reach (SPEC.md §7.3).
func (c *Client) Spaces(ctx context.Context, req ListSpacesRequest) iter.Seq2[Space, error] {
	query := url.Values{}
	setIf(query, "filter", req.Filter)

	return paginate(ctx, c, pager[Space]{
		path:  "spaces",
		query: query,
		limit: req.Limit,
		decode: func(payload []byte) ([]Space, string, error) {
			var body struct {
				Spaces        []Space `json:"spaces"`
				NextPageToken string  `json:"nextPageToken"`
			}
			err := json.Unmarshal(payload, &body)
			return body.Spaces, body.NextPageToken, err
		},
	})
}

// Messages lists messages in one space (SPEC.md §7.3).
func (c *Client) Messages(ctx context.Context, req ListMessagesRequest) iter.Seq2[Message, error] {
	if err := CheckSpaceName(req.Space); err != nil {
		return failed[Message](err)
	}

	order := req.OrderBy
	if order == "" {
		order = OrderNewestFirst
	}

	query := url.Values{}
	query.Set("orderBy", order)
	setIf(query, "filter", messageFilter(req))
	if req.ShowDeleted {
		query.Set("showDeleted", "true")
	}

	return paginate(ctx, c, pager[Message]{
		path:  req.Space + "/messages",
		query: query,
		limit: req.Limit,
		decode: func(payload []byte) ([]Message, string, error) {
			var body struct {
				Messages      []Message `json:"messages"`
				NextPageToken string    `json:"nextPageToken"`
			}
			err := json.Unmarshal(payload, &body)
			return body.Messages, body.NextPageToken, err
		},
	})
}

// Members lists the memberships of one space (SPEC.md §7.3).
func (c *Client) Members(ctx context.Context, req ListMembersRequest) iter.Seq2[Membership, error] {
	if err := CheckSpaceName(req.Space); err != nil {
		return failed[Membership](err)
	}

	query := url.Values{}
	if req.ShowInvited {
		query.Set("showInvited", "true")
	}
	if req.ShowGroups {
		query.Set("showGroups", "true")
	}

	return paginate(ctx, c, pager[Membership]{
		path:  req.Space + "/members",
		query: query,
		limit: req.Limit,
		decode: func(payload []byte) ([]Membership, string, error) {
			var body struct {
				Memberships   []Membership `json:"memberships"`
				NextPageToken string       `json:"nextPageToken"`
			}
			err := json.Unmarshal(payload, &body)
			return body.Memberships, body.NextPageToken, err
		},
	})
}

// GetSpace reads one space (SPEC.md §7.3).
func (c *Client) GetSpace(ctx context.Context, space string) (*Space, error) {
	if err := CheckSpaceName(space); err != nil {
		return nil, err
	}
	return fetch[Space](ctx, c, space)
}

// GetMessage reads one message by its resource name (SPEC.md §7.3).
func (c *Client) GetMessage(ctx context.Context, message string) (*Message, error) {
	if err := CheckMessageName(message); err != nil {
		return nil, err
	}
	return fetch[Message](ctx, c, message)
}

// fetch is one GET of one resource.
//
// Generic because the three lines that differ between reading a space and
// reading a message are the type and the path, and a second copy of the decode
// error handling is a second place for it to be worded differently.
func fetch[T any](ctx context.Context, c *Client, path string) (*T, error) {
	payload, err := c.do(ctx, Request{Method: http.MethodGet, Path: path})
	if err != nil {
		return nil, err
	}

	var out T
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, c.wrapTransport(fmt.Errorf("the response for %s could not be read: %w", path, err))
	}
	return &out, nil
}

// pager is one paginated endpoint.
type pager[T any] struct {
	// path is relative to the base, like every other request path.
	path string

	// query is everything except pageSize and pageToken, which this file owns.
	query url.Values

	// limit is how many items the caller wants in total. Zero or less means all.
	limit int

	// decode reads one page: its items and the token for the next one.
	decode func([]byte) ([]T, string, error)
}

// paginate walks an endpoint's pages and yields its items.
//
// An iterator rather than a page or a slice, and the choice is load-bearing
// rather than stylistic.
//
// Returning one page makes every caller responsible for the loop, and a
// forgotten loop is exactly the failure the invariant forbids: fifty messages
// read as the whole conversation, decided on, and reported as a success.
// Returning every page as a slice cannot stream, and buys an allocation whose
// size is chosen by a server the operator may not control.
//
// So pages are fetched as the caller ranges. The first item reaches the caller
// while later pages are still unfetched, which is what makes `--json` genuinely
// NDJSON rather than a document that happens to be printed a line at a time, and
// a caller who wants fifty stops at fifty rather than trimming a thousand.
//
// The error is the second half of the pair rather than a return value, so a
// failure on page four arrives at the caller consuming page four, in order, and
// a loop that ignores it had to write the blank identifier to do so.
func paginate[T any](ctx context.Context, c *Client, p pager[T]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T

		seen := 0
		token := ""
		for {
			items, next, err := p.fetch(ctx, c, token, seen)
			if err != nil {
				yield(zero, err)
				return
			}
			if p.emit(items, &seen, yield) {
				return
			}

			if next == "" {
				return
			}

			// A token identical to the one just used is a server that would keep
			// answering forever. It cannot happen against the real API and it
			// costs one comparison to make it impossible here, because the
			// alternative is a command that never returns and a quota spent
			// finding out.
			//
			// Stopping is right and stopping silently is not. Every other way a
			// walk ends early is either the caller's own doing or an error that
			// exits non-zero, so a caller checking the exit code cannot be
			// misled by any of them. This one ends short, with no error, at exit
			// zero, which is a truncated result reported as complete: this
			// repository's own defence producing the failure the defence exists
			// to prevent. So it yields one.
			if next == token {
				yield(zero, errTruncated(p.path))
				return
			}
			token = next
		}
	}
}

// fetch gets one page and decodes it.
func (p pager[T]) fetch(ctx context.Context, c *Client, token string, seen int) ([]T, string, error) {
	query := url.Values{}
	maps.Copy(query, p.query)
	if token != "" {
		query.Set("pageToken", token)
	}
	query.Set("pageSize", strconv.Itoa(p.pageSize(seen)))

	payload, err := c.do(ctx, Request{Method: http.MethodGet, Path: p.path, Query: query})
	if err != nil {
		return nil, "", err
	}

	items, next, err := p.decode(payload)
	if err != nil {
		return nil, "", c.wrapTransport(fmt.Errorf("a page of %s could not be read: %w", p.path, err))
	}
	return items, next, nil
}

// emit yields one page's items and reports whether the walk is over.
//
// Two ways for it to be over, and they are not the same thing. The caller broke
// out of its range, which is its business and not a failure, or the limit was
// reached, which is this function's business. Both stop, neither is an error,
// and seen is a pointer because it counts across pages rather than within one.
func (p pager[T]) emit(items []T, seen *int, yield func(T, error) bool) bool {
	for _, item := range items {
		if !yield(item, nil) {
			return true
		}
		*seen++
		if p.limit > 0 && *seen >= p.limit {
			return true
		}
	}
	return false
}

// pageSize is how many items to ask for next, given how many have been yielded.
//
// Asking for only what is still wanted is what keeps a `--limit 3` from
// fetching a thousand messages and throwing away 997 of them, on a per-space
// quota shared with every other app acting in that space.
func (p pager[T]) pageSize(seen int) int {
	if p.limit <= 0 {
		return maxPageSize
	}
	if remaining := p.limit - seen; remaining < maxPageSize {
		return remaining
	}
	return maxPageSize
}

// errTruncated reports a walk that stopped early through no fault of the
// caller's.
//
// An error rather than a warning, because the exit code is what a script checks
// and this is the one truncation that would otherwise be indistinguishable from
// success. The rows already written stay: they were real, and a partial answer
// with a non-zero exit is honest where a partial answer with a zero exit is not.
func errTruncated(path string) error {
	return &output.Error{
		Code: "TRUNCATED",
		Exit: output.ExitAPI,
		Message: fmt.Sprintf(
			"the list of %s stopped early: the server kept handing back the same page token, "+
				"so the results above are incomplete.\n"+
				"Nothing was wrong with the request. Try again, and if it repeats, the far end is not paginating.",
			path),
		Err: ErrTruncated,
	}
}

// failed is an iterator that yields one error and stops.
//
// It exists so that a name this tool refuses is reported through the same
// channel as a name the API refuses. The alternative is a second return value on
// every list method, which every caller would have to check before starting the
// range, and which one of them eventually would not.
func failed[T any](err error) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		yield(zero, err)
	}
}

// userName is a user reference: users/ and either a numeric ID or an email.
//
// The email half is measured rather than assumed. Probed against the live API
// on 2026-08-16, spaces:findDirectMessage answers 404 NOT_FOUND for
// users/someone@example.com and 400 INVALID_ARGUMENT for users/garbage, so an
// address is parsed as a user reference and a 404 means "understood, no direct
// message" rather than "could not read that".
//
// The character set is what an address can contain, minus what would change a
// request path. No slash, so a second path segment cannot be added; no percent,
// so no encoding of one can be either. An address that needs a character
// outside this set is refused rather than escaped, which is the same rule the
// space and message names follow.
var userName = regexp.MustCompile(`^users/[A-Za-z0-9][A-Za-z0-9_.+=~-]*(@[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+)?$`)

// CheckUserName refuses anything that is not a user resource name.
//
// The same rule and the same reason as CheckSpaceName: this value becomes a
// query value on a request path, and what the pattern accepts has to be safe
// there. It is a query value rather than a path segment, so the encoder would
// escape it, and that is the second layer rather than the only one.
func CheckUserName(user string) error {
	if user == "" {
		return clientErr("no user was given.")
	}
	if !userName.MatchString(user) {
		return clientErr("%q is not a user name.\nA user is %q followed by an address or a numeric ID, as in %q.",
			user, "users/", "users/someone@example.com")
	}
	return nil
}

// FindDirectMessage returns the direct message space shared with one user
// (SPEC.md §7.3).
//
// The user is a resource name rather than a bare address, because that is what
// the API takes and translating here would mean a second place that knows the
// shape. internal/resolve is what turns "someone@example.com" into
// "users/someone@example.com".
//
// A 404 is left as a 404 rather than turned into an empty result. "There is no
// direct message with this person yet" and "this lookup failed" are different
// answers, and a caller that got nil for both would have to guess which.
func (c *Client) FindDirectMessage(ctx context.Context, user string) (*Space, error) {
	if err := CheckUserName(user); err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("name", user)

	payload, err := c.do(ctx, Request{
		Method: http.MethodGet,
		Path:   "spaces:findDirectMessage",
		Query:  query,
	})
	if err != nil {
		return nil, err
	}

	var space Space
	if err := json.Unmarshal(payload, &space); err != nil {
		return nil, c.wrapTransport(fmt.Errorf("the response for a direct message lookup could not be read: %w", err))
	}
	return &space, nil
}

// The values a caller may ask for, as words rather than as the API's own
// strings: "createTime DESC" is a shell-quoting problem and an implementation
// detail of an endpoint this tool exists to hide.
const (
	OrderNewest = "newest"
	OrderOldest = "oldest"
)

// OrderBy turns what a caller asked for into the API's ordering, refusing
// anything else.
//
// Here rather than in an adapter because both adapters take the argument, and a
// second copy of a two-case switch is how two of them end up disagreeing about
// what "oldest" means.
//
// Refused rather than passed through, because a typo that reached the API would
// come back as an INVALID_ARGUMENT naming a field the caller never typed, and a
// value silently ignored would return the opposite order with a success code.
func OrderBy(order string) (string, error) {
	switch strings.ToLower(order) {
	case OrderNewest:
		return OrderNewestFirst, nil
	case OrderOldest:
		return OrderOldestFirst, nil
	}
	return "", clientErr("the order is %q, and it takes %q or %q.", order, OrderNewest, OrderOldest)
}

// The event types spaceEvents.list accepts, as the API spells them.
//
// Written out rather than built from parts, because a name assembled from a
// prefix and a verb is a name nobody can grep for, and because the set is not
// regular: a space has updated and nothing else, a membership has three.
const (
	EventMessageCreated    = "google.workspace.chat.message.v1.created"
	EventMessageUpdated    = "google.workspace.chat.message.v1.updated"
	EventMessageDeleted    = "google.workspace.chat.message.v1.deleted"
	EventReactionCreated   = "google.workspace.chat.reaction.v1.created"
	EventReactionDeleted   = "google.workspace.chat.reaction.v1.deleted"
	EventMembershipCreated = "google.workspace.chat.membership.v1.created"
	EventMembershipUpdated = "google.workspace.chat.membership.v1.updated"
	EventMembershipDeleted = "google.workspace.chat.membership.v1.deleted"
	EventSpaceUpdated      = "google.workspace.chat.space.v1.updated"
)

// SpaceEventsRequest asks what has happened in one space.
type SpaceEventsRequest struct {
	Space string

	// Types is the event_types the filter asks for. Empty is refused rather
	// than defaulted here: what a caller who said nothing wants is a decision
	// for the command, and a default invented in the client would be one the
	// command could not see.
	Types []string

	// Filter replaces the built one entirely, for a caller who wants a filter
	// this tool does not build. It carries its own event_types, because the
	// endpoint requires them and this does not add to what somebody wrote.
	Filter string

	// Since bounds the window. The API takes start_time in the same filter and
	// requires it to be in the past.
	Since time.Time

	Limit int
}

// SpaceEvents lists what has happened in a space (SPEC.md §7.3).
//
// The filter is not optional and is not the caller's to forget. Without one the
// endpoint answers 400 "Missing event types", which names neither the parameter
// nor the values it takes, so the request is built here and the refusal for an
// empty set happens before it.
func (c *Client) SpaceEvents(ctx context.Context, req SpaceEventsRequest) iter.Seq2[SpaceEvent, error] {
	if err := CheckSpaceName(req.Space); err != nil {
		return failed[SpaceEvent](err)
	}
	filter, err := eventFilter(req)
	if err != nil {
		return failed[SpaceEvent](err)
	}

	query := url.Values{}
	query.Set("filter", filter)

	return paginate(ctx, c, pager[SpaceEvent]{
		path:  req.Space + "/spaceEvents",
		query: query,
		limit: req.Limit,
		decode: func(payload []byte) ([]SpaceEvent, string, error) {
			var body struct {
				SpaceEvents   []SpaceEvent `json:"spaceEvents"`
				NextPageToken string       `json:"nextPageToken"`
			}
			err := json.Unmarshal(payload, &body)
			return body.SpaceEvents, body.NextPageToken, err
		},
	})
}

// eventFilter builds the filter the endpoint requires.
//
// The syntax is `event_types:"a" OR event_types:"b"`, with start_time anded on
// when there is one. Quoted with %q so a value cannot close the string it is
// in, and the values are this tool's own constants rather than a caller's, so
// the quoting is the second layer rather than the only one.
func eventFilter(req SpaceEventsRequest) (string, error) {
	if req.Filter != "" {
		return req.Filter, nil
	}
	if len(req.Types) == 0 {
		return "", clientErr("no event types were asked for, and this endpoint requires at least one.")
	}

	terms := make([]string, 0, len(req.Types))
	for _, kind := range req.Types {
		if kind == "" {
			return "", clientErr("an event type is empty.")
		}
		terms = append(terms, fmt.Sprintf("event_types:%q", kind))
	}

	filter := strings.Join(terms, " OR ")
	if !req.Since.IsZero() {
		// start_time takes an equals and means "from here onwards", which is
		// not what an equals usually means and is not what messages.list does
		// with createTime, where the comparison is > and < and >= is refused.
		// Measured on 2026-08-16: start_time > is a 400 "Error parsing the
		// filter", start_time = returns everything after that instant.
		//
		// What that measurement did not settle is the boundary: whether an
		// event whose eventTime is exactly the start_time is in the answer.
		// Watch has to be right either way and is, without knowing. See seenAt.
		//
		// The OR group is parenthesized because it has to be. The same filter
		// without the brackets is the same 400, so the brackets are load
		// bearing rather than tidy.
		filter = fmt.Sprintf("(%s) AND start_time = %q", filter, wireTime(req.Since))
	}
	return filter, nil
}
