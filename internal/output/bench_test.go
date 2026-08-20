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

import "testing"

// This file exists because escape is on the path of every value this tool
// prints and nothing measured it until 2026-08-20.
//
// internal/store was the only benchmarked package, and it measures the index:
// reading a file, decoding a line, resolving supersession, sorting. The
// rendering path is a different one, it runs per cell rather than per record,
// and a regression in it was invisible to `make bench` entirely.
//
// The first run answered a question that had been settled by a comment. grow
// says the common case of text needing nothing never allocates, and it did
// allocate, 32 bytes every call, because fmt.Fprintf(&b, ...) in the branch for
// an invalid byte converted the Builder to an io.Writer and sent it to the
// heap. See writeHexByte. The comment was right about the intent and wrong
// about the code for six milestones, and one benchmark was enough to tell.
//
// # What it measured, 2026-08-20
//
// linux/arm64, 12 cores, Go 1.26.6, in a container on a shared host. Six
// samples of each in one process, allocations per call in the second column:
//
//	clean ascii, 68 bytes         0.77us to 0.81us     0 allocs
//	clean unicode, 75 bytes       0.65us to 0.81us     0 allocs
//	tab and newline, 28 bytes     Cell 0.29us to 0.33us, 1 alloc, 48 B
//	                              Sanitize 0.21us to 0.23us, 0 allocs
//	hostile, 47 bytes             0.85us to 2.2us     12 allocs, 256 B
//
// So a clean cell costs well under a microsecond and allocates nothing. The
// allocations that remain are one per escaped rune, from the fmt.Sprintf calls
// in escapeRune, and they are left alone deliberately: that is the path of text
// that is trying something, and it is worth spending on.
//
// Cell and Sanitize come apart only on tab_newline, which is the whole
// difference between them: strict escapes tab and newline so a value cannot
// forge a column or a row, and the escape is what costs the allocation. On
// every other case they are one boolean apart and measure the same.
//
// # A note on the wall-clock numbers above
//
// A count=1 run of these benchmarks ninety seconds earlier reported 0.50us for
// clean ascii against the 0.77us here, from the same binary. That is 1.6x from
// the host, and it is the noise floor in internal/store/bench_test.go arriving
// in a second package rather than an anomaly. Do not argue from these
// microseconds. The allocation counts did not move by one across either run,
// and those are what this file is for.
//
// sink is where a benchmark's result goes, so the compiler cannot delete the
// work that produced it.
var sink string

// benchInputs are the shapes a cell actually arrives in.
//
// Hostile is one string rather than four, because a body that carries an ANSI
// sequence is the kind that also carries a bidi override: the cost being
// measured is a scan that keeps finding things, not one escape in isolation.
var benchInputs = map[string]string{
	"clean_ascii":   "deploy rolled back at 14:02, see the runbook for the follow-up steps",
	"clean_unicode": "déployé à 14:02 par l'équipe 👩‍🚀, voir le runbook pour la suite",
	"tab_newline":   "rollback\tdone\nnext: redeploy",
	"hostile":       "rollback\x1b[31m done\x1b[0m \u202egnirts\u202c \U000E0041\U000E0042 \xff",
}

// BenchmarkEscape measures both exported entry points over the same inputs.
//
// Cell and Sanitize differ by one boolean and are benchmarked together so that
// a change which speeds one up by slowing the other down cannot look like a
// win.
func BenchmarkEscape(b *testing.B) {
	for _, name := range []string{"clean_ascii", "clean_unicode", "tab_newline", "hostile"} {
		in := benchInputs[name]

		b.Run("Cell/"+name, func(b *testing.B) {
			b.ReportAllocs()
			out := ""
			for b.Loop() {
				out = Cell(in)
			}
			b.StopTimer()
			sink = out
		})

		b.Run("Sanitize/"+name, func(b *testing.B) {
			b.ReportAllocs()
			out := ""
			for b.Loop() {
				out = Sanitize(in)
			}
			b.StopTimer()
			sink = out
		})
	}
}
