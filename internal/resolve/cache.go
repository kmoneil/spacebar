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
// tool that cannot find a space. A name that cannot be a profile is the same
// answer for the same reason: no cache, and a command that still works.
//
// The name is checked here rather than trusted, and the difference matters
// because this is where a string becomes a path. Every caller today reaches
// this through config.Active, which returns a key out of a file that
// CheckProfileName has already been run over, so the check is a second layer.
// It is written because the layer above is three facts in three packages, and
// what is on the other side of it is not a bad read: Write renames onto this
// path and Forget removes it. A first layer that needs the layer below it to be
// safe is not a first layer.
//
// One check at construction covers all three methods, because the path is
// decided here and never rebuilt.
func NewCache(profile string) *Cache {
	if err := config.CheckProfileName(profile); err != nil {
		return nil
	}

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
//
// "When there are some" is a condition and not a turn of phrase, and it was
// only the phrase until 2026-08-20. A file that parses, names this profile and
// is fresh but lists nothing answered "known: nothing", and the caller cannot
// tell that from "known: these six". What it costs is not one request.
// `search` compares the index against this list to say which spaces it did not
// look in, so an empty list is a comparison that finds nothing missing, and a
// search of one space out of six then reports itself as whole. Over MCP it is
// `coverage_known: true` beside an empty `unsearched`, which the tool
// description tells a model to read as "nothing was missed", to the one reader
// that cannot check.
//
// A profile that genuinely reaches no spaces and a list that never got filled
// are the same bytes, so the safe reading is the second. The cost of being
// wrong that way is one listing on a command that was going to fail anyway,
// because a resolve against no spaces matches nothing.
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
	if len(file.Spaces) == 0 {
		return nil, false
	}
	return file.Spaces, true
}

// Write records a space list. A failure is returned and ignored by the caller:
// the answer is already in hand, and a full disk is not a reason to refuse it.
//
// A list with nothing in it is not recorded, and that is the producer side of
// the rule Read states. It matters for a reason beyond symmetry: the only
// caller writes whatever the listing yielded, so without this one odd answer
// from the API replaces a good cached list with an empty one, and Read then has
// to distrust it for a full TTL. Declining leaves the previous list in place,
// which can only overstate what a search did not look in, and overstating that
// is the safe direction.
func (c *Cache) Write(spaces []chat.Space) error {
	if c == nil || len(spaces) == 0 {
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

// Forget removes the cached list, for the commands that end an authorization or
// a profile, because what it holds is no longer what that name can reach.
//
// Two callers, and the second is the one that is a wrong answer rather than a
// leftover. `auth logout` leaves the display name of every space the account
// could reach sitting on disk, which the person who typed logout has no reason
// to expect. `profile rm` leaves the same file under a name that is reusable:
// remove a profile, configure a new one with the same name for a different
// account, and for the rest of the day a display name resolves against the old
// account's spaces. The Profile field in the file cannot catch that, because it
// is the same name.
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
