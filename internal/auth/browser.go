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

package auth

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// OpenBrowser asks the desktop to open a URL.
//
// Fifteen lines of os/exec rather than a dependency, per SPEC.md §3.2 and §6.5.
// github.com/pkg/browser is a fine library and this is all of it that matters,
// and a dependency in a tree this small has to earn more than that.
//
// Start rather than Run, deliberately. The helper is a launcher: it hands the
// URL to whatever is registered and returns, and on some desktops it stays
// alive for as long as the browser does. Waiting for it would mean the flow
// hung until the browser was closed, which is after the user finished, which is
// after the listener needed to have received the callback.
//
// The process is left unreaped for the same reason, which costs one zombie
// entry for the seconds this command runs and avoids a goroutine whose only job
// is to wait for something nobody is waiting for.
func OpenBrowser(url string) error {
	if err := checkBrowserURL(url); err != nil {
		return err
	}

	name, args := browserCommand(runtime.GOOS)
	cmd := exec.Command(name, append(args, url)...)
	return cmd.Start()
}

func browserCommand(goos string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler"}
	}
	return "xdg-open", nil
}

// checkBrowserURL refuses anything that is not an http URL.
//
// The URL is built by this package and never arrives from outside, so this
// guards against a future caller rather than against today's. What it guards
// against is worth the four lines: the argument goes to a program whose whole
// job is to decide what to do with a URL by its scheme, and the set of schemes
// a desktop will act on includes ones that run things. A leading dash is
// refused for the older reason, that it would be read as a flag by whatever is
// on the other end.
func checkBrowserURL(url string) error {
	switch {
	case strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "https://"):
		return nil
	case strings.HasPrefix(url, "-"):
		return errors.New("refusing to open a URL that starts with a dash")
	}
	return errors.New("refusing to open a URL that is not http or https")
}
