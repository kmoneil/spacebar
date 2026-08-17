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

//go:build !windows

package store

import (
	"os"
	"syscall"
)

// lockExclusive takes an exclusive advisory lock on the open file and returns
// the function that releases it.
//
// flock rather than fcntl, because fcntl's locks are owned by the process and
// are dropped when *any* descriptor for the file is closed, which makes them
// unsafe in a program that may open the same path twice. flock's are owned by
// the open file description, which is the granularity this needs.
//
// It blocks. A tail that waited a few milliseconds for another tail is correct;
// one that failed because a second process happened to be writing would turn a
// normal situation into an error somebody has to understand.
//
// Advisory, which is worth being plain about: it binds every process that takes
// the lock and nothing else. Anything appending to these files without going
// through this package can still interleave with it. That is the guarantee the
// platform offers, and no amount of care here changes it.
func lockExclusive(f *os.File) (func(), error) {
	fd := int(f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return nil, storeErr("cannot lock %s: %v", f.Name(), err)
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN) }, nil
}
