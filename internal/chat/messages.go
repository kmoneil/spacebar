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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

// MessageIDPrefix is what the API requires a caller-supplied message ID to
// begin with.
//
// This is written down because the failure is unhelpful: an ID without the
// prefix is refused at the API with a message that does not mention the prefix,
// so somebody who strips it while tidying up gets an error that reads like a
// problem with their message rather than with their ID.
const MessageIDPrefix = "client-"

// messageIDHexLength is how much of the digest goes into a derived ID.
//
// Thirty-two hex characters is 128 bits, which is far more than enough to make
// an accidental collision between two different sends impossible in practice,
// and it keeps the whole ID inside the length the API allows.
const messageIDHexLength = 32

// SendRequest is one message to post.
type SendRequest struct {
	// Space is spaces/AAA. Empty for a webhook, whose URL already names the
	// space it posts to and which cannot post anywhere else.
	Space string

	// Message is the resource to create.
	Message Message

	// ThreadKey groups this message with others that share it. It is the only
	// threading a webhook has, since naming an existing thread requires reading
	// the space first.
	//
	// It travels in the body, as message.thread.threadKey, and not as the query
	// parameter of the same name that SPEC.md §7.3 lists. The query form is
	// marked deprecated in the API reference in favour of thread.thread_key,
	// and the webhook guide's own examples put it in the body. The spec is out
	// of date here rather than wrong about intent.
	ThreadKey string

	// MessageReplyOption decides what happens when ThreadKey matches no
	// existing thread. The API's own values, passed through rather than
	// translated, because a name invented here would have to be mapped back and
	// the mapping is where a wrong default hides.
	//
	// Left empty alongside a ThreadKey, this is filled in rather than sent
	// empty. See ReplyFallbackToNewThread.
	MessageReplyOption string

	// MessageID is the caller's own name for this message, and it is what makes
	// a send safe to retry. Supplying one turns a POST into a request the API
	// will refuse to carry out twice, which is why it is also what lets this
	// client replay one after an upstream error.
	MessageID string

	// RequestID is the API's other de-duplication mechanism, kept distinct
	// because it does not become part of the message's resource name.
	RequestID string
}

// SendMessage posts a message (SPEC.md §7.3).
func (c *Client) SendMessage(ctx context.Context, req SendRequest) (*Message, error) {
	query := url.Values{}
	setIf(query, "messageReplyOption", replyOption(req))
	setIf(query, "messageId", req.MessageID)
	setIf(query, "requestId", req.RequestID)

	body := req.Message
	if req.ThreadKey != "" {
		// Merged rather than overwritten, so that a caller who set a thread by
		// name keeps it. The key is what a webhook has instead of a name.
		thread := Thread{}
		if body.Thread != nil {
			thread = *body.Thread
		}
		thread.ThreadKey = req.ThreadKey
		body.Thread = &thread
	}

	payload, err := c.do(ctx, Request{
		Method: http.MethodPost,
		Path:   messagesPath(req.Space),
		Query:  query,
		Body:   body,

		// The whole of the no-replay rule, in one field. A POST carrying a
		// message ID cannot produce a second message however many times it is
		// sent, because the API refuses the duplicate. A POST without one can,
		// so a 503 on it ends the operation rather than being retried, and the
		// caller is told rather than guessed for.
		Idempotent: req.MessageID != "",
	})
	if err != nil {
		return nil, err
	}

	var sent Message
	if err := json.Unmarshal(payload, &sent); err != nil {
		return nil, c.wrapTransport(fmt.Errorf("the message was accepted but the response could not be read: %w", err))
	}
	return &sent, nil
}

// The values messageReplyOption takes, from the API reference.
const (
	// ReplyFallbackToNewThread replies to the thread the key names, and starts
	// a new one when there is no such thread.
	ReplyFallbackToNewThread = "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"

	// ReplyOrFail replies to the thread the key names, or returns NOT_FOUND.
	// For a caller who would rather be told than be somewhere unexpected.
	ReplyOrFail = "REPLY_MESSAGE_OR_FAIL"
)

// replyOption is the messageReplyOption to send.
//
// The default is filled in whenever a thread key is present, and this is the
// most important line in the file. The API's own default,
// MESSAGE_REPLY_OPTION_UNSPECIFIED, is documented as "Starts a new thread.
// Using this option ignores any thread ID or threadKey that's included": a
// caller who asks to group a message into a thread and says nothing else gets a
// new thread every time, silently, with a successful response and no indication
// that the thing they asked for did not happen.
//
// That is the exact failure this tool exists not to have. Supplying a thread
// key is a request to thread, so the option that threads is what gets sent, and
// a caller who wants the stricter behaviour asks for ReplyOrFail by name.
func replyOption(req SendRequest) string {
	if req.MessageReplyOption != "" {
		return req.MessageReplyOption
	}
	if req.ThreadKey != "" {
		return ReplyFallbackToNewThread
	}
	return ""
}

