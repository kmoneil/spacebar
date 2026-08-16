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

package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
)

// cacheRoot points the cache at a directory this test owns and returns it.
func cacheRoot(t *testing.T) string {
	t.Helper()

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, err := config.CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	return dir
}

// TestACachePathCannotLeaveItsRoot.
//
// The path is built from a profile name, and what is on the other side of it is
// not a bad read: Write renames onto it and Forget removes it. So a name
// carrying a separator or a parent reference would be a rename and a delete at
// an attacker-chosen path, and the only thing standing between config.json and
// that today is three facts in three packages.
//
// The nasty cases are here because they are the ones somebody would think of.
// The property is in the fuzz target below, because the ones nobody thought of
// are the point.
func TestACachePathCannotLeaveItsRoot(t *testing.T) {
	dir := cacheRoot(t)

	for _, name := range []string{
		"..",
		".",
		"../x",
		"../../etc/passwd",
		"a/b",
		"a/../../b",
		"/etc/passwd",
		`..\windows`,
		`a\b`,
		"",
		".hidden",
		"-x",
		"_x",
		"a\x00b",
		"a b",
		"work/../../../tmp/x",
		strings.Repeat("a", 300),
	} {
		if c := NewCache(name); c != nil {
			t.Errorf("NewCache(%q) built a cache at %q", name, c.path)
		}
	}

	// And the ordinary case still works, because a check that refuses
	// everything is not a check, it is a removal.
	c := NewCache("work")
	if c == nil {
		t.Fatal("NewCache(\"work\") returned nothing")
	}
	if got := filepath.Dir(c.path); got != dir {
		t.Errorf("the cache is at %q, want a direct child of %q", c.path, dir)
	}
}

// FuzzACachePathStaysUnderItsRoot states the property rather than the cases.
//
// Either the name is refused, or the file it produces is a direct child of the
// cache directory. Nothing in between: not a subdirectory it would have to
// create, not a sibling of the root, not a path that merely begins with the
// root's characters.
func FuzzACachePathStaysUnderItsRoot(f *testing.F) {
	f.Setenv("XDG_CACHE_HOME", f.TempDir())
	dir, err := config.CacheDir()
	if err != nil {
		f.Fatalf("CacheDir: %v", err)
	}

	for _, seed := range []string{"work", "..", "../x", "a/b", "", "a.b", strings.Repeat("a", 300)} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		c := NewCache(name)
		if c == nil {
			return
		}
		if filepath.Dir(c.path) != dir {
			t.Fatalf("NewCache(%q) escaped to %q", name, c.path)
		}

		// Write puts its temporary file beside the final one, so a path that
		// stays under the root takes the rename with it.
		base := filepath.Base(c.path)
		if !strings.HasPrefix(base, "spaces-") || !strings.HasSuffix(base, ".json") {
			t.Fatalf("NewCache(%q) named a file %q", name, base)
		}
	})
}

// TestWriteAndForgetStayInsideTheRoot, because the path check at construction is
// only worth something if it is the path these two use.
func TestWriteAndForgetStayInsideTheRoot(t *testing.T) {
	dir := cacheRoot(t)

	c := NewCache("work")
	if err := c.Write([]chat.Space{{Name: "spaces/AAA", DisplayName: "Ops"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "spaces-work.json" {
		t.Fatalf("the cache directory holds %v", entries)
	}

	// The temporary file is gone rather than left beside the real one, which is
	// what os.CreateTemp would leave on a path that failed to rename.
	if err := c.Forget(); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Forget left %v behind", entries)
	}
}

// TestForgettingWhatIsNotThereIsNotAFailure.
//
// Both callers run after the irreversible part is done: the token is deleted,
// or the profile and its credential are. A second logout, or a profile that
// never resolved a display name and so never wrote a cache, must not report a
// failure for the one thing that did not need doing.
func TestForgettingWhatIsNotThereIsNotAFailure(t *testing.T) {
	cacheRoot(t)

	if err := NewCache("work").Forget(); err != nil {
		t.Errorf("Forget on a cache that was never written: %v", err)
	}
	if err := (*Cache)(nil).Forget(); err != nil {
		t.Errorf("Forget on a nil cache: %v", err)
	}
}

// TestAReusedProfileNameDoesNotInheritTheOldAccountsSpaces.
//
// A profile name is reusable, and the cache is keyed by it. Remove a profile,
// configure one with the same name for a different account, and without a
// Forget the old account's space list answers the new account's lookups until
// the TTL runs out. The Profile field inside the file cannot catch it: it was
// written for a file that reached the wrong path, and here the name matches
// because it is the same name.
func TestAReusedProfileNameDoesNotInheritTheOldAccountsSpaces(t *testing.T) {
	cacheRoot(t)

	first := NewCache("work")
	first.now = func() time.Time { return fixedNow }
	if err := first.Write([]chat.Space{{Name: "spaces/OLD", DisplayName: "Ops"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// What `profile rm` and `auth logout` now do.
	if err := first.Forget(); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	second := NewCache("work")
	second.now = func() time.Time { return fixedNow }
	if spaces, ok := second.Read(); ok {
		t.Errorf("a profile of the same name read %d spaces belonging to the account before it", len(spaces))
	}
}
