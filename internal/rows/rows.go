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

import "github.com/kmoneil/spacebar/internal/chat"

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

	// The type column is where the display name used to be, and the trade is
	// deliberate. Measured on 2026-08-16 across seven memberships in five
	// spaces, and against the sender of every message read the same day, a
	// user-authenticated read returns {"name": "users/NNN", "type": "HUMAN"} and
	// nothing else, so that column was structurally blank. HUMAN against BOT is
	// the fact it was standing in front of: it tells a person from an app, which
	// is the question a blank name was leaving unanswered.
	return row, []string{row.Member, row.MemberType, m.State, m.Role, m.Affiliation}
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

	// The display name is preferred in the text column and the resource name is
	// the fallback, which is the opposite of how --json orders them. A person
	// reading a terminal wants to know who said it; a script wants a value it
	// can compare, and --json carries both.
	who := row.DisplayName
	if who == "" {
		who = row.Sender
	}

	// output.Cell escapes the tab and the newline, so a message body cannot
	// forge a column or a row here. That is why the body can be a column at all.
	return row, []string{m.CreateTime, who, m.Text}
}
