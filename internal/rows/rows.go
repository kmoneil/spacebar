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

// Package rows holds the shapes this tool publishes, and the projection from
// the wire onto them.
//
// It exists because there are two adapters and one contract. `--json` and an
// MCP tool result are read by the same kind of consumer, usually a program,
// often an agent, and if each adapter built its own shape the tool would
// publish two vocabularies for one answer and only one of them would be
// documented. SPEC.md §4 says neither adapter is where a decision gets made,
// and a struct tag is a decision.
//
// Not in internal/output, which cannot import internal/chat because
// internal/chat imports it. Not in internal/chat either: these shapes are
// deliberately not the wire, and a package holding both would invite somebody
// to notice the duplication and unify them, which is the thing the comments
// below exist to prevent.
//
// Each function returns the published shape and the tab-separated cells beside
// it. They are different documents for different readers, which is the same
// split output.Result makes, and deriving either from the other produces
// something bad at both jobs. A caller that wants only one takes only one.
package rows

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/kmoneil/spacebar/internal/chat"
)

// Space is the published shape of one space.
//
// A shape of this repository's choosing rather than the wire struct, because a
// golden file makes it a public API the moment one records it. Passing the
// API's own document through would mean every field Google adds becomes part of
// this tool's contract without anybody deciding it should be.
type Space struct {
	Name        string `json:"name"`
	SpaceType   string `json:"space_type,omitempty"`
	DisplayName string `json:"display_name,omitempty"`

	// SingleUserBotDm is what tells a direct message with an app from one with a
	// person. Both have no display name, so without it they are the same row.
	SingleUserBotDm bool   `json:"single_user_bot_dm,omitempty"`
	LastActiveTime  string `json:"last_active_time,omitempty"`
}

// BotDM is what the fourth column of `spaces list` says for a direct message
// with an app.
//
// A word rather than "true", because the column is read by a person scanning a
// list and by an awk one-liner, and both are served better by a value that says
// what it means. Empty for everything else, which is the wire's own meaning:
// the API omits the field rather than sending false.
const BotDM = "bot"

// ForSpace projects one space onto what is published about it.
func ForSpace(s chat.Space) (Space, []string) {
	marker := ""
	if s.SingleUserBotDm {
		marker = BotDM
	}

	return Space{
			Name:            s.Name,
			SpaceType:       s.SpaceType,
			DisplayName:     s.DisplayName,
			SingleUserBotDm: s.SingleUserBotDm,
			LastActiveTime:  s.LastActiveTime,
		}, []string{
			s.Name,
			s.SpaceType,
			s.DisplayName,
			marker,
			s.LastActiveTime,
		}
}

