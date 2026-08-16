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

package webhook_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/transport"
	"github.com/kmoneil/spacebar/internal/transport/webhook"
)

// TestAWebhookRefusesEveryOperationItCannotDo.
//
// A webhook is a URL that posts to one space, so everything else on the
// interface has to refuse, and refuse with the sentinel a caller branches on.
// Written as a table over the interface for the reason the useroauth one is:
// the mistake this catches is made while adding the eleventh method, not the
// first, and a refusal that quietly became a nil error would look like a
// working command right up until it sent nothing.
func TestAWebhookRefusesEveryOperationItCannotDo(t *testing.T) {
	posting, err := webhook.New(webhook.Options{
		Profile: "alerts",
		URL:     "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=k&token=t",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"spaces", func() error { return firstErr(posting.Spaces(ctx, chat.ListSpacesRequest{})) }},
		{"get space", func() error {
			_, err := posting.GetSpace(ctx, "spaces/AAAA")
			return err
		}},
		{"members", func() error {
			return firstErr(posting.Members(ctx, chat.ListMembersRequest{Space: "spaces/AAAA"}))
		}},
		{"messages", func() error {
			return firstErr(posting.Messages(ctx, chat.ListMessagesRequest{Space: "spaces/AAAA"}))
		}},
		{"get message", func() error {
			_, err := posting.GetMessage(ctx, "spaces/AAAA/messages/BBB")
			return err
		}},
		{"find direct message", func() error {
			_, err := posting.FindDirectMessage(ctx, "users/1")
			return err
		}},
		{"tail", func() error { return firstErr(posting.Tail(ctx, chat.TailRequest{Space: "spaces/AAAA"})) }},
		{"watch", func() error { return firstErr(posting.Watch(ctx, chat.WatchRequest{Space: "spaces/AAAA"})) }},
		{"upload", func() error {
			_, err := posting.Upload(ctx, chat.UploadRequest{Space: "spaces/AAAA"})
			return err
		}},
		{"download", func() error {
			_, err := posting.Download(ctx, "AAAA")
			return err
		}},
		{"edit", func() error {
			_, err := posting.EditMessage(ctx, chat.EditRequest{Message: "spaces/AAAA/messages/BBB"})
			return err
		}},
		{"delete", func() error { return posting.DeleteMessage(ctx, "spaces/AAAA/messages/BBB") }},
		{"react", func() error {
			_, err := posting.React(ctx, chat.ReactRequest{Message: "spaces/AAAA/messages/BBB", Emoji: "👍"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("a webhook was allowed to do this, and it is a URL that posts to one space")
			}
			if !errors.Is(err, transport.ErrUnsupported) {
				t.Errorf("refused with %v, which is not the sentinel a caller branches on", err)
			}
		})
	}
}

func firstErr[T any](seq func(func(T, error) bool)) error {
	var out error
	for _, err := range seq {
		if err != nil {
			out = err
		}
		break
	}
	return out
}
