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

package auth

import (
	"strings"
	"testing"
)

// TestRedactURL is the shape a Chat incoming webhook actually has, and the
// variations of it that a redactor written against one example would miss.
func TestRedactURL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		keep    []string
		secrets []string
	}{
		{
			name:    "a webhook URL keeps its space and loses its credentials",
			raw:     "https://chat.googleapis.com/v1/spaces/AAAA1111/messages?key=AIzaSyFAKEKEY&token=FAKETOKEN",
			keep:    []string{"chat.googleapis.com", "spaces/AAAA1111", "REDACTED"},
			secrets: []string{"AIzaSyFAKEKEY", "FAKETOKEN"},
		},
		{
			// The same credentials in the other order. A redactor built by
			// pattern-matching one example string gets this wrong.
			name:    "the parameters in the other order",
			raw:     "https://chat.googleapis.com/v1/spaces/AAAA1111/messages?token=FAKETOKEN&key=AIzaSyFAKEKEY",
			keep:    []string{"spaces/AAAA1111", "REDACTED"},
			secrets: []string{"AIzaSyFAKEKEY", "FAKETOKEN"},
		},
		{
			name: "extra parameters survive, credentials do not",
			raw: "https://chat.googleapis.com/v1/spaces/AAAA1111/messages?" +
				"key=AIzaSyFAKEKEY&token=FAKETOKEN&messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD&threadKey=deploys",
			keep:    []string{"messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD", "threadKey=deploys"},
			secrets: []string{"AIzaSyFAKEKEY", "FAKETOKEN"},
		},
		{
			name:    "a repeated parameter is redacted in every position",
			raw:     "https://chat.googleapis.com/v1/spaces/AAAA1111/messages?key=FIRSTKEY&key=SECONDKEY",
			keep:    []string{"REDACTED"},
			secrets: []string{"FIRSTKEY", "SECONDKEY"},
		},
		{
			name:    "credentials in the authority",
			raw:     "https://user:hunter2@chat.googleapis.example/v1/spaces/AAAA1111/messages",
			keep:    []string{"spaces/AAAA1111"},
			secrets: []string{"hunter2"},
		},
		{
			name:    "an OAuth token endpoint response redirect",
			raw:     "https://oauth2.googleapis.example/token?code=4/FAKECODE&client_secret=GOCSPX-FAKE",
			keep:    []string{"REDACTED"},
			secrets: []string{"4/FAKECODE", "GOCSPX-FAKE"},
		},
		{
			name:    "a URL with no query is left alone",
			raw:     "https://chat.googleapis.com/v1/spaces/AAAA1111/messages",
			keep:    []string{"https://chat.googleapis.com/v1/spaces/AAAA1111/messages"},
			secrets: nil,
		},
		{
			// Whatever this is, it came out of a field that holds a
			// credential, and nothing can be reasoned about it.
			name:    "something that will not parse is replaced entirely",
			raw:     "https://chat.googleapis.com/v1/spaces/AAAA\x7f1111?key=FAKEKEY",
			keep:    []string{"REDACTED"},
			secrets: []string{"FAKEKEY"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactURL(tc.raw)
			for _, want := range tc.keep {
				if !strings.Contains(got, want) {
					t.Errorf("redacted to %q, which does not contain %q", got, want)
				}
			}
			for _, secret := range tc.secrets {
				if strings.Contains(got, secret) {
					t.Errorf("redacted to %q, which still contains the secret %q", got, secret)
				}
			}
		})
	}
}

func TestRedactURLLeavesTheEmptyStringAlone(t *testing.T) {
	if got := RedactURL(""); got != "" {
		t.Errorf("redacted an empty string to %q", got)
	}
}
