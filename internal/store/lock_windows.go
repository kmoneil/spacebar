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

//go:build windows

package store

import (
	"os"
	"syscall"
	"unsafe"
)

// lockfileExclusiveLock is LOCKFILE_EXCLUSIVE_LOCK.
const lockfileExclusiveLock = 0x00000002

// LockFileEx and UnlockFileEx are resolved from kernel32 by hand.
//
// Go's own syscall package does not export them on Windows, and the usual
// answer, golang.org/x/sys/windows, would be a sixth direct dependency for two
// procedure addresses. SPEC.md §3.1 budgets five and CONTRIBUTING says the
// sixth needs an argument in the pull request; "we needed a file lock" is not
// one when the standard library can already reach the same call.
//
// x/sys does exactly this internally, so nothing here is a trick: a lazy DLL
// handle is the documented way to reach a Win32 entry point the standard
// library has not wrapped.
var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	procLockFile = kernel32.NewProc("LockFileEx")
	procUnlock   = kernel32.NewProc("UnlockFileEx")
)

// lockExclusive takes an exclusive lock on the open file and returns the
// function that releases it.
//
// The unix side uses flock and blocks; LockFileEx without
// LOCKFILE_FAIL_IMMEDIATELY blocks too, so the two agree on what matters. The
// range is the whole file, expressed as the largest one LockFileEx takes, which
// is how a whole-file lock is spelled on this platform.
//
// Unlike flock this is mandatory rather than advisory, so a second writer is
// blocked whether or not it cooperates. That is stronger than the unix side
// gives and nothing relies on it: the rule this package states is the weaker
// one both platforms can keep.
func lockExclusive(f *os.File) (func(), error) {
	h := syscall.Handle(f.Fd())
	overlapped := new(syscall.Overlapped)

	r, _, err := procLockFile.Call(uintptr(h), lockfileExclusiveLock, 0,
		uintptr(^uint32(0)), uintptr(^uint32(0)), uintptr(unsafe.Pointer(overlapped)))
	if r == 0 {
		return nil, storeErr("cannot lock %s: %v", f.Name(), err)
	}

	return func() {
		released := new(syscall.Overlapped)
		_, _, _ = procUnlock.Call(uintptr(h), 0,
			uintptr(^uint32(0)), uintptr(^uint32(0)), uintptr(unsafe.Pointer(released)))
	}, nil
}
