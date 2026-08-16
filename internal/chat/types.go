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

import "encoding/json"

// The wire structs. They hold what the send path needs and what its response
// carries, and nothing else: the rest of SPEC.md §7.3 arrives with the
// milestones that call those endpoints, and a struct written now for an
// endpoint nobody calls is a guess that will be reviewed as though it were
// knowledge.
//
// Times are strings. The API returns RFC 3339, and decoding into a time.Time
// would mean re-encoding it to print it, which is a conversion that can lose or
// change something. This tool does not alter a value to make it representable,
// and that applies to a timestamp as much as to a message body. Formatting is
// the caller's business.

// Message is a Chat message.
type Message struct {
	// Name is the resource name, spaces/AAA/messages/BBB. Assigned by the API
	// on a send, unless the caller supplied a message ID.
	Name string `json:"name,omitempty"`

	Sender     *User  `json:"sender,omitempty"`
	CreateTime string `json:"createTime,omitempty"`

	// LastUpdateTime is set once a message has been edited.
	LastUpdateTime string `json:"lastUpdateTime,omitempty"`

	// Text is the message as Chat markup, which is not CommonMark.
	// internal/format is where the translation lives, and it is one-way.
	Text string `json:"text,omitempty"`

	// FormattedText is what the API returns for a message whose formatting it
	// has normalized. It is read-only: setting it on a send does nothing.
	FormattedText string `json:"formattedText,omitempty"`

	Thread *Thread `json:"thread,omitempty"`
	Space  *Space  `json:"space,omitempty"`

	// ThreadReply is true when this message is a reply rather than the start of
	// a thread.
	ThreadReply bool `json:"threadReply,omitempty"`

	// CardsV2 is carried through rather than modelled.
	//
	// A card is a deep tree of widgets with its own schema, and every field of
	// it would be a guess written here and reviewed as though it were
	// knowledge. The caller supplies the JSON, so the caller owns its shape,
	// and this tool does not silently drop a field it had not heard of. Each
	// element is a CardWithId: {"cardId": "...", "card": {...}}.
	//
	// Only a webhook can send one. A user-authenticated create is text-only,
	// because a card requires app authentication, which is the counter-intuitive
	// row of the capability matrix.
	CardsV2 []json.RawMessage `json:"cardsV2,omitempty"`
}

// User is whoever or whatever sent a message.
type User struct {
	// Name is users/NNN. A webhook send comes back with a sender of type BOT.
	Name string `json:"name,omitempty"`

	// DisplayName is chosen by the account holder and is not unique. Anything
	// printed from it is untrusted text, which is why internal/output escapes
	// what it renders.
	DisplayName string `json:"displayName,omitempty"`

	// Type is HUMAN or BOT.
	Type string `json:"type,omitempty"`
}

// Thread groups messages in a space.
type Thread struct {
	// Name is spaces/AAA/threads/CCC.
	Name string `json:"name,omitempty"`

	// ThreadKey is the caller's own name for a thread. It is what makes
	// threading possible over a webhook, which has no way to learn a thread's
	// resource name because it cannot read.
	ThreadKey string `json:"threadKey,omitempty"`
}

// Space is a room or a direct message.
type Space struct {
	// Name is spaces/AAA.
	Name string `json:"name,omitempty"`

	DisplayName string `json:"displayName,omitempty"`

	// SpaceType is SPACE, GROUP_CHAT, or DIRECT_MESSAGE. The older Type field
	// is deliberately absent: the API deprecated it, and carrying both would
	// mean deciding which one to believe.
	SpaceType string `json:"spaceType,omitempty"`
}

// Membership is somebody's place in a space.
//
// A direct message and a group chat have no display name of their own, so this
// is what `spaces list` has to fall back on to say who a conversation is with.
// That makes it a read path rather than an administrative curiosity.
type Membership struct {
	// Name is spaces/AAA/members/BBB.
	Name string `json:"name,omitempty"`

	// State is JOINED, INVITED, or NOT_A_MEMBER. Worth carrying because an
	// invited member is not a member yet, and a list that showed both the same
	// way would answer "who is in this space" wrongly.
	State string `json:"state,omitempty"`

	// Role is ROLE_MEMBER or ROLE_MANAGER.
	Role string `json:"role,omitempty"`

	// Member is the person. Absent when this membership is a Google Group,
	// which arrives as GroupMember instead.
	Member *User `json:"member,omitempty"`

	// GroupMember is carried through rather than modelled, for the reason
	// CardsV2 is: nothing in this tool reads inside it yet, and a struct written
	// now would be a guess reviewed as though it were knowledge.
	GroupMember json.RawMessage `json:"groupMember,omitempty"`

	CreateTime string `json:"createTime,omitempty"`
}
