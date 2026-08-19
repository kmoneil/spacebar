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
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// launchGrace is how long a launcher is given to fail before it is believed.
//
// A measurement rather than a round number chosen for looking careful. On a
// machine with no display and no browser, xdg-open exits 3 without opening
// anything, and over forty runs the slowest took 61.8ms. This is four times
// that, which is the margin for a machine slower than the one it was measured
// on, and it is still short enough that nobody waiting for a consent URL
// notices it.
//
// Wrong in one direction costs nothing and wrong in the other costs a false
// sentence, which is what decides the size. A launcher slower to fail than
// this is reported as having worked, which is exactly the behaviour that
// shipped before this constant existed. A launcher that works but is slower
// than this to exit is also reported as having worked, correctly, because a
// launcher still running is a launcher that did not fail.
const launchGrace = 250 * time.Millisecond

// OpenBrowser asks the desktop to open a URL.
//
// Fifteen lines of os/exec rather than a dependency, per SPEC.md §3.2 and §6.5.
// github.com/pkg/browser is a fine library and this is all of it that matters,
// and a dependency in a tree this small has to earn more than that.
//
// Start rather than Run, deliberately, and that has not changed. The helper is
// a launcher: it hands the URL to whatever is registered and returns, and on
// some desktops it stays alive for as long as the browser does. Waiting for it
// would mean the flow hung until the browser was closed, which is after the
// user finished, which is after the listener needed to have received the
// callback.
//
// What has changed is that Start alone was taken as success, and it answers a
// different question from the one the caller is asking. Start reports whether
// a process was spawned. The caller wants to know whether a browser opened,
// and on a container with xdg-open installed and no display those two answers
// disagree: the process spawns, exits 3 a hundredth of a second later having
// opened nothing, and the flow tells somebody "Opened a browser to authorize".
// That sentence was false on every machine this tool is built for.
//
// So the launcher is given launchGrace to fail. Exited non-zero inside it is a
// failure; exited zero, or still running when the window closes, is a launch.
// Nothing waits for the browser, and the flow is never held up by more than
// the window.
//
// The goroutine also reaps the child, which the previous version deliberately
// did not, on the grounds that nothing was waiting for it. Something is now,
// briefly, so the zombie that comment accepted is gone as a side effect rather
// than as a fix.
func OpenBrowser(url string) error {
	name, args := browserCommand(runtime.GOOS)
	return openWith(url, name, args)
}

// openWith is OpenBrowser with the launcher named, which is the whole of it
// apart from choosing one.
//
// Split out for the test rather than for the structure, and that is worth
// saying because the split looks like ceremony. The first version of this
// change was tested by driving settled and Flow.open separately, and reverting
// OpenBrowser to the line that shipped passed every one of those tests: each
// half was held and the wire between them was not. There is no seam a test can
// reach in a function that decides its own subprocess from runtime.GOOS.
func openWith(url, name string, args []string) error {
	if err := checkBrowserURL(url); err != nil {
		return err
	}

	cmd := exec.Command(name, append(args, url)...)
	if err := cmd.Start(); err != nil {
		// The launcher is not on this machine at all, which is the case
		// SPEC.md §6.5 was written for and the one that already worked.
		return err
	}
	return settled(cmd, launchGrace)
}

// settled reports whether a launcher that started has since failed.
//
// The channel is buffered so that the goroutine can finish and be collected
// when the window closes first, rather than blocking forever on a send nobody
// is left to receive.
func settled(cmd *exec.Cmd, grace time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("the browser launcher exited without opening anything: %w", err)
		}
		return nil
	case <-timer.C:
		// Still running, which is what a launcher that handed the URL to a
		// browser and stayed alive for it looks like.
		return nil
	}
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
