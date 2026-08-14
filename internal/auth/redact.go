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

import "net/url"

// Redacted is what replaces a secret. A placeholder rather than an omission,
// because a missing line reads as "nothing was sent", which is a different and
// wrong answer to the question somebody uses --dry-run to ask.
const Redacted = "REDACTED"

// secretParams are the query parameters that are credentials.
//
// key and token are the pair that makes a Chat incoming webhook URL a bearer
// credential rather than an address: together they are the entire
// authentication for posting to that space. The rest are here because an OAuth
// endpoint puts them in a query string and a URL from anywhere may end up in a
// log line.
var secretParams = map[string]bool{
	"key":           true,
	"token":         true,
	"access_token":  true,
	"refresh_token": true,
	"client_secret": true,
	"code":          true,
}

// SecretParam reports whether a query parameter of this name carries a
// credential.
//
// Exported so that the one list has one reader more: internal/chat refuses to
// let a request set any of these, because a caller that could set key or token
// could point an otherwise correct request at a credential of its own choosing.
// A second copy of the list somewhere else would be a second chance for the two
// to disagree, and the one that disagrees quietly is the one that leaks.
func SecretParam(name string) bool { return secretParams[name] }

// RedactURL returns raw with every credential-bearing query parameter replaced.
//
// The path survives, because it is the part being checked: an operator reading
// a dry run wants to see which space the message was going to, and a URL
// redacted down to nothing answers no question at all.
//
// A URL that will not parse is replaced entirely. It cannot be reasoned about,
// and the one thing certain about it is that it came from a field that holds a
// credential.
func RedactURL(raw string) string {
	if raw == "" {
		return ""
	}

	u, err := url.Parse(raw)
	if err != nil {
		return Redacted
	}

	if u.User != nil {
		// Credentials in the authority are rare and are unambiguously secret.
		u.User = url.User(Redacted)
	}

	query := u.Query()
	if len(query) == 0 {
		return u.String()
	}
	for name := range query {
		if secretParams[name] {
			query.Set(name, Redacted)
		}
	}
	u.RawQuery = query.Encode()
	return u.String()
}
