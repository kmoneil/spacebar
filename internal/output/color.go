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

package output

// The whole of the colour support, as SPEC.md §11.3 asks for: a handful of raw
// ANSI constants and one function that wraps a string in them. A dependency
// would bring a renderer, a style system, and a colour profile detector, for
// six escape sequences that have not changed since the 1970s.
//
// This is deliberately the only place in this tool that writes an escape
// sequence on purpose. Everywhere else, an escape sequence in a string is
// somebody else's program trying to run on the reader's screen, and Sanitize
// takes it apart.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiYellow = "\x1b[33m"
)

// paint wraps s in an ANSI code, when colour is on.
//
// It is called on labels, prefixes, and headings, and never on a value that
// came from anywhere but this source tree. That is what keeps colour from being
// a way back in: data reaches a stream through Sanitize or Cell, which turn an
// escape sequence into visible text, and the only strings that keep their
// escapes are the constants above.
func (r *Renderer) paint(code, s string) string {
	if !r.opts.Color {
		return s
	}
	return code + s + ansiReset
}
