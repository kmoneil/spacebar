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
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
)

// TTL is how long a cached space list is used before being fetched again.
//
// Listing spaces costs a request against a per-space quota shared with every
// other app acting in those spaces, so a resolver that listed on every command
// would degrade the space for everybody to save somebody typing a resource
// name. A day is long enough that the common case is free and short enough that
// a space created this morning is findable this afternoon. --refresh is there
// for the rest.
const TTL = 24 * time.Hour

// Cache is one profile's remembered space list.
//
// Per profile, and that is the whole reason the profile name is in the path.
// Two profiles authorized as different accounts reach different spaces, and a
// single shared file would let one account's space list answer the other's
// lookup: a name that resolves to a space the active profile cannot even see,
// and then a 404 that reads as the tool being broken. Worse if the name happens
// to exist in both.
type Cache struct {
	path string
	now  func() time.Time
}

// cacheFile is what lands on disk.
type cacheFile struct {
	// Fetched is when the list was read from the API. Stored rather than taken
	// from the file's mtime, which is not preserved by every copy, backup, or
	// sync tool that might touch this directory.
	Fetched time.Time `json:"fetched"`

	// Profile is written so that a file which somehow reaches the wrong path is
	// refused rather than believed. Cheap, and the failure it prevents is
	// resolving a name against another account's spaces.
	Profile string `json:"profile"`

	Spaces []chat.Space `json:"spaces"`
}

// NewCache returns the cache for one profile.
//
// An unwritable or unlocatable cache directory is not an error. The resolver
// works without a cache, one API call at a time, and refusing to resolve
// because a directory could not be created would turn a read-only home into a
// tool that cannot find a space.
func NewCache(profile string) *Cache {
	dir, err := config.CacheDir()
	if err != nil {
		return nil
	}
	return &Cache{
		path: filepath.Join(dir, "spaces-"+profile+".json"),
		now:  time.Now,
	}
}

// Read returns the cached spaces when there are some and they are fresh.
//
// Every failure is a miss rather than an error, and deliberately: a corrupt,
// truncated, or half-written cache file should cost one extra API call, not a
// command that will not run until somebody deletes a file they did not know
// existed. The only cost of being wrong here is a request.
func (c *Cache) Read() ([]chat.Space, bool) {
	if c == nil {
		return nil, false
	}

	body, err := os.ReadFile(c.path)
	if err != nil {
		return nil, false
	}

	var file cacheFile
	if err := json.Unmarshal(body, &file); err != nil {
		return nil, false
	}
	if file.Profile != c.profileFromPath() {
		return nil, false
	}
	if c.now().Sub(file.Fetched) > TTL {
		return nil, false
	}
	if file.Fetched.After(c.now()) {
		// A clock that moved backwards, or a file copied from a machine ahead of
		// this one. Treated as a miss rather than as a very fresh cache, because
		// the alternative is a cache that never expires.
		return nil, false
	}
	return file.Spaces, true
}

// Write records a space list. A failure is returned and ignored by the caller:
// the answer is already in hand, and a full disk is not a reason to refuse it.
func (c *Cache) Write(spaces []chat.Space) error {
	if c == nil {
		return nil
	}

	body, err := json.Marshal(cacheFile{
		Fetched: c.now(),
		Profile: c.profileFromPath(),
		Spaces:  spaces,
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(c.path), config.DirMode); err != nil {
		return err
	}

	// Written through a temporary file and renamed, so that a command
	// interrupted halfway leaves either the old list or the new one. A
	// half-written JSON document would be a miss on every subsequent read,
	// which is a cache that silently stopped working.
	temp, err := os.CreateTemp(filepath.Dir(c.path), "spaces-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()

	if err := temp.Chmod(config.FileMode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), c.path)
}

// Forget removes the cached list, for `alias` and `logout` to call when what it
// holds may no longer be what the account can reach.
func (c *Cache) Forget() error {
	if c == nil {
		return nil
	}
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// profileFromPath recovers the profile name the path was built with, so that
// the file's own record of it can be checked without carrying a second copy.
func (c *Cache) profileFromPath() string {
	base := filepath.Base(c.path)
	base = base[:len(base)-len(filepath.Ext(base))]
	return base[len("spaces-"):]
}
