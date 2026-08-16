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

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAServerSuppliedFilenameCannotLeaveTheDirectory.
//
// The claim this card is really about. An attachment's name is chosen by
// whoever posted the message, and it is joined onto a directory the operator
// named, so "../../.ssh/authorized_keys" is a filename as far as the API is
// concerned and a write outside the tree as far as the operator is concerned.
//
// Stated as a property over the join rather than as a list of the separators
// somebody thought of: whatever the name, the file is a direct child of the
// directory that was asked for.
func TestAServerSuppliedFilenameCannotLeaveTheDirectory(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{
		"../../.ssh/authorized_keys",
		"../../../etc/passwd",
		"/etc/passwd",
		"/",
		"..",
		".",
		"",
		"   ",
		"....//....//etc/passwd",

		// A Windows separator on a Unix host. filepath.Base leaves it whole
		// here, because a backslash is an ordinary character in a Unix
		// filename, so the escape it would be on Windows is a strange name
		// here. Replaced anyway.
		`..\..\evil.txt`,
		`C:\Windows\System32\drivers\etc\hosts`,

		// A NUL, which no filesystem takes and which some C library on the
		// other side of a syscall might truncate at.
		"quiet\x00.txt",

		// Ordinary names, which have to survive: a check that refuses
		// everything is not a check.
		"report.pdf",
		"a name with spaces.txt",
		"ünïcödé.txt",
		".hidden",
	} {
		got := filepath.Join(dir, SafeFilename(name))

		if filepath.Dir(got) != dir {
			t.Errorf("%q landed at %q, outside %q", name, got, dir)
		}
		if strings.Contains(filepath.Base(got), "/") || strings.Contains(filepath.Base(got), `\`) {
			t.Errorf("%q kept a separator: %q", name, filepath.Base(got))
		}
		if strings.ContainsRune(filepath.Base(got), 0) {
			t.Errorf("%q kept a NUL: %q", name, filepath.Base(got))
		}
	}

	// And the ordinary ones are still themselves, because a defence that
	// renames every download is one somebody turns off.
	//
	// A hostile name keeps its shape rather than being reduced to its last
	// element, and that is deliberate. Taking the base name would write
	// "authorized_keys", which is safe and looks like something somebody meant
	// to send; flattening the separators writes ".._.._.ssh_authorized_keys",
	// which is safe and says what arrived. It is also the same answer on every
	// platform: a backslash is a separator on Windows and an ordinary character
	// on Unix, so a base name would differ between them for one input.
	for name, want := range map[string]string{
		"report.pdf":                 "report.pdf",
		"a name with spaces.txt":     "a name with spaces.txt",
		".hidden":                    ".hidden",
		"../../.ssh/authorized_keys": ".._.._.ssh_authorized_keys",
		"..\\..\\evil.txt":           ".._.._evil.txt",
	} {
		if got := SafeFilename(name); got != want {
			t.Errorf("SafeFilename(%q) = %q, want %q", name, got, want)
		}
	}
}

// FuzzASafeFilenameIsAlwaysOneNameInTheDirectory states it as a property over
// arbitrary input, because the cases above are the ones somebody thought of.
func FuzzASafeFilenameIsAlwaysOneNameInTheDirectory(f *testing.F) {
	for _, seed := range []string{
		"report.pdf", "../../etc/passwd", `..\..\evil`, "", ".", "..", "/", "a\x00b",
	} {
		f.Add(seed)
	}

	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, name string) {
		safe := SafeFilename(name)

		if safe == "" || safe == "." || safe == ".." {
			t.Fatalf("SafeFilename(%q) = %q, which is a path element rather than a name", name, safe)
		}
		if strings.ContainsAny(safe, `/\`) || strings.ContainsRune(safe, 0) {
			t.Fatalf("SafeFilename(%q) = %q", name, safe)
		}
		if got := filepath.Join(dir, safe); filepath.Dir(got) != dir {
			t.Fatalf("SafeFilename(%q) landed at %q, outside %q", name, got, dir)
		}
	})
}
