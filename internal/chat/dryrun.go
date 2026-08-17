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

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/kmoneil/spacebar/internal/auth"
)

// Preview is the request that would have been sent, redacted (SPEC.md §10.2).
//
// It is produced from the request the client actually built, not from a second
// description of one. That is the difference between a dry run and a
// well-informed guess: a parallel representation can drift from what would go
// on the wire, and it drifts silently, which makes it worse than printing
// nothing at all.
type Preview struct {
	// DryRun marks the envelope. The shape is already unmistakable, since a
	// real result has none of these fields, but a consumer branching on
	// .dry_run is clearer than one branching on the presence of .method, and
	// this is the field they will reach for.
	DryRun bool `json:"dry_run"`

	Method string `json:"method"`

	// URL has its credential-bearing query parameters replaced. The path
	// survives, because the space is what somebody running a dry run is
	// checking.
	URL string `json:"url"`

	// Headers are sorted, and the ones that carry a credential read REDACTED.
	// Sorted because a map is not, and a golden file that reordered itself
	// between runs would be a contract nobody could hold.
	Headers map[string]string `json:"headers"`

	// Body is the exact bytes that would be sent, kept raw so that they are
	// exact and still queryable. Re-encoding them would make the output a
	// description of the body rather than the body.
	//
	// The one exception is a request that carries a file, whose body is the
	// file. That one is described with its exact size instead. See previewBody.
	Body json.RawMessage `json:"body,omitempty"`
}

// DryRun is returned in place of sending, and carries what would have been
// sent.
//
// An error rather than a second return value, because every path out of the
// client already handles one and a caller who ignores it gets a loud failure
// rather than a message they think was delivered. A caller who handles it
// prints the request and exits 0.
type DryRun struct {
	Request *Preview
}

func (d *DryRun) Error() string {
	return "dry run: the request below was not sent"
}

// Text renders the preview the way an HTTP request is written, because that is
// what it is and because somebody comparing it against the API documentation is
// reading the same shape there.
func (p *Preview) Text() string {
	var b strings.Builder

	b.WriteString(p.Method)
	b.WriteString(" ")
	b.WriteString(p.URL)
	b.WriteString("\n")

	for _, name := range slices.Sorted(maps.Keys(p.Headers)) {
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(p.Headers[name])
		b.WriteString("\n")
	}

	if len(p.Body) > 0 {
		// A blank line between headers and body, as in the protocol.
		b.WriteString("\n")
		b.Write(p.Body)
		b.WriteString("\n")
	}
	return b.String()
}

// preview builds the redacted description of a request.
//
// Redaction happens here, in the package that built the request, and not
// wherever it is printed. SPEC.md §15.1 is explicit about that ordering, and
// the reason is that a formatter which is handed a token has already been
// handed a token: whether it prints it is then a matter of that formatter's
// care, and there will be more than one formatter.
func (c *Client) preview(req *http.Request, body []byte) *Preview {
	headers := make(map[string]string, len(req.Header))
	for name := range req.Header {
		headers[name] = c.scrub(redactHeader(name, req.Header.Get(name)))
	}

	return &Preview{
		DryRun:  true,
		Method:  req.Method,
		URL:     c.scrub(auth.RedactURL(req.URL.String())),
		Headers: headers,
		Body:    previewBody(body),
	}
}

// previewBody keeps the request body exactly as it would be sent, unless it is
// a file.
//
// The trailing newline the JSON encoder writes is trimmed, because it is an
// artefact of the encoder rather than part of the document, and a golden that
// recorded it would be recording the encoder.
//
// The other branch is the decision this function's comment deferred. It used to
// say a non-JSON body "is not something this tool sends yet. When media upload
// arrives it will be multipart and megabytes, and printing it verbatim would be
// the wrong answer; that is a decision for the card that adds it." Media upload
// arrived in Milestone 4 and the decision was not taken, so what was left was
// the fallback: json.Marshal of the whole multipart document, which for a
// two-hundred-megabyte attachment is a two-hundred-megabyte line on somebody's
// terminal. Nobody saw it, because the command that produces one exited before
// printing anything at all, which is the bug this arrived with.
//
// It is described instead, with its exact size. Printing it is not showing a
// request, it is copying a file to stdout, and the part of an upload a person
// runs a dry run to check is the method, the URL and the headers rather than
// the bytes they already have on disk.
//
// Described rather than truncated, and the difference is the whole of why this
// is allowed. The rule is that a value is never *silently* altered; a count
// that says how many bytes it stands in for is not silent, and there is no
// prefix of a multipart document that would be more useful than the number.
func previewBody(body []byte) json.RawMessage {
	trimmed := strings.TrimRight(string(body), "\n")
	if trimmed == "" {
		return nil
	}
	if !json.Valid([]byte(trimmed)) {
		return elidedBody(len(body))
	}
	return json.RawMessage(trimmed)
}

// elidedBody stands in for a body that is a file rather than a document.
//
// A JSON string, so that --json output stays one parseable object and a
// consumer reading .body gets something it can print rather than a shape it has
// to branch on. The angle brackets are what mark it as a placeholder rather
// than as something the body contained.
//
// Encoded with HTML escaping off, for the reason encodeBody and the renderer
// both turn it off: json.Marshal writes < and > as < and >, which is
// lossless and unreadable, and this string exists to be read.
func elidedBody(size int) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(fmt.Sprintf("<%d bytes, not shown: this request carries a file>", size)); err != nil {
		// Unreachable: the argument is a string this function built.
		return nil
	}
	return json.RawMessage(strings.TrimRight(buf.String(), "\n"))
}
