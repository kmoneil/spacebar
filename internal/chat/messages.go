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
	"strings"

	"github.com/kmoneil/spacebar/internal/meta"
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

	// Attach is an upload token from Upload, turned into the message's
	// attachment field on the way out. A token rather than a resource name,
	// because that is what the create endpoint takes for something just
	// uploaded.
	Attach string
}

// SendMessage posts a message (SPEC.md §7.3).
func (c *Client) SendMessage(ctx context.Context, req SendRequest) (*Message, error) {
	if err := CheckSendTarget(req.Space); err != nil {
		return nil, err
	}
	if err := CheckMessageID(req.MessageID); err != nil {
		return nil, err
	}

	query := url.Values{}
	setIf(query, "messageReplyOption", replyOption(req))
	setIf(query, "messageId", req.MessageID)
	setIf(query, "requestId", req.RequestID)

	body := req.Message
	if req.Attach != "" {
		body.Attachment = append(body.Attachment, Attachment{
			AttachmentDataRef: &AttachmentDataRef{AttachmentUploadToken: req.Attach},
		})
	}
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

// CheckSendTarget refuses a space a send cannot go to.
//
// Empty is allowed, and only here. It is the webhook case and is not an
// omission: a webhook URL is already the full messages endpoint for exactly one
// space, so there is no path to add and nowhere else the request could go.
// Refusing to guess is the point.
//
// Anything else is a space name, checked by the same function every other
// method in this package uses. That was not true until now, and the reason it
// was not is a comment that outlived its milestone: this said the strict check
// "lands with Milestone 3, where target resolution produces the value. What
// holds until then is checkRelative". Milestone 3 landed, and so did four more.
//
// Nothing was reachable through the gap. `useroauth.Send` checks the space
// before calling and `webhook.Send` clears it, so both callers were covered.
// But every other write in this package checks its own resource name, and this
// was the one leaning on the layer below it and on its callers remembering,
// which is the arrangement this repository refuses three other times in these
// same files: a first layer that needs the layer below it to be safe is not a
// first layer.
func CheckSendTarget(space string) error {
	if space == "" {
		return nil
	}
	return CheckSpaceName(space)
}

// CheckMessageID refuses a caller-chosen message name the API will not take.
//
// Here rather than in an adapter, which is the whole of why it moved. The CLI
// refused an ID without the prefix and the MCP tool did not, so the same value
// was a usage error through one adapter and a 400 through the other, and
// SPEC.md §4 says neither adapter is where a decision gets made.
//
// The CLI still checks first and its message is better, because it can name the
// flag that carries the value. That is not a duplicate for the same reason
// `transport.Require` is not a duplicate of a transport's own refusal: one
// produces the better sentence and the other holds when a caller forgets.
//
// Only the prefix, and nothing about length or alphabet. The prefix is
// measured: the API refuses an ID without it with a message that does not
// mention it, which is why the constant exists. Whatever else the API requires
// of the rest has not been measured here, and a validator invented from a
// reference is how a tool refuses a value that would have worked.
//
// Empty is allowed and means the caller wants none, which is also what makes a
// send non-idempotent. That pairing is the reason this check is worth having at
// all: a message ID is what marks a POST safe to replay, so an ID the API will
// reject is a request marked replayable on the strength of a value that was
// never going to work.
func CheckMessageID(id string) error {
	if id == "" || strings.HasPrefix(id, MessageIDPrefix) {
		return nil
	}
	return clientErr("a message id has to begin with %q, which the API requires of any name a caller chooses.\n"+
		"%q does not, and the API refuses it with a message that does not mention the prefix.",
		MessageIDPrefix, id)
}

// messagesPath is where a send goes. The space has already been through
// CheckSendTarget.
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

// SpaceOfMessage is the space a message belongs to.
//
// A message name contains its space, so this reads it rather than asking the
// API, which means it costs nothing and cannot be wrong about a message that
// has since been deleted.
//
// It exists because `--allow-space` has to constrain a reaction as well as a
// send, and a reaction names a message rather than a space. Without this the
// allowlist would silently not apply to `react_to_message`: the tool would take
// a message in a space the operator never allowed and the check would have
// nothing to compare. Here rather than in `internal/mcpsrv` because a rule that
// lives in an adapter is a rule the other adapter does not have.
//
// Both names are checked, the message on the way in and the space on the way
// out. The second is not redundant: the message pattern is what guarantees the
// prefix is a space, and a first layer that needs the layer below it to be safe
// is not a first layer.
func SpaceOfMessage(message string) (string, error) {
	if err := CheckMessageName(message); err != nil {
		return "", err
	}

	space, _, found := strings.Cut(message, "/messages/")
	if !found {
		return "", clientErr("%q is not a message name.", message)
	}
	if err := CheckSpaceName(space); err != nil {
		return "", err
	}
	return space, nil
}

// EditRequest asks to replace a message's text.
type EditRequest struct {
	// Message is spaces/AAA/messages/BBB. Checked before it reaches a path.
	Message string

	// Text is the whole new body, not a patch of it. The API replaces the field.
	Text string
}

// EditMessage replaces a message's text (SPEC.md §7.3).
//
// updateMask is not optional and is not the caller's to forget. The API takes a
// PATCH with no mask as a request to update nothing, so a caller who omitted it
// would get a 200 and an unchanged message, which is the worst shape a failure
// can have: successful, silent, and wrong.
//
// The mask is "text" and nothing else, because text is the only field this tool
// edits. A mask naming a field the body does not carry would clear it.
func (c *Client) EditMessage(ctx context.Context, req EditRequest) (*Message, error) {
	if err := CheckMessageName(req.Message); err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("updateMask", "text")

	payload, err := c.do(ctx, Request{
		Method: http.MethodPatch,
		Path:   req.Message,
		Query:  query,
		Body:   Message{Text: req.Text},

		// This particular patch is idempotent: it sets a field to a value, so
		// sending it twice leaves the same message behind. PATCH does not opt in
		// by method, because that is a property of what the patch says rather
		// than of the verb, and the next PATCH this tool grows may append.
		Idempotent: true,
	})
	if err != nil {
		return nil, err
	}

	var edited Message
	if err := json.Unmarshal(payload, &edited); err != nil {
		return nil, c.wrapTransport(fmt.Errorf("the edit was accepted but the response could not be read: %w", err))
	}
	return &edited, nil
}

// DeleteMessage removes a message (SPEC.md §7.3).
//
// The API answers with an empty body, so there is nothing to decode and nothing
// to return but the failure.
func (c *Client) DeleteMessage(ctx context.Context, message string) error {
	if err := CheckMessageName(message); err != nil {
		return err
	}

	_, err := c.do(ctx, Request{Method: http.MethodDelete, Path: message})
	return err
}

// ReactRequest asks to put one emoji on one message.
type ReactRequest struct {
	Message string
	Emoji   string
}

// React adds a reaction to a message (SPEC.md §7.3).
func (c *Client) React(ctx context.Context, req ReactRequest) (*Reaction, error) {
	if err := CheckMessageName(req.Message); err != nil {
		return nil, err
	}
	if err := CheckEmoji(req.Emoji); err != nil {
		return nil, err
	}

	payload, err := c.do(ctx, Request{
		Method: http.MethodPost,
		Path:   req.Message + "/reactions",
		Body:   Reaction{Emoji: &Emoji{Unicode: req.Emoji}},

		// Not replayed. Whether a second identical reaction is a duplicate, an
		// error, or a no-op is the API's business, and a retry that turned a
		// successful reaction into a failure report would be this tool guessing
		// on the caller's behalf.
	})
	if err != nil {
		return nil, err
	}

	var added Reaction
	if err := json.Unmarshal(payload, &added); err != nil {
		return nil, c.wrapTransport(fmt.Errorf("the reaction was accepted but the response could not be read: %w", err))
	}
	return &added, nil
}

// CheckEmoji refuses what the reactions endpoint cannot take.
//
// It takes the emoji itself, as characters. A shortcode is refused at the proto
// level, measured rather than read: {"emoji": ":thumbsup:"} comes back as
// "Invalid value at 'reaction.emoji' (google.chat.v1.Emoji)". Passing one
// through would mean carrying a shortcode-to-emoji table in this tool, which is
// eighteen hundred entries that go stale, to save pasting one character.
//
// So the refusal happens here, before the request, and says what to type
// instead. The alternative is a 400 quoting a proto type at somebody who wrote
// something a chat client would have accepted.
func CheckEmoji(emoji string) error {
	if emoji == "" {
		return clientErr("no emoji was given.")
	}
	if len(emoji) > 2 && strings.HasPrefix(emoji, ":") && strings.HasSuffix(emoji, ":") {
		return clientErr("%q is a shortcode, and this endpoint takes the emoji itself.\n"+
			"Paste the character: %s react MESSAGE '👍'",
			emoji, meta.AppName)
	}
	return nil
}

// mediaName is what the download endpoint takes.
//
// Not a resource path, which is what this was first written for and what the
// name suggests. Measured against a real upload on 2026-08-16, the value is
// base64url with padding, and it decodes to the path:
//
//	ClpzcGFjZXMvQUFBQUV4YW1wbGVPbmUvbWVzc2FnZXMv.....
//	-> spaces/AAAAExampleOne/messages/mmmmExampleMsg.mmmmExampleMsg/attachments/AAAAExampleAttach...
//
// So the first version of this pattern would have refused every attachment
// there is. That is the failure mode a too-narrow validator has, and it was
// caught by uploading a file rather than by reading a reference.
//
// The alphabet is base64url, which is deliberately not base64: `+` and `/` are
// refused rather than escaped, because a `/` would add a path segment and this
// value is chosen by the server. What the pattern accepts is safe in a path
// unescaped, which is the same promise CheckSpaceName and CheckMessageName
// make, and escaping is the layer below rather than the only one.
var mediaName = regexp.MustCompile(`^[A-Za-z0-9_-]+=*$`)

// CheckMediaName refuses an attachment resource name that is not one.
func CheckMediaName(name string) error {
	if name == "" {
		return clientErr("no attachment was given.")
	}
	if !mediaName.MatchString(name) {
		return clientErr("%q is not an attachment resource name.\n"+
			"It is the resourceName inside an attachment's attachmentDataRef, which is base64.", name)
	}
	return nil
}
