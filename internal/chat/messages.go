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
	ThreadKey string

	// MessageReplyOption decides what happens when ThreadKey matches no
	// existing thread. The API's own values, passed through rather than
	// translated, because a name invented here would have to be mapped back and
	// the mapping is where a wrong default hides.
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
	setIf(query, "threadKey", req.ThreadKey)
	setIf(query, "messageReplyOption", req.MessageReplyOption)
	setIf(query, "messageId", req.MessageID)
	setIf(query, "requestId", req.RequestID)

	payload, err := c.do(ctx, Request{
		Method: http.MethodPost,
		Path:   messagesPath(req.Space),
		Query:  query,
		Body:   req.Message,

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