// Member is the published shape of one membership.
type Member struct {
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
	Role  string `json:"role,omitempty"`

	// Member is users/NNN, which is the stable identifier. DisplayName is not:
	// it is chosen by the account holder, is not unique, and is untrusted text.
	//
	// The API has never sent one. It is kept as a field, with omitempty, so that
	// a program parsing this gets it for free on the day Google starts, and so
	// that dropping it from the text row is a change to what a person reads
	// rather than to what a script can select.
	Member      string `json:"member,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	MemberType  string `json:"member_type,omitempty"`

	// GroupMember is groups/NNN when a Google Group holds this membership, in
	// which case Member is empty. Exactly one of the two is set.
	//
	// Its own key rather than a value in Member, because that is how the wire
	// separates them and because a program selecting .member is asking about a
	// person. Overloading the field would make every existing consumer start
	// receiving a kind of value it has never seen, silently.
	//
	// There is no group display name and no group address to go with it: a
	// Chat scope reaches groups/NNN and no further, measured on 2026-08-17.
	GroupMember string `json:"group_member,omitempty"`

	// Affiliation is INTERNAL or EXTERNAL, and absent for an app.
	Affiliation string `json:"affiliation,omitempty"`

	// CreateTime is when they joined. In the JSON and not in a column, because
	// "when did they join" is a different question from "who is in here" and the
	// row is already as wide as a person can scan.
	CreateTime string `json:"create_time,omitempty"`
}

// ForMember projects one membership onto what is published about it.
func ForMember(m chat.Membership) (Member, []string) {
	row := Member{
		Name:        m.Name,
		State:       m.State,
		Role:        m.Role,
		Affiliation: m.Affiliation,
		CreateTime:  m.CreateTime,
	}
	if m.Member != nil {
		row.Member = m.Member.Name
		row.DisplayName = m.Member.DisplayName
		row.MemberType = m.Member.Type
	}
	if m.GroupMember != nil {
		row.GroupMember = m.GroupMember.Name
	}

	// The type column is where the display name used to be, and the trade is
	// deliberate. Measured on 2026-08-16 across seven memberships in five
	// spaces, and against the sender of every message read the same day, a
	// user-authenticated read returns {"name": "users/NNN", "type": "HUMAN"} and
	// nothing else, so that column was structurally blank. HUMAN against BOT is
	// the fact it was standing in front of: it tells a person from an app, which
	// is the question a blank name was leaving unanswered.
	//
	// A group membership has no member at all, so without the two lines below it
	// projects to four blank cells out of five and reads as a rendering fault
	// rather than as what it is. GROUP is this tool's word and not the API's:
	// the wire distinguishes the two kinds structurally, by which field it
	// populates, and sends no type string on a group. --json keeps that
	// structure and invents no member_type, so nothing there is a value the API
	// did not send; the column exists because a person scanning a table cannot
	// see which key was populated.
	identifier, kind := row.Member, row.MemberType
	if row.GroupMember != "" {
		identifier, kind = row.GroupMember, "GROUP"
	}

	return row, []string{identifier, kind, m.State, m.Role, m.Affiliation}
}

// Message is the published shape of one message.
//
// Chosen here rather than passed through from the wire, so that a field the API
// adds does not silently become part of this tool's contract. Text is the
// message as Chat markup, unaltered: this tool does not rewrite a value to make
// it easier to read, and a body that went out as Chat markup comes back as Chat
// markup.
type Message struct {
	Name       string `json:"name"`
	CreateTime string `json:"create_time,omitempty"`

	// LastUpdateTime is set once a message has been edited, and its presence is
	// the only way to tell an edited message from an original one.
	LastUpdateTime string `json:"last_update_time,omitempty"`

	Sender      string `json:"sender,omitempty"`
	DisplayName string `json:"sender_display_name,omitempty"`
	SenderType  string `json:"sender_type,omitempty"`

	Thread      string `json:"thread,omitempty"`
	ThreadReply bool   `json:"thread_reply,omitempty"`

	Text string `json:"text,omitempty"`

	// Attachments is what came with the message, if anything. The download URI
	// the API returns beside each one is deliberately absent: it carries an
	// access token in its query, so publishing it would put a credential in
	// --json.
	Attachments []Attachment `json:"attachments,omitempty"`

	// AttachedGifs is the URL of each GIF that arrived through Chat's own GIF
	// picker.
	//
	// A list of strings and not a list of objects, because chat.AttachedGif has
	// exactly one field, so nothing is lost by flattening and a caller writing
	// `jq -r '.attached_gifs[]'` gets the URLs. That is the same call already
	// made for an attachment's resource_name, which is lifted out of the
	// attachmentDataRef it arrives inside. If the schema ever grows a second
	// field this becomes an object and that is a contract break to take
	// deliberately, rather than a reason to publish an awkward shape now
	// against a field that may never gain one.
	AttachedGifs []string `json:"attached_gifs,omitempty"`

	// Cards and CardsV2 are the message's card content, carried raw.
	//
	// Both were decoded and then dropped, which is two independent ways to lose
	// the same thing: Cards had no field on chat.Message at all, and CardsV2
	// had one and was not projected here. An app posting a GIF, a form, or a
	// button produced a row whose text was the attribution line and whose
	// content was gone, and nothing said a field had been discarded.
	//
	// Raw for the reason internal/chat carries them raw: a card is a deep tree
	// of widgets with its own schema, and modelling it here would be a guess
	// reviewed as though it were knowledge.
	//
	// A card is untrusted content, chosen by whoever posted the message. It
	// reaches --json and MCP and never a text column, which is what keeps it
	// clear of output.Cell and the Unicode Tags rule: those exist for what is
	// rendered to a terminal, and --json hands a program what was there.
	Cards []json.RawMessage `json:"cards,omitempty"`

	// CardsV2 is the modern spelling of the same thing. Both are published
	// because the API returns whichever the sender used, and a message sent
	// before the change holds Cards for as long as it exists.
	CardsV2 []json.RawMessage `json:"cards_v2,omitempty"`
}

// Attachment is the published shape of one file on a message.
type Attachment struct {
	Name        string `json:"name"`
	ContentName string `json:"content_name,omitempty"`
	ContentType string `json:"content_type,omitempty"`

	// ResourceName is the handle the download endpoint takes. Base64, and
	// opaque: it is not a path, whatever it looks like when decoded.
	ResourceName string `json:"resource_name,omitempty"`

	// Source is UPLOADED_CONTENT or DRIVE_FILE. A Drive file has no resource
	// name here and is fetched with Drive's API rather than this one, which is
	// worth knowing before writing a loop that assumes every attachment can be
	// downloaded.
	Source string `json:"source,omitempty"`
}

// ForMessage projects one message onto what is published about it.
func ForMessage(m chat.Message) (Message, []string) {
	row := Message{
		Name:           m.Name,
		CreateTime:     m.CreateTime,
		LastUpdateTime: m.LastUpdateTime,
		ThreadReply:    m.ThreadReply,
		Text:           m.Text,
	}
	if m.Sender != nil {
		row.Sender = m.Sender.Name
		row.DisplayName = m.Sender.DisplayName
		row.SenderType = m.Sender.Type
	}
	if m.Thread != nil {
		row.Thread = m.Thread.Name
	}
	for _, file := range m.Attachment {
		attachment := Attachment{
			Name:        file.Name,
			ContentName: file.ContentName,
			ContentType: file.ContentType,
			Source:      file.Source,
		}
		if file.AttachmentDataRef != nil {
			attachment.ResourceName = file.AttachmentDataRef.ResourceName
		}
		row.Attachments = append(row.Attachments, attachment)
	}

	for _, gif := range m.AttachedGifs {
		row.AttachedGifs = append(row.AttachedGifs, gif.URI)
	}
	row.Cards = m.Cards
	row.CardsV2 = m.CardsV2

	// The display name is preferred in the text column and the resource name is
	// the fallback, which is the opposite of how --json orders them. A person
	// reading a terminal wants to know who said it; a script wants a value it
	// can compare, and --json carries both.
	who := row.DisplayName
	if who == "" {
		who = row.Sender
	}

	// A GIF from Chat's picker is the whole of a message and arrives with no
	// text at all, so the text column would be blank on a row that has content.
	// A human reading `tail` cannot tell that from a rendering fault, and this
	// tool would be showing nothing for a message that says something.
	//
	// The URL is what the API sent rather than a description invented here, and
	// --json keeps the two apart: text stays empty because it was empty, and
	// attached_gifs carries the URLs. This is the same split the who column
	// already makes, where a display name is preferred for a person reading and
	// --json carries the resource name beside it.
	//
	// A card gets no such fallback, because reaching an image URL inside one
	// means modelling a widget tree this package deliberately carries raw. In
	// practice an app that posts a card sends text with it: the measured Giphy
	// messages carry the attribution line.
	body := m.Text
	if body == "" {
		body = strings.Join(row.AttachedGifs, " ")
	}

	// output.Cell escapes the tab and the newline, so a message body cannot
	// forge a column or a row here. That is why the body can be a column at all.
	return row, []string{m.CreateTime, who, body}
}

// Event is the published shape of one space event.
//
// The payload travels with it, raw. That is a departure from what the other
// three shapes do, and it is deliberate: for a message that has since been
// deleted, the payload is the only place the tombstone exists, and `messages
// get` on that name answers nothing. Publishing the type without the subject
// would send every consumer to an endpoint that cannot answer.
type Event struct {
	Name      string `json:"name"`
	EventType string `json:"event_type,omitempty"`
	EventTime string `json:"event_time,omitempty"`

	// Subject is the resource name of whatever happened: the message, the
	// reaction, the membership.
	Subject string `json:"subject,omitempty"`

	// Payload is the API's own event data, unaltered. Absent when the event
	// carried none.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ForEvent projects one space event onto what is published about it.
//
// The text row leads with the time and then the type, which is the order tail
// and messages list use: when, then what, is how a person reads a stream. The
// fourth column is the message text when the event carries one, because a watch
// that says "a message was created" and makes somebody fetch it is a worse tail.
func ForEvent(e chat.SpaceEvent) (Event, []string) {
	return Event{
			Name:      e.Name,
			EventType: e.EventType,
			EventTime: e.EventTime,
			Subject:   e.Subject,
			Payload:   e.Payload,
		}, []string{
			e.EventTime,
			shortEventType(e.EventType),
			e.Subject,
			textOf(e.Payload),
		}
}

// shortEventType trims the reverse-domain prefix off an event type for the text
// column, and leaves anything it does not recognise alone.
//
// "google.workspace.chat.message.v1.created" is forty characters of which seven
// carry the information, and a column that wide pushes everything else off the
// screen. --json keeps the full value, because a program comparing against the
// API's own documentation needs the name the API uses.
func shortEventType(full string) string {
	const prefix = "google.workspace.chat."
	trimmed, ok := strings.CutPrefix(full, prefix)
	if !ok {
		return full
	}
	return strings.ReplaceAll(trimmed, ".v1.", " ")
}

// textOf lifts a message body out of an event payload when there is one.
//
// Best effort by design. A payload this does not recognise produces an empty
// column rather than an error, because the row is still worth printing: the
// time, the type and the subject are all there, and a reaction has no text to
// find in the first place.
func textOf(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}

	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		return ""
	}
	// Sorted, for the reason internal/chat sorts the same shape: ranging a map
	// is not ordered, so with two candidate keys the same bytes produce a
	// different column on different runs of the same build.
	for _, key := range slices.Sorted(maps.Keys(wrapper)) {
		var subject struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(wrapper[key], &subject); err == nil && subject.Text != "" {
			return subject.Text
		}
	}
	return ""
}

// Reaction is the published shape of one emoji reaction.
//
// It moved here from internal/cli in the m5-99 sweep. It had been declared in
// the command that prints it, which made it the one published shape an adapter
// owned, and the parity check is what found it: react_to_message could not
// return the same object the CLI does without either importing a command
// package or declaring a second, silently divergent copy.
type Reaction struct {
	Name    string `json:"name"`
	Message string `json:"message"`

	// Emoji is the unicode character, which is the only form this API accepts.
	// A shortcode is refused before the request.
	Emoji string `json:"emoji,omitempty"`

	// User is who reacted, as users/NNN. Absent when the API did not say.
	User string `json:"user,omitempty"`
}

// ForReaction projects one reaction onto what is published about it.
//
// The message is passed in rather than read off the reaction's own name.
// Trimming two path segments off the far end's string would be this tool
// deriving a resource name from a value it did not check, and the caller
// already holds the message it asked about.
func ForReaction(r chat.Reaction, message string) (Reaction, []string) {
	row := Reaction{Name: r.Name, Message: message}
	if r.Emoji != nil {
		row.Emoji = r.Emoji.Unicode
	}
	if r.User != nil {
		row.User = r.User.Name
	}
	return row, []string{row.Message, row.Emoji, row.User}
}
