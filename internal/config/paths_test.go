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

package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

func TestDirFollowsXDG(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)

	got, err := Dir()
	if err != nil {
		t.Fatalf("resolving the directory: %v", err)
	}
	if want := filepath.Join(base, meta.AppName); got != want {
		t.Errorf("directory is %q, want %q", got, want)
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("resolving the file: %v", err)
	}
	if want := filepath.Join(base, meta.AppName, FileName); path != want {
		t.Errorf("file is %q, want %q", path, want)
	}
}

// TestDirFallsBackToDotConfig is the decision this package makes against
// os.UserConfigDir, which would answer ~/Library/Application Support here. A
// terminal tool's config belongs where SPEC.md §5.1 says it does and where its
// owner will look for it.
func TestDirFallsBackToDotConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses %AppData%, which this case is not about")
	}

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)

	got, err := Dir()
	if err != nil {
		t.Fatalf("resolving the directory: %v", err)
	}
	if want := filepath.Join(home, ".config", meta.AppName); got != want {
		t.Errorf("directory is %q, want %q", got, want)
	}
}

// TestDirWithNowhereToLookFails covers the case where there is neither an XDG
// variable nor a home directory, which is a container with a scrubbed
// environment and is where a scheduled job usually runs.
func TestDirWithNowhereToLookFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows resolves %AppData% by another route")
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	_, err := Dir()
	wantExit(t, err, output.ExitUsage)
	if !strings.Contains(err.Error(), "XDG_CONFIG_HOME") {
		t.Errorf("the message does not say what to set:\n%v", err)
	}
}

// TestRelativeXDGIsRefused is a deliberate departure from the base directory
// specification, which says to ignore a relative value. Ignoring it puts the
// file somewhere other than where the person who set the variable expects, and
// leaves them no way to find that out.
func TestRelativeXDGIsRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/path")

	_, err := Dir()
	wantExit(t, err, output.ExitUsage)
}
