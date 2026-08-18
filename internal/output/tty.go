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

package output

import "os"

// IsTTY reports whether f is a terminal.
//
// SPEC.md §11.3 spells this out rather than taking a dependency for it. A
// terminal is a character device, and os.FileMode already says which files are
// one; the two lines below are the whole of what a dependency would add.
func IsTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// UseColor reports whether ANSI escapes may be written to f.
//
// Every condition has to hold, because each one is somebody telling us not to:
// a pipe is a program that will store what we write, NO_COLOR is a convention
// the user opted into globally, --no-color is them saying it for this run, and
// TERM=dumb is a terminal saying it cannot render escapes.
//
// NO_COLOR is honoured when it is present and non-empty, which is what
// no-color.org specifies. SPEC.md §11.3 says "unset"; the difference matters
// because NO_COLOR= in an environment is how a user un-sets it for one command
// without being able to unset it, and treating that as "no colour" would leave
// them no way back.
func UseColor(f *os.File, noColorFlag bool) bool {
	if noColorFlag {
		return false
	}
	if v, ok := os.LookupEnv("NO_COLOR"); ok && v != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return IsTTY(f)
}
