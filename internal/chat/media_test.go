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
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

// gifBytes is a valid 1x1 GIF89a.
//
// Small on purpose: this exercises the sniff, which reads at most the first 512
// bytes, and a file shorter than that window is the case where Peek returns
// what it has alongside io.EOF.
var gifBytes = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
	0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02,
	0x44, 0x01, 0x00, 0x3b,
}

// pngBytes is the eight-byte PNG signature, which is all the sniff table reads.
var pngBytes = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00}

// parts pulls the metadata and media parts back out of a built body.
func parts(t *testing.T, body []byte, contentType string) (metadata map[string]string, mediaType string, media []byte) {
	t.Helper()

	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("the body's own content type does not parse: %v", err)
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])

	first, err := reader.NextPart()
	if err != nil {
		t.Fatalf("reading the metadata part: %v", err)
	}
	raw, err := io.ReadAll(first)
	if err != nil {
		t.Fatalf("reading the metadata part: %v", err)
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("the metadata part is not JSON: %v", err)
	}

	second, err := reader.NextPart()
	if err != nil {
		t.Fatalf("reading the media part: %v", err)
	}
	media, err = io.ReadAll(second)
	if err != nil {
		t.Fatalf("reading the media part: %v", err)
	}
	return metadata, second.Header.Get("Content-Type"), media
}

// TestAnUploadDeclaresWhatTheBytesActuallyAre.
//
// The media part's Content-Type is not a formality. Measured against a real
// space on 2026-08-18: the attachment's contentType is this header echoed back,
// so declaring everything application/octet-stream is what made every image
// this tool uploaded arrive as a generic file card rather than as a picture.
//
// Sniffed from the bytes rather than from the filename extension, so the
// filename here says .bin while the bytes say GIF. The extension is the half a
// caller can get wrong, and mime.TypeByExtension would also answer from the
// machine's own mime database, which differs across the six platforms this
// builds for.
func TestAnUploadDeclaresWhatTheBytesActuallyAre(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  []byte
		want  string
		named string
	}{
		{name: "a GIF", body: gifBytes, want: "image/gif", named: "anything.bin"},
		{name: "a PNG", body: pngBytes, want: "image/png", named: "anything.bin"},

		// The unrecognised case is the whole of the old behaviour, and it has
		// to stay exactly what it was: this change must not alter a single
		// upload whose bytes nothing can name.
		{name: "bytes nothing recognises", body: []byte{0x00, 0x01, 0x02, 0x03}, want: "application/octet-stream", named: "blob.dat"},

		// An empty file sniffs as text/plain, which would be a claim about a
		// file with nothing in it. It stays opaque.
		{name: "an empty file", body: nil, want: "application/octet-stream", named: "empty.dat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, contentType, err := multipartBody(UploadRequest{
				Space:    "spaces/AAA",
				Filename: tc.named,
				Body:     bytes.NewReader(tc.body),
			})
			if err != nil {
				t.Fatalf("multipartBody: %v", err)
			}

			metadata, mediaType, media := parts(t, body, contentType)
			if mediaType != tc.want {
				t.Errorf("the media part says %q, want %q", mediaType, tc.want)
			}
			if !bytes.Equal(media, tc.body) {
				t.Errorf("the file that was sent is not the file that was given:\n got %x\nwant %x", media, tc.body)
			}
			if metadata["filename"] != tc.named {
				t.Errorf("filename = %q, want %q", metadata["filename"], tc.named)
			}
		})
	}
}

// TestACallerCanOverrideWhatAnUploadIsDeclaredAs.
//
// The escape hatch, and it exists because of a measurement rather than for
// symmetry. Declaring the true type makes Chat validate the bytes against it: a
// 1x1 GIF uploads as application/octet-stream and is refused with 400 as
// image/gif, at 44 bytes and at 10KB alike. Sniffing calls that file image/gif
// too, correctly, so nothing this package chooses avoids the refusal and the
// only way through is to say something else on purpose.
func TestACallerCanOverrideWhatAnUploadIsDeclaredAs(t *testing.T) {
	body, contentType, err := multipartBody(UploadRequest{
		Space:       "spaces/AAA",
		Filename:    "one.gif",
		Body:        bytes.NewReader(gifBytes),
		ContentType: "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("multipartBody: %v", err)
	}

	_, mediaType, media := parts(t, body, contentType)
	if mediaType != "application/octet-stream" {
		t.Errorf("the media part says %q; a caller's own answer was overridden by the sniff", mediaType)
	}
	if !bytes.Equal(media, gifBytes) {
		t.Error("the file that was sent is not the file that was given")
	}
}

// TestSniffingDoesNotEatTheStartOfTheFile.
//
// The sniff reads the first 512 bytes and the copy has to send them anyway,
// which is what bufio.Peek is for and is the one way this change could corrupt
// every upload silently. A file longer than the sniff window is the case that
// would show it: a reader consumed rather than peeked loses exactly the first
// 512 bytes and still produces a plausible-looking body.
func TestSniffingDoesNotEatTheStartOfTheFile(t *testing.T) {
	large := append(append([]byte(nil), gifBytes...), bytes.Repeat([]byte("x"), 4096)...)

	body, contentType, err := multipartBody(UploadRequest{
		Space:    "spaces/AAA",
		Filename: "one.gif",
		Body:     bytes.NewReader(large),
	})
	if err != nil {
		t.Fatalf("multipartBody: %v", err)
	}

	_, mediaType, media := parts(t, body, contentType)
	if mediaType != "image/gif" {
		t.Errorf("the media part says %q, want image/gif", mediaType)
	}
	if len(media) != len(large) {
		t.Fatalf("sent %d bytes of a %d byte file", len(media), len(large))
	}
	if !bytes.Equal(media, large) {
		t.Error("the bytes sent are not the bytes given, which is what a consumed reader looks like")
	}
}

// TestTheBodyIsRelatedRatherThanFormData.
//
// The endpoint refuses form-data, and the boundary has to be the one the writer
// generated. Asserted here because this is now the only test that reads a built
// body, so a change to the shape would otherwise be caught by nothing until it
// reached the API.
func TestTheBodyIsRelatedRatherThanFormData(t *testing.T) {
	_, contentType, err := multipartBody(UploadRequest{
		Space:    "spaces/AAA",
		Filename: "one.gif",
		Body:     bytes.NewReader(gifBytes),
	})
	if err != nil {
		t.Fatalf("multipartBody: %v", err)
	}
	if !strings.HasPrefix(contentType, "multipart/related; boundary=") {
		t.Errorf("content type = %q, want multipart/related with a boundary", contentType)
	}
}
