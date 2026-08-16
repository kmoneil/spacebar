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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/resolve"
)

// cached writes a space list for one profile and returns the path it landed on,
// standing in for a command that resolved a display name earlier in the day.
func cached(t *testing.T, name string) string {
	t.Helper()

	cache := resolve.NewCache(name)
	if cache == nil {
		t.Fatalf("no cache for profile %q", name)
	}
	if err := cache.Write([]chat.Space{{Name: "spaces/AAA", DisplayName: "Ops"}}); err != nil {
		t.Fatalf("writing the cache: %v", err)
	}

	dir, err := config.CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	path := filepath.Join(dir, "spaces-"+name+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the cache was not written where it was expected: %v", err)
	}
	return path
}

// TestLoggingOutLeavesNoSpaceListBehind.
//
// The cached list holds the display name of every space that profile could
// reach. Deleting the token and leaving that on disk is a logout that removed
// the part somebody could see and kept the part they could not, and nothing in
// the output would have said so.
func TestLoggingOutLeavesNoSpaceListBehind(t *testing.T) {
	isolate(t)
	authorized(t, "work", time.Hour)
	path := cached(t, "work")

	got := runCLIIn(t, "", "auth", "logout")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the cached space list survived a logout: %v", err)
	}
}

// TestRemovingAProfileLeavesNoSpaceListBehind.
//
// The sharper of the two, because a profile name is reusable and the cache is
// keyed by it. Remove a profile, configure one with the same name for a
// different account, and until the day-long TTL runs out a display name
// resolves against spaces the new account may not be able to see. The Profile
// field written inside the file cannot catch it: that guard is for a file that
// reached the wrong path, and here the name matches because it is the same name.
func TestRemovingAProfileLeavesNoSpaceListBehind(t *testing.T) {
	isolate(t)
	authorized(t, "work", time.Hour)
	path := cached(t, "work")

	got := runCLIIn(t, "", "profile", "rm", "work", "--yes")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the cached space list survived the profile: %v", err)
	}
}

// TestACacheThatCannotBeRemovedIsAWarningRatherThanAFailure.
//
// By the time this runs, the irreversible part is done: the token is gone, or
// the profile and the credential behind it are. Reporting a non-zero exit for
// the file that is left would tell a script the removal failed when the part
// that cannot be undone succeeded. Saying nothing would leave somebody
// believing a file is gone when it is on disk. So it is a warning, on stderr,
// naming the profile.
func TestACacheThatCannotBeRemovedIsAWarningRatherThanAFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permission this test depends on")
	}

	isolate(t)
	authorized(t, "work", time.Hour)
	path := cached(t, "work")

	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	got := runCLIIn(t, "", "auth", "logout")
	if got.exit != output.ExitOK {
		t.Fatalf("a cache that could not be removed failed the logout: exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "cached space list") {
		t.Errorf("nothing said the space list is still there:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "work") {
		t.Errorf("the warning does not name the profile:\n%s", got.stderr)
	}
}
