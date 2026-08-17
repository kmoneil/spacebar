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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
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
		"NUL", "COM1", "report.txt:hidden", "C:evil",
	} {
		f.Add(seed)
	}

	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, name string) {
		safe := SafeFilename(name)

		if safe == "" || safe == "." || safe == ".." {
			t.Fatalf("SafeFilename(%q) = %q, which is a path element rather than a name", name, safe)
		}
		if strings.ContainsAny(safe, `/\:`) || strings.ContainsRune(safe, 0) {
			t.Fatalf("SafeFilename(%q) = %q", name, safe)
		}
		if got := filepath.Join(dir, safe); filepath.Dir(got) != dir {
			t.Fatalf("SafeFilename(%q) landed at %q, outside %q", name, got, dir)
		}

		// The lexical layer under all of it, and the one that carries the
		// Windows half: filepath.IsLocal is what refuses a reserved device
		// name there. On Unix it refuses almost nothing this has not already
		// handled, so this assertion is weak where it runs and is the whole
		// claim where it does not.
		if !filepath.IsLocal(safe) {
			t.Fatalf("SafeFilename(%q) = %q, which filepath.IsLocal refuses", name, safe)
		}
	})
}

// TestADownloadWillNotFollowASymlinkOutOfTheDirectory is the claim SafeFilename
// does not make and this used to get wrong.
//
// The name cannot leave the directory, which the two tests above hold. What
// nothing held is that the **write** cannot: the old code asked os.Stat whether
// the path existed and then called os.WriteFile, and os.Stat follows symlinks,
// so a dangling link answered ErrNotExist, the guard passed, and the write
// followed the same link to wherever it pointed. The bytes are chosen by
// whoever posted the message.
//
// Every case is run twice, with and without --force, because they take
// different paths through writeInto and only one of them was ever going to be
// remembered: without --force an O_EXCL open refuses anything already there,
// and with --force a temporary file is renamed over the name, which replaces it
// rather than following it.
func TestADownloadWillNotFollowASymlinkOutOfTheDirectory(t *testing.T) {
	const attachment = "report.pdf"

	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, dir, outside string)
	}{
		{
			// The one that needs no race at all. os.Stat on a link whose target
			// does not exist answers ErrNotExist, which reads as "nothing is
			// there" and is not.
			name: "a dangling symlink pointing out of the directory",
			plant: func(t *testing.T, dir, outside string) {
				link(t, filepath.Join(outside, "secret.txt"), filepath.Join(dir, attachment))
			},
		},
		{
			name: "a live symlink pointing out of the directory",
			plant: func(t *testing.T, dir, outside string) {
				write(t, filepath.Join(outside, "secret.txt"), "ORIGINAL")
				link(t, filepath.Join(outside, "secret.txt"), filepath.Join(dir, attachment))
			},
		},
		{
			// Inside the root, so os.Root permits the traversal and the refusal
			// has to come from somewhere else. Without --force that is O_EXCL;
			// with it, the rename replaces the link rather than writing through
			// it, so the sibling survives either way.
			name: "a symlink to a sibling inside the directory",
			plant: func(t *testing.T, dir, _ string) {
				write(t, filepath.Join(dir, "sibling.txt"), "ORIGINAL")
				link(t, "sibling.txt", filepath.Join(dir, attachment))
			},
		},
	} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s force=%v", tc.name, force), func(t *testing.T) {
				base := t.TempDir()
				dir, outside := filepath.Join(base, "shared"), filepath.Join(base, "outside")
				mkdir(t, dir)
				mkdir(t, outside)
				tc.plant(t, dir, outside)

				root, err := os.OpenRoot(dir)
				if err != nil {
					t.Fatalf("OpenRoot: %v", err)
				}
				t.Cleanup(func() { _ = root.Close() })

				path := filepath.Join(dir, attachment)
				err = writeInto(root, attachment, path, []byte("ATTACHMENT BYTES"), force)

				// Whether the write is refused or replaces the link is the
				// implementation's business. What is asserted is the property:
				// nothing outside the directory changed, and no file the
				// operator already had was written through.
				for _, kept := range []string{
					filepath.Join(outside, "secret.txt"),
					filepath.Join(dir, "sibling.txt"),
				} {
					if body, readErr := os.ReadFile(kept); readErr == nil && body != nil &&
						string(body) != "ORIGINAL" {
						t.Errorf("the download wrote through the link into %s: %q (err=%v)",
							kept, body, err)
					}
				}
				if _, statErr := os.Lstat(filepath.Join(outside, "secret.txt")); statErr == nil &&
					tc.name == "a dangling symlink pointing out of the directory" {
					t.Errorf("the download created a file outside the directory (err=%v)", err)
				}

				// And whatever is at the name afterwards is a real file rather
				// than still a link, or the write was refused outright.
				if err == nil {
					fi, statErr := os.Lstat(path)
					if statErr != nil {
						t.Fatalf("Lstat after a write that reported success: %v", statErr)
					}
					if fi.Mode()&os.ModeSymlink != 0 {
						t.Errorf("%s is still a symlink after the write", path)
					}
				}
			})
		}
	}
}