// messagesPath is where a send goes.
//
// An empty space is the webhook case and is not an omission: the webhook URL is
// already the full messages endpoint for exactly one space, so there is no path
// to add and nowhere else the request could go. Refusing to guess is the point.
//
// The strict ^spaces/[A-Za-z0-9_-]+$ check from SPEC.md §15.8 lands with
// Milestone 3, where target resolution produces the value. What holds until
// then is checkRelative, which resolve runs over this and which refuses the
// shapes that could move the request to another host or another path.
func messagesPath(space string) string {
	if space == "" {
		return ""
	}
	return space + "/messages"
}

func setIf(query url.Values, name, value string) {
	if value != "" {
		query.Set(name, value)
	}
}

// DeriveMessageID returns a stable ID for a send, so that a retrying caller
// cannot double-post (SPEC.md §7.6).
//
// The digest covers the fields that decide whether two sends are the same
// message, and each one is length-prefixed before it is hashed. SPEC.md writes
// the derivation as sha256(space + body + threadKey), and plain concatenation
// makes ("ab", "c") and ("a", "bc") the same input. For an idempotency key that
// is the wrong direction to be wrong in: two genuinely different messages would
// share an ID, and the second one would be silently dropped by the API as a
// duplicate of the first.
func DeriveMessageID(space, text, threadKey string) string {
	// hash.Hash promises that Write never returns an error, which is why the
	// results are discarded rather than checked into a path that could not
	// happen.
	digest := sha256.New()
	for _, field := range []string{space, text, threadKey} {
		_, _ = fmt.Fprintf(digest, "%d:", len(field))
		_, _ = digest.Write([]byte(field))
	}
	return MessageIDPrefix + hex.EncodeToString(digest.Sum(nil))[:messageIDHexLength]
}

// spaceName is what the API calls a space: spaces/ and an opaque identifier.
var spaceName = regexp.MustCompile(`^spaces/[A-Za-z0-9_-]+$`)

// messageName is a message resource name: a space, then messages/, then an
// identifier.
//
// The identifier half admits a dot, which the space half does not, because the
// API's own generated message IDs contain one: a message reads as
// spaces/AAA/messages/nMs6.nMs6 rather than as a single opaque run. A
// caller-supplied ID is the other shape it takes, and those begin with the
// client- prefix and are hex from DeriveMessageID. Neither admits a slash, which
// is the character that would add a path segment.
//
// The identifier must contain at least one character that is not a dot, which
// is the part somebody would otherwise remove as fussy. Admitting the dot
// admitted "." and ".." as whole identifiers, and those are not characters that
// need escaping, they are path elements that move the request:
// spaces/AAA/messages/.. addresses the space rather than a message in it. The
// relative-path check caught it at the join, so nothing ever left the process
// wrong, but this pattern's promise is that anything it accepts is safe as a
// path segment unescaped, and it was leaning on the second layer to keep it. A
// rule that needs the layer below it is not the first layer. Found by
// FuzzAMessageNameThatIsAcceptedIsSafeUnescaped on a seed.
//
// Written as "any leading dots, then a non-dot" rather than as "must begin with
// an alphanumeric", which was the first attempt and was wrong. The API's IDs
// look base64url, and that alphabet contains - and _, so a leading-alphanumeric
// rule could refuse a message that exists. The failure would present as this
// tool being unable to open one of somebody's messages, which is the same shape
// of bug as a space regex that is too narrow, and it would be found by a user
// rather than by us. What is refused here is only what is dangerous: an
// identifier made of nothing but dots.
var messageName = regexp.MustCompile(`^spaces/[A-Za-z0-9_-]+/messages/[.]*[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

// CheckSpaceName refuses anything that is not a space resource name
// (SPEC.md §15.8).
//
// The rule is here rather than at each place a space arrives, because there is
// more than one such place and they land on the same request path. A space
// reaches this tool from a command line, from a webhook URL, from an alias
// somebody was sent, and at Milestone 4 from a resolver reading the API's own
// answers. Anything the pattern accepts is safe as a URL path segment
// unescaped, which makes escaping the second layer rather than the only one.
//
// Milestone 3's resolver is the other caller. It validates what it produces
// with this, so the two cannot disagree about what a space is.
func CheckSpaceName(space string) error {
	if space == "" {
		return clientErr("no space was given.")
	}
	if !spaceName.MatchString(space) {
		return clientErr("%q is not a space name.\nA space is %q followed by its identifier, as in %q.",
			space, "spaces/", "spaces/AAAAAAAAAAA")
	}
	return nil
}

// CheckMessageName refuses anything that is not a message resource name.
//
// The same rule as CheckSpaceName and for the same reason: this value becomes a
// URL path, so what the pattern accepts has to be safe there unescaped, and
// escaping stays the second layer rather than the only one. It is a separate
// function rather than a flag on the first because the two names admit
// different characters, and a single function taking a boolean would be one
// call site away from checking a message against the space rule.
func CheckMessageName(message string) error {
	if message == "" {
		return clientErr("no message was given.")
	}
	if !messageName.MatchString(message) {
		return clientErr("%q is not a message name.\nA message is a space, then %q, then its identifier, as in %q.",
			message, "messages/", "spaces/AAAAAAAAAAA/messages/BBBBBBBBBBB")
	}
	return nil
}
