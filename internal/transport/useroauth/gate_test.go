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

package useroauth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/transport"
	"github.com/kmoneil/spacebar/internal/transport/useroauth"
)

// TestEveryOperationIsGatedOnACapability.
//
// The structural claim, and the one that gets harder to keep true with every
// method added to the interface: an operation whose capability is missing
// refuses, and refuses before anything is built or sent.
//
// It is written as a table over the interface rather than as a test per method,
// because the failure it exists for is a method wired to the wrong capability
// or to none at all, and that is a mistake made while adding the eleventh one
// rather than the first.
//
// The transport is built with no capabilities, so every row must refuse. A row
// that stops refusing is either a capability that was dropped or a method that
// forgot to ask.
func TestEveryOperationIsGatedOnACapability(t *testing.T) {
	none, err := useroauth.New(useroauth.Options{
		Profile: "work",
		Auth:    staticAuth{},

		// No scopes at all, which is what a token record written before scopes
		// were recorded grants: nothing.
		Scopes: nil,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"send", func() error {
			_, err := none.Send(ctx, chat.SendRequest{Space: "spaces/AAA"})
			return err
		}},
		{"spaces", func() error { return firstErr(none.Spaces(ctx, chat.ListSpacesRequest{})) }},
		{"get space", func() error {
			_, err := none.GetSpace(ctx, "spaces/AAA")
			return err
		}},
		{"members", func() error {
			return firstErr(none.Members(ctx, chat.ListMembersRequest{Space: "spaces/AAA"}))
		}},
		{"messages", func() error {
			return firstErr(none.Messages(ctx, chat.ListMessagesRequest{Space: "spaces/AAA"}))
		}},
		{"get message", func() error {
			_, err := none.GetMessage(ctx, "spaces/AAA/messages/BBB")
			return err
		}},
		{"find direct message", func() error {
			_, err := none.FindDirectMessage(ctx, "users/1")
			return err
		}},
		{"tail", func() error { return firstErr(none.Tail(ctx, chat.TailRequest{Space: "spaces/AAA"})) }},
		{"watch", func() error { return firstErr(none.Watch(ctx, chat.WatchRequest{Space: "spaces/AAA"})) }},
		{"watch --all", func() error {
			return firstErr(none.WatchMany(ctx, chat.WatchManyRequest{Spaces: []string{"spaces/AAA"}}))
		}},
		{"upload", func() error {
			_, err := none.Upload(ctx, chat.UploadRequest{Space: "spaces/AAA"})
			return err
		}},
		{"download", func() error {
			_, err := none.Download(ctx, "AAAA")
			return err
		}},
		{"edit", func() error {
			_, err := none.EditMessage(ctx, chat.EditRequest{Message: "spaces/AAA/messages/BBB"})
			return err
		}},
		{"delete", func() error { return none.DeleteMessage(ctx, "spaces/AAA/messages/BBB") }},
		{"react", func() error {
			_, err := none.React(ctx, chat.ReactRequest{Message: "spaces/AAA/messages/BBB", Emoji: "👍"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("a profile with no scopes was allowed to do this")
			}
			if !errors.Is(err, transport.ErrUnsupported) {
				t.Errorf("refused with %v, which is not the capability sentinel a caller branches on", err)
			}
		})
	}
}

// staticAuth stands in for a token source. Nothing here reaches the network, so
// it is never asked for anything.
type staticAuth struct{}

func (staticAuth) Authorization(context.Context) (string, error) { return "Bearer test", nil }
func (staticAuth) Refresh(context.Context) (bool, error)         { return false, nil }

// firstErr drains an iterator far enough to see its first failure.
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