// TestADownloadStillWritesAndStillRefusesAFileYouHave holds the other half.
//
// A containment fix that refused every download would pass the test above and
// be useless, so the ordinary paths are asserted beside it: a new name is
// written, a name already taken is refused without --force and overwritten with
// it, and the mode is the one config sets.
func TestADownloadStillWritesAndStillRefusesAFileYouHave(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	path := filepath.Join(dir, "report.pdf")
	if err := writeInto(root, "report.pdf", path, []byte("FIRST"), false); err != nil {
		t.Fatalf("writing a name nothing holds: %v", err)
	}
	if body := read(t, path); body != "FIRST" {
		t.Errorf("the file says %q", body)
	}
	if mode := stat(t, path).Mode().Perm(); mode != config.FileMode {
		t.Errorf("mode = %#o, want %#o", mode, config.FileMode)
	}

	// The name came from the message rather than from the operator, so a second
	// attachment must not quietly replace the first.
	err = writeInto(root, "report.pdf", path, []byte("SECOND"), false)
	if err == nil {
		t.Fatal("a name already taken was overwritten without --force")
	}
	if got := output.ExitCodeOf(err); got != output.ExitUsage {
		t.Errorf("exit = %d, want %d", got, output.ExitUsage)
	}
	if body := read(t, path); body != "FIRST" {
		t.Errorf("the refused write changed the file to %q", body)
	}

	if err := writeInto(root, "report.pdf", path, []byte("SECOND"), true); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if body := read(t, path); body != "SECOND" {
		t.Errorf("--force left %q", body)
	}
	if mode := stat(t, path).Mode().Perm(); mode != config.FileMode {
		t.Errorf("mode after --force = %#o, want %#o", mode, config.FileMode)
	}

	// Nothing staged is left behind. An interrupted overwrite is allowed to
	// leave one; a successful one is not, because it would sit in somebody's
	// download directory for good.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Errorf("a staged file was left behind: %s", e.Name())
		}
	}
}

// TestALongNameStillWritesUnderForce holds the band this change nearly broke.
//
// The staged name is the prefix plus the real one, and a name that fits a
// filesystem on its own does not once the prefix is in front of it. That band
// is narrow, 236 to 255 bytes here, and it exists only under --force, which is
// exactly the shape of regression nobody finds until somebody has an attachment
// with a long name.
//
// Asserted against the real filesystem rather than against the arithmetic,
// because the arithmetic is the thing that was wrong.
func TestALongNameStillWritesUnderForce(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	for _, n := range []int{200, 235, 236, 240, 255} {
		name := strings.Repeat("a", n-len(".pdf")) + ".pdf"
		if len(name) != n {
			t.Fatalf("test built a %d byte name for %d", len(name), n)
		}
		if staged := stagedName(name); len(staged) > maxFilenameBytes {
			t.Errorf("stagedName for a %d byte name is %d bytes", n, len(staged))
		}

		path := filepath.Join(dir, name)
		if err := writeInto(root, name, path, []byte("FIRST"), false); err != nil {
			t.Fatalf("a %d byte name: %v", n, err)
		}
		if err := writeInto(root, name, path, []byte("SECOND"), true); err != nil {
			t.Errorf("--force on a %d byte name: %v", n, err)
		}
		if body := read(t, path); body != "SECOND" {
			t.Errorf("a %d byte name holds %q", n, body)
		}
	}
}

// TestAStagedNameIsCutOnARuneBoundary.
//
// The truncation is on bytes, so a multi-byte character at the cut is the case
// to get right: half of one is not a character, and the name may be looked at
// by somebody wondering what an interrupted download left behind.
func TestAStagedNameIsCutOnARuneBoundary(t *testing.T) {
	for _, name := range []string{
		strings.Repeat("é", 200),
		strings.Repeat("😀", 100),
		strings.Repeat("a", 250) + "é",
		strings.Repeat("a", 253) + "😀",
	} {
		staged := stagedName(name)
		if len(staged) > maxFilenameBytes {
			t.Errorf("staged name for %d bytes is %d", len(name), len(staged))
		}
		if !utf8.ValidString(staged) {
			t.Errorf("staged name for a %d byte name is not valid UTF-8: %q", len(name), staged)
		}
		if !strings.HasPrefix(staged, tempPrefix) {
			t.Errorf("staged name lost its prefix: %q", staged)
		}
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll %s: %v", path, err)
	}
}

func link(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink %s -> %s: %v", path, target, err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(body)
}

func stat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	return fi
}
