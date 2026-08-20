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

package format

import "testing"

// This file measures the other half of the render path, for the reason
// internal/output/bench_test.go measures the first: until 2026-08-20 the index
// was the only benchmarked thing in the tree, and a body goes through here on
// its way out.
//
// It is per message rather than per record, so the numbers matter less than
// store's do, and there is one place they could matter a great deal: a body may
// be up to 32,117 bytes, which is what CheckMessageText refuses above, and the
// scanners here are all linear over it.
//
// Validate runs on every send, with or without --md, so it is measured on its
// own. Translate runs only when the caller asked for it.
//
// # What it measured, 2026-08-20
//
// linux/arm64, 12 cores, Go 1.26.6, in a container on a shared host, six
// samples of each in one process:
//
//	Validate, 68 bytes            0.065us to 0.070us    0 allocs
//	Validate, about 32,000 bytes  31us to 33us          0 allocs
//	Translate plain               0.35us to 0.48us      2 allocs, 96 B
//	Translate marked              0.55us to 0.58us      6 allocs, 200 B
//	Translate fenced              0.40us to 0.42us      6 allocs, 256 B
//	Translate list and table      1.33us to 1.46us     20 allocs, 912 B
//
// So a message at the API's limit costs about thirty microseconds to validate
// and allocates nothing doing it, and translation is a few microseconds at
// most. Neither is worth optimizing today. What the file is for is that a
// change which made either of them quadratic would now be visible, and the
// largest legal body is where that would show up first.
//
// Read the noise floor in internal/store/bench_test.go before arguing from a
// wall-clock number here. Allocation counts are the stable metric on this host.

// sink is where a benchmark's result goes, so the compiler cannot delete the
// work that produced it.
var sink string

// benchBodies are the shapes a message body arrives in.
var benchBodies = map[string]string{
	"plain":  "deploy rolled back at 14:02, see the runbook for the follow-up steps",
	"marked": "**deploy** rolled back at `14:02`, see the [runbook](https://runbook.example/x) and *ping* the on-call",
	"fenced": "before\n```\nfor i := range xs { **not markup** }\n```\nafter **bold**",
	"table":  "# Heading\n\n- one **bold**\n- two `code`\n- three [link](https://x.example)\n\n| a | b |\n| - | - |\n| 1 | 2 |",
}

// BenchmarkValidate is the UTF-8 scan every send pays for, at a realistic size
// and at the largest body the API accepts.
//
// The large case is built from multi-byte runes rather than ASCII, because a
// scan that decodes is the thing being measured and an ASCII body is the case
// where DecodeRuneInString does the least work.
func BenchmarkValidate(b *testing.B) {
	cases := map[string]string{
		"message":  benchBodies["plain"],
		"at_limit": largeBody(32_000),
	}

	for _, name := range []string{"message", "at_limit"} {
		in := cases[name]

		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			err := error(nil)
			for b.Loop() {
				err = Validate(in)
			}
			b.StopTimer()

			if err != nil {
				b.Fatalf("the fixture does not validate, so this measured the error path: %v", err)
			}
		})
	}
}

// BenchmarkTranslate is the cost of --md over the shapes that exercise
// different scanners: none, inline markup, a fenced block that must come
// through untouched, and a document with a heading, a list and a table.
func BenchmarkTranslate(b *testing.B) {
	for _, name := range []string{"plain", "marked", "fenced", "table"} {
		in := benchBodies[name]

		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			out, err := "", error(nil)
			for b.Loop() {
				out, _, err = Translate(in)
			}
			b.StopTimer()

			if err != nil {
				b.Fatalf("the fixture does not translate, so this measured the error path: %v", err)
			}
			if out == "" {
				b.Fatal("the translation is empty, so this measured a loop that did no work")
			}
			sink = out
		})
	}
}

// largeBody builds a body of about n bytes out of multi-byte runes.
//
// Sized in bytes rather than in runes because that is what the API's limit is
// measured in: 32,117 was refused and 32,017 was accepted, and an emoji body
// crossed at the same byte count.
func largeBody(n int) string {
	const unit = "déployé à 14:02 par l'équipe, voir le runbook 👩‍🚀\n"

	out := make([]byte, 0, n+len(unit))
	for len(out) < n {
		out = append(out, unit...)
	}
	return string(out)
}
