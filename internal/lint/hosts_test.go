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

package lint

import (
	"go/ast"
	"go/token"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// reservedTLDs cannot resolve, by RFC 2606 and RFC 6761. A request to one of
// them fails at the resolver rather than reaching somebody.
var reservedTLDs = map[string]bool{
	"invalid": true,
	"test":    true,
	"example": true,
	"local":   true,
}

// parsedNeverDialled are real hosts that appear in test literals on purpose.
//
// Each one is a fixture that gets parsed, redacted, or compared, and never
// dialled: reaching a host needs an http.Client and a server, and these are
// strings. The entry is required rather than the host being quietly permitted,
// because "this one is only parsed" is a sentence that stops being true when
// somebody writes the next test, and the entry is where they will read it.
var parsedNeverDialled = map[string]string{
	"chat.googleapis.com": "the real API host, used as a webhook URL fixture for the parsing, " +
		"redaction and same-origin rules. Nothing constructs a client for it.",

	"www.googleapis.com": "the prefix of an OAuth scope. A scope is a URL-shaped identifier and is " +
		"never fetched, by us or by Google.",

	"accounts.google.com": "the OAuth authorization host, written out once so that a test can assert " +
		"the endpoint constant has not drifted. Writing that assertion against the constant itself " +
		"would prove nothing, and nothing dials the literal.",

	"localhost": "a fixture in the test that asserts it is refused. SPEC.md §15.4 will not accept the " +
		"name for a loopback address, because it resolves through whatever the machine's resolver " +
		"says, and the test exists to prove the refusal.",
}

// TestEveryHostInATestIsUnreachable holds the rule SPEC.md §16 states as "no
// network access in tests", from the direction a test can actually break it.
//
// CI runs with egress blocked, which catches a real request there and nowhere
// else: on a developer's machine the same test passes by quietly talking to
// somebody. The realistic accident is not a deliberate call, it is a real URL
// pasted into a new fixture, which is why this reads the literals.
//
// SECURITY.md claimed this rule as "every host in a test uses a reserved TLD"
// and that was not true when it was written down: chat.googleapis.com appears
// in twenty-five test literals. The claim has been corrected to what is true
// and this is what holds it.
//
// Comments are not literals, so the Apache licence header at the top of every
// file, which names www.apache.org, is out of scope by construction.
func TestEveryHostInATestIsUnreachable(t *testing.T) {
	fset, files := repoSource(t)

	for _, f := range files {
		if !f.test {
			continue
		}

		ast.Inspect(f.syn, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.Contains(value, "://") {
				return true
			}
			checkHost(t, fset, f, lit.Pos(), value)
			return true
		})
	}
}

// trimToHostname drops trailing characters a hostname cannot contain.
//
// Chat markup writes a link as <https://url|text>, so a fixture testing that
// internal/format refuses a bracket inside the URL half parses as a host of
// "x.invalid>". The host is x.invalid; the bracket is the thing under test.
// Only trailing characters are trimmed, so a host that is strange in the middle
// is still reported.
func trimToHostname(host string) string {
	return strings.TrimRightFunc(host, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case r == '.' || r == '-':
			return false
		}
		return true
	})
}

func checkHost(t *testing.T, fset *token.FileSet, f sourceFile, pos token.Pos, value string) {
	t.Helper()

	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" {
		// Not addressed at anybody. A URL that will not parse is usually a
		// fixture for a parser, which is a test doing its job.
		return
	}

	host := trimToHostname(parsed.Hostname())
	if host == "" {
		return
	}
	if reason, ok := parsedNeverDialled[host]; ok {
		if reason == "" {
			t.Errorf("%s is allowed in tests with no reason given", host)
		}
		return
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return
		}
		t.Errorf("%s:%d names the address %s, which is not loopback.\n"+
			"A test server is on 127.0.0.1. An address that is not is an address somebody owns.",
			f.rel, fset.Position(pos).Line, host)
		return
	}

	if label := host[strings.LastIndex(host, ".")+1:]; reservedTLDs[label] {
		return
	}

	t.Errorf("%s:%d names the host %q, which can resolve.\n"+
		"A test never talks to anybody: CI blocks egress, so a real request fails there and nowhere else, "+
		"and on the machine it was written on it passes by quietly reaching somebody.\n"+
		"Use a reserved TLD (.invalid, .test, .example) or 127.0.0.1. If this host is only ever parsed "+
		"and never dialled, add it to parsedNeverDialled with that reason.",
		f.rel, fset.Position(pos).Line, host)
}
