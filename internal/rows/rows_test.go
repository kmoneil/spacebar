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

package rows

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/chat"
)

// TestTheRowShapesAreTheContractAnAgentParses.
//
// Frozen here rather than only in a golden, because the goldens can reach the
// success path only through a live API. These field names are what a caller
// selects on, so renaming one is a breaking change whether or not a golden moves.
func TestTheRowShapesAreTheContractAnAgentParses(t *testing.T) {
	space, _ := ForSpace(chat.Space{
		Name:            "spaces/AAA",
		DisplayName:     "Ops",
		SpaceType:       "SPACE",
		SingleUserBotDm: true,
		LastActiveTime:  "2026-08-15T09:00:00Z",
	})
	assertJSON(t, space, `{"name":"spaces/AAA","space_type":"SPACE","display_name":"Ops",`+
		`"single_user_bot_dm":true,"last_active_time":"2026-08-15T09:00:00Z"}`)

	member, _ := ForMember(chat.Membership{
		Name:  "spaces/AAA/members/m",
		State: "JOINED",
		Role:  "ROLE_MEMBER",
		Member: &chat.User{
			Name:        "users/1",
			DisplayName: "Ada",
			Type:        "HUMAN",
		},
		Affiliation: "INTERNAL",
		CreateTime:  "2026-08-15T09:00:00Z",
	})
	assertJSON(t, member, `{"name":"spaces/AAA/members/m","state":"JOINED","role":"ROLE_MEMBER",`+
		`"member":"users/1","display_name":"Ada","member_type":"HUMAN","affiliation":"INTERNAL",`+
		`"create_time":"2026-08-15T09:00:00Z"}`)

	message, _ := ForMessage(chat.Message{
		Name:       "spaces/AAA/messages/BBB",
		CreateTime: "2026-08-15T09:00:00Z",
		Text:       "deploy done",
		Sender:     &chat.User{Name: "users/1", DisplayName: "Ada", Type: "HUMAN"},
		Thread:     &chat.Thread{Name: "spaces/AAA/threads/T"},
	})
	assertJSON(t, message, `{"name":"spaces/AAA/messages/BBB","create_time":"2026-08-15T09:00:00Z",`+
		`"sender":"users/1","sender_display_name":"Ada","sender_type":"HUMAN",`+
		`"thread":"spaces/AAA/threads/T","text":"deploy done"}`)
}

func assertJSON(t *testing.T, value any, want string) {
	t.Helper()

	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if string(got) != want {
		t.Errorf("shape changed, which every caller sees:\n got %s\nwant %s", got, want)
	}
}

// TestAMissingSenderDoesNotProduceABlankColumnWithNoExplanation.
//
// A message with no sender is what a tombstone for a deleted message looks like,
// and --show-deleted returns those. The text column falls back to the resource
// name rather than printing an empty cell that reads like a rendering bug.
func TestAMissingSenderDoesNotProduceABlankColumnWithNoExplanation(t *testing.T) {
	_, cells := ForMessage(chat.Message{
		Name:   "spaces/AAA/messages/BBB",
		Sender: &chat.User{Name: "users/1"},
	})
	if cells[1] != "users/1" {
		t.Errorf("who = %q, want the resource name when there is no display name", cells[1])
	}

	_, cells = ForMessage(chat.Message{Name: "spaces/AAA/messages/BBB"})
	if cells[1] != "" {
		t.Errorf("who = %q, want empty when there is no sender at all", cells[1])
	}
}

// TestAMembershipAsTheAPIActuallyReturnsIt.
//
// TestTheRowShapesAreTheContractAnAgentParses builds a membership with a
// display name, which is the right shape to freeze and is not a shape this
// endpoint produces. Measured on 2026-08-16 across seven memberships in five
// spaces, spaces.members.list answers with a resource name and a type and
// nothing else:
//
//	"member": {"name": "users/100000000000000000001", "type": "HUMAN"}
//
// m3-99 saw that once and kept the display-name column, on the grounds that one
// observation of one membership in one space is not enough to change an output
// shape. This is the card where it was settled the other way, and what settled
// it was not the count: the sender of every message read the same day comes back
// the same way, so the empty name is how a user-authorized read answers rather
// than something about one space.
//
// So the text row carries the member's type where the name used to be, and the
// JSON keeps a display_name field that nothing has ever filled. A person loses a
// column that could not say anything and gains one that tells an app from a
// colleague; a program keeps every key it could select on.
func TestAMembershipAsTheAPIActuallyReturnsIt(t *testing.T) {
	data, cells := ForMember(chat.Membership{
		Name:  "spaces/AAA/members/100000000000000000001",
		State: "JOINED",
		Role:  "ROLE_MANAGER",
		Member: &chat.User{
			Name: "users/100000000000000000001",
			Type: "HUMAN",
		},
		Affiliation: "INTERNAL",
		CreateTime:  "2026-08-14T22:05:42.639018Z",
	})

	// No display_name key at all, rather than an empty one, so a consumer can
	// tell "not provided" from "provided and empty".
	assertJSON(t, data, `{"name":"spaces/AAA/members/100000000000000000001","state":"JOINED",`+
		`"role":"ROLE_MANAGER","member":"users/100000000000000000001","member_type":"HUMAN",`+
		`"affiliation":"INTERNAL","create_time":"2026-08-14T22:05:42.639018Z"}`)

	if len(cells) != 5 {
		t.Fatalf("cells = %q, want 5 columns", cells)
	}
	if cells[0] != "users/100000000000000000001" {
		t.Errorf("the first column is not the identifier: %q", cells[0])
	}
	if cells[1] != "HUMAN" {
		t.Errorf("the type column is %q, and it is what tells a person from an app", cells[1])
	}
	if cells[2] != "JOINED" || cells[3] != "ROLE_MANAGER" {
		t.Errorf("state and role are not in columns three and four: %q", cells)
	}
	if cells[4] != "INTERNAL" {
		t.Errorf("the affiliation column is %q, want what the API sent", cells[4])
	}
}

