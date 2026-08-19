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
	"net/url"
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

// FuzzARedactedURLCarriesNoCredential states what the redactor is for, which
// the table above only samples.
//
// The property is stated by planting the secret rather than by hunting for one.
// A fuzzer handed a whole URL and asked "is there a credential left in it"
// needs a rule for what a credential looks like, and that rule would be the
// same guess the function under test is making: the target would agree with
// the code by construction and pass whatever either of them got wrong. So the
// value is a sentinel this test chose, put in a place the function has
// promised to redact, and the only question asked is whether it survived.
//
// The fuzzed string is everything around it. That is where both defects were:
// a semicolon anywhere in the query made url.URL.Query answer with nothing and
// the whole raw query came back untouched, and a fragment was never looked at
// by either redaction layer.
//
// The second half of the property matters as much as the first. A redactor
// that returns REDACTED for every input passes the first half and is useless,
// so a URL that parses has to come back still naming its space.
func FuzzARedactedURLCarriesNoCredential(f *testing.F) {
	for _, seed := range []string{
		"", "&x=1", ";x=1", "#access_token=leaked", "&token=second",
		"&key=other", "?", "#", "%", "&a=%zz", "&;", ";;;", "\x7f",
		"&note=nothing+secret", "#section", "/../..", "&key=", "&key",
	} {
		f.Add(seed)
	}

	// Long, and shaped like nothing url.Parse would produce on its own, so a
	// match is the planted value and never a coincidence.
	const sentinel = "sentinelFAKECREDENTIAL0123456789abcdef"
	const space = "spaces/AAAATestSpace"

	f.Fuzz(func(t *testing.T, around string) {
		// The sentinel appearing in the fuzzer's own string would be a
		// legitimate survival: it is then somebody's ordinary text in a
		// parameter nobody promised to redact.
		if strings.Contains(around, sentinel) {
			return
		}

		for _, raw := range []string{
			// In the value, with the fuzzed text after it. This is the
			// semicolon case: the pair parses as one thing or as nothing at
			// all depending on what follows.
			"https://chat.example/v1/" + space + "/messages?key=" + sentinel + around,

			// Behind the fuzzed text, so a parser that gives up part way
			// through leaves the credential rather than the ordinary value.
			"https://chat.example/v1/" + space + "/messages?" + around + "&token=" + sentinel,

			// In a fragment, which is never sent and is where an implicit-flow
			// access token arrives.
			"https://chat.example/v1/" + space + "/messages?" + around + "#access_token=" + sentinel,

			// And in the authority. Spliced rather than concatenated so that
			// the literal names a reserved host: internal/lint reads string
			// literals looking for a URL, and "https://user:" on its own
			// parses as a host called "user".
			strings.Replace("https://user:PASSWORD@chat.example/v1/"+space+"?", "PASSWORD", sentinel, 1) + around,
		} {
			got := RedactURL(raw)
			if strings.Contains(got, sentinel) {
				t.Fatalf("the credential survived redaction:\n  in %q\n out %q", raw, got)
			}

			// A URL that parsed still has to say where the request was going,
			// or the redaction has answered the question by refusing to.
			if u, err := url.Parse(raw); err == nil && strings.Contains(u.EscapedPath(), space) {
				if !strings.Contains(got, space) {
					t.Fatalf("redaction removed the space as well as the credential:\n  in %q\n out %q", raw, got)
				}
			}
		}
	})
}

// TestARedactedURLKeepsNothingBackFromAQueryItCannotRead is the semicolon
// defect written out as the case it was, so that it reads in the source rather
// than only in a corpus file.
//
// url.URL.Query discards its error. url.ParseQuery refuses the entire pair a
// semicolon appears in, so a query of nothing but such a pair parsed to zero
// parameters, the "nothing to redact" path returned u.String(), and u.String()
// writes RawQuery back exactly as it arrived.
//
// It was not reachable with a real credential through today's callers, and
// that is worth saying rather than leaving somebody to find out. internal/chat
// builds the URL this is called on, and its own url.URL.Query call drops the
// same pair on the way, so the request goes out with no key rather than with a
// leaked one. What made it worth fixing anyway is that the same silent drop
// leaves internal/chat's second redaction layer with an empty list of secrets:
// on such a profile both layers are off at once, and the only thing standing
// between them and a log line is that the first one now fails closed.
func TestARedactedURLKeepsNothingBackFromAQueryItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    string
		secret string
	}{
		{
			name:   "a semicolon makes the whole query unreadable",
			raw:    "https://chat.googleapis.com/v1/spaces/AAAA1111/messages?key=AIzaSyFAKEKEY;x=1",
			secret: "AIzaSyFAKEKEY",
		},
		{
			name:   "a semicolon beside a pair that does parse",
			raw:    "https://chat.googleapis.com/v1/spaces/AAAA1111/messages?a=1&key=AIzaSyFAKEKEY;b=2",
			secret: "AIzaSyFAKEKEY",
		},
		{
			name:   "an escape that will not decode",
			raw:    "https://chat.googleapis.com/v1/spaces/AAAA1111/messages?key=AIzaSyFAKEKEY&x=%zz",
			secret: "AIzaSyFAKEKEY",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactURL(tc.raw)
			if strings.Contains(got, tc.secret) {
				t.Errorf("redacted to %q, which still contains the secret %q", got, tc.secret)
			}
			// The path is the part somebody reading a dry run is checking, and
			// an unreadable query is no reason to take it away from them.
			if !strings.Contains(got, "spaces/AAAA1111") {
				t.Errorf("redacted to %q, which no longer names the space", got)
			}
		})
	}
}

// TestAFragmentIsRedactedWhetherOrNotItLooksLikeACredential.
//
// A fragment is never sent to a server, so nothing about a request is learned
// by reading one, and the OAuth implicit flow returns an access token in one.
// Reading it to decide would mean deciding what a fragment means, and "#top"
// and "#access_token=..." are the same syntax.
//
// It reaches here: internal/chat copies the base URL wholesale when it builds
// a request, so a fragment on a profile's webhook URL is on every request URL
// this redacts, and internal/chat's scrub collects its known secret values out
// of the base URL's query and so has never seen the fragment's.
func TestAFragmentIsRedactedWhetherOrNotItLooksLikeACredential(t *testing.T) {
	for _, tc := range []struct{ name, raw, gone string }{
		{
			name: "an implicit-flow access token",
			raw:  "https://chat.googleapis.com/v1/spaces/AAAA1111/messages#access_token=ya29.FAKETOKEN",
			gone: "ya29.FAKETOKEN",
		},
		{
			name: "an ordinary anchor, redacted too, because telling them apart means reading one",
			raw:  "https://chat.googleapis.com/v1/spaces/AAAA1111/messages#top",
			gone: "#top",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactURL(tc.raw)
			if strings.Contains(got, tc.gone) {
				t.Errorf("redacted to %q, which still contains %q", got, tc.gone)
			}
			if !strings.Contains(got, "spaces/AAAA1111") {
				t.Errorf("redacted to %q, which no longer names the space", got)
			}
		})
	}
}
