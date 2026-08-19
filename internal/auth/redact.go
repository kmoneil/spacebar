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
	"net"
	"net/url"
)

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

// IsLoopbackHost reports whether host is a loopback IP literal.
//
// It decides where the one exception to "a credential travels over https"
// applies, and it is here rather than in either caller because both
// internal/chat and this package need it and internal/chat already imports this
// one. A second copy would be a second chance for the two to disagree about
// what counts as local.
//
// An IP literal and not a name. "localhost" resolves through whatever the
// machine's resolver says, so it is not necessarily loopback at all, which is
// the same reason SPEC.md §15.4 refuses it for the OAuth listener.
func IsLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// RedactURL returns raw with every credential-bearing part replaced.
//
// The path survives, because it is the part being checked: an operator reading
// a dry run wants to see which space the message was going to, and a URL
// redacted down to nothing answers no question at all.
//
// A URL that will not parse is replaced entirely. It cannot be reasoned about,
// and the one thing certain about it is that it came from a field that holds a
// credential. The two clauses below are that same argument applied to a part
// of a URL rather than to the whole of one, and both were found by
// FuzzARedactedURLCarriesNoCredential rather than by reading this.
//
// The query is parsed here rather than read off url.URL.Query, which discards
// the parse error and answers with whatever it managed. A semicolon is the
// case that matters: url.ParseQuery refuses the whole pair one appears in, so
// "?key=SECRET;x=1" yielded no parameters at all, the nothing-to-redact path
// handed back the raw query untouched, and the credential went to the log. A
// query this cannot read is replaced whole.
//
// A fragment is replaced whenever there is one, unread. It is never sent to a
// server, so it answers no question a dry run is asked, and the OAuth implicit
// flow returns an access token in one. Both redaction layers were skipping it:
// this one looked only at the query, and internal/chat's scrub only knows the
// values it found in the same place, so a fragment on a profile's base URL
// reached both the verbose log and a dry run intact.
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

	if u.Fragment != "" || u.RawFragment != "" {
		u.Fragment, u.RawFragment = Redacted, ""
	}

	if u.RawQuery == "" {
		return u.String()
	}

	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		u.RawQuery = Redacted
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