// TestAnAppsMembershipIsNotGivenAnAffiliationItDoesNotHave.
//
// The BOT membership measured on 2026-08-16 carries state, role, and member, and
// no affiliation at all, where every HUMAN membership beside it carries
// INTERNAL. That is the API declining to say whether an app is inside or outside
// an organization, which is the only sensible answer to a question that does not
// apply to it.
//
// The failure this guards against is a default. Filling the blank with INTERNAL
// would be a guess, printed in the column somebody reads before posting
// something they would not send outside the company, and it would be
// indistinguishable from a measurement.
func TestAnAppsMembershipIsNotGivenAnAffiliationItDoesNotHave(t *testing.T) {
	data, cells := ForMember(chat.Membership{
		Name:   "spaces/AAA/members/100000000000000000002",
		State:  "JOINED",
		Role:   "ROLE_MEMBER",
		Member: &chat.User{Name: "users/100000000000000000002", Type: "BOT"},
	})

	assertJSON(t, data, `{"name":"spaces/AAA/members/100000000000000000002","state":"JOINED",`+
		`"role":"ROLE_MEMBER","member":"users/100000000000000000002","member_type":"BOT"}`)

	if cells[4] != "" {
		t.Errorf("an app was given the affiliation %q; the API sent none", cells[4])
	}
	if cells[1] != "BOT" {
		t.Errorf("the type column is %q, and it is the only thing saying this is an app", cells[1])
	}
}

// TestTwoDirectMessagesAreNotTheSameRow.
//
// Four direct messages on the test account printed as four identical rows: a
// resource name, DIRECT_MESSAGE, and a blank display name. The cost was not
// cosmetic. m4-01 recorded that every direct message on the account was with a
// bot, having looked at one of four, and two of them are with people. The claim
// survived a review because the output could not contradict it.
//
// singleUserBotDm is what the API distinguishes them by, and this is the
// assertion that it reaches a person reading a terminal rather than stopping in
// the decoder.
func TestTwoDirectMessagesAreNotTheSameRow(t *testing.T) {
	withApp, appCells := ForSpace(chat.Space{
		Name:            "spaces/BBB",
		SpaceType:       "DIRECT_MESSAGE",
		SingleUserBotDm: true,
		LastActiveTime:  "2026-04-17T11:29:52.558415Z",
	})
	withPerson, personCells := ForSpace(chat.Space{
		Name:           "spaces/CCC",
		SpaceType:      "DIRECT_MESSAGE",
		LastActiveTime: "2023-02-24T18:03:10.183295Z",
	})

	if appCells[3] != BotDM {
		t.Errorf("the app column is %q for a direct message with an app", appCells[3])
	}
	if personCells[3] != "" {
		t.Errorf("the app column is %q for a direct message with a person", personCells[3])
	}
	if appCells[4] == "" || personCells[4] == "" {
		t.Error("last active is missing, and it is the other thing that tells two rows apart")
	}

	assertJSON(t, withApp, `{"name":"spaces/BBB","space_type":"DIRECT_MESSAGE",`+
		`"single_user_bot_dm":true,"last_active_time":"2026-04-17T11:29:52.558415Z"}`)

	// Absent rather than false, because that is what the API sends and because a
	// consumer filtering on the key gets the same answer either way.
	assertJSON(t, withPerson, `{"name":"spaces/CCC","space_type":"DIRECT_MESSAGE",`+
		`"last_active_time":"2023-02-24T18:03:10.183295Z"}`)
}

// TestAnEventRowCarriesWhatAWatchNeedsToShow.
//
// The payload is the part worth pinning. It is published raw, which none of the
// other shapes here do, because for a message that has since been deleted it is
// the only place the tombstone exists: `messages get` on that name answers
// nothing. A version of this that dropped it would look tidier and would send
// every consumer to an endpoint that cannot answer.
func TestAnEventRowCarriesWhatAWatchNeedsToShow(t *testing.T) {
	created := chat.SpaceEvent{
		Name:      "spaces/AAA/spaceEvents/MTc4Ng",
		EventType: "google.workspace.chat.message.v1.created",
		EventTime: "2026-08-16T21:36:48.376447Z",
		Subject:   "spaces/AAA/messages/BBB",
		Payload:   []byte(`{"message":{"name":"spaces/AAA/messages/BBB","text":"deploy done"}}`),
	}

	row, cells := ForEvent(created)
	assertJSON(t, row, `{"name":"spaces/AAA/spaceEvents/MTc4Ng",`+
		`"event_type":"google.workspace.chat.message.v1.created",`+
		`"event_time":"2026-08-16T21:36:48.376447Z","subject":"spaces/AAA/messages/BBB",`+
		`"payload":{"message":{"name":"spaces/AAA/messages/BBB","text":"deploy done"}}}`)

	if len(cells) != 4 {
		t.Fatalf("cells = %q, want 4 columns", cells)
	}
	if cells[1] != "message created" {
		t.Errorf("the type column is %q; forty characters of reverse domain pushes the row off the screen", cells[1])
	}
	if cells[3] != "deploy done" {
		t.Errorf("the text column is %q, and a watch that makes somebody fetch the message is a worse tail", cells[3])
	}
}

// TestAnEventThisToolDoesNotRecogniseStillPrints.
//
// A payload shaped some other way costs the subject and the text columns and
// nothing else. That is the direction a guess about somebody else's schema
// should be wrong in: the time and the type are the API's own fields and are
// still there.
func TestAnEventThisToolDoesNotRecogniseStillPrints(t *testing.T) {
	for _, payload := range []string{
		``,
		`{}`,
		`{"whatever":{"no":"name"}}`,
		`not json at all`,
		`{"reaction":{"name":"spaces/AAA/messages/BBB/reactions/CCC"}}`,
	} {
		row, cells := ForEvent(chat.SpaceEvent{
			Name:      "spaces/AAA/spaceEvents/X",
			EventType: "google.workspace.chat.space.v1.updated",
			EventTime: "2026-08-16T21:36:48.376447Z",
			Payload:   []byte(payload),
		})

		if row.EventType == "" || row.EventTime == "" {
			t.Errorf("payload %q cost the fields that were understood: %+v", payload, row)
		}
		if len(cells) != 4 {
			t.Errorf("payload %q produced %d columns", payload, len(cells))
		}
		if cells[1] != "space updated" {
			t.Errorf("payload %q lost its type column: %q", payload, cells[1])
		}
	}
}

// TestAMessageRowCarriesItsAttachmentsAndNotTheirDownloadURL.
//
// The URL the API returns beside every attachment carries an access token in
// its query, which makes it a credential rather than a link. It is not decoded
// in internal/chat, so it cannot appear here; this asserts the published shape
// is the resource name instead, which is what messages download takes.
func TestAMessageRowCarriesItsAttachmentsAndNotTheirDownloadURL(t *testing.T) {
	row, _ := ForMessage(chat.Message{
		Name: "spaces/AAA/messages/BBB",
		Text: "the report",
		Attachment: []chat.Attachment{{
			Name:              "spaces/AAA/messages/BBB/attachments/CCC",
			ContentName:       "report.pdf",
			ContentType:       "application/pdf",
			AttachmentDataRef: &chat.AttachmentDataRef{ResourceName: "Q2xvbmU="},
			Source:            "UPLOADED_CONTENT",
		}, {
			// A Drive file, which has no bytes to fetch here.
			Name:         "spaces/AAA/messages/BBB/attachments/DDD",
			ContentName:  "notes",
			DriveDataRef: []byte(`{"driveFileId":"abc"}`),
			Source:       "DRIVE_FILE",
		}},
	})

	if len(row.Attachments) != 2 {
		t.Fatalf("attachments = %+v", row.Attachments)
	}
	if row.Attachments[0].ResourceName != "Q2xvbmU=" {
		t.Errorf("the first attachment lost its resource name: %+v", row.Attachments[0])
	}
	if row.Attachments[1].ResourceName != "" {
		t.Errorf("a Drive file was given a resource name: %+v", row.Attachments[1])
	}

	body, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	for _, forbidden := range []string{"downloadUri", "thumbnailUri", "attachment_token"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("the published shape carries %s, which is a credential:\n%s", forbidden, body)
		}
	}
}
