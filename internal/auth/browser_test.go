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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// browser.go had no tests at all until this file, which is worth saying out
// loud rather than quietly fixing: the reason the "Opened a browser" sentence
// could be false on every containerised machine this tool is built for is that
// nothing ever asked what OpenBrowser returns when the launcher fails.
//
// Nothing here launches a browser. The launcher is this test binary re-run as
// a subprocess, which is the os/exec package's own technique for the same
// problem, and it means these assertions say the same thing on a developer's
// desktop as on a runner with no display. A test that passed or failed
// depending on whether the machine happened to have xdg-open would be
// measuring the machine.

// helperEnv selects what the subprocess should do. Read in a test file, where
// internal/lint's environment and product-name gates do not reach, because
// this names no setting of this tool's and nothing outside the test sets it.
const helperEnv = "SPACEBAR_TEST_LAUNCHER"

// TestBrowserLauncherHelper is the subprocess. It is a test only so that the
// test binary can be re-run as one, and it does nothing at all in an ordinary
// run.
func TestBrowserLauncherHelper(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "":
		// The ordinary run. Not a skip, because there is nothing here that
		// wanted to run and could not.
		return
	case "fail":
		// What xdg-open does on a machine with no display and no browser: it
		// exits non-zero, quickly, having opened nothing.
		os.Exit(3)
	case "ok":
		os.Exit(0)
	case "linger":
		// What a launcher that handed the URL to a browser and stayed alive
		// for it looks like. Long enough that a test which waited for it
		// would be obvious rather than slow.
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}

// launcher builds a command that behaves the way mode names.
func launcher(t *testing.T, mode string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestBrowserLauncherHelper$")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	return cmd
}

// start runs the launcher and arranges for it to be cleaned up, so that a
// lingering subprocess cannot outlive the test that made it.
func start(t *testing.T, cmd *exec.Cmd) {
	t.Helper()

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the stand-in launcher: %v", err)
	}
	// Killed and not waited for. settled starts the only goroutine that may
	// call Wait on this command, and a second call from here is a write to the
	// same ProcessState from two goroutines: the race detector found it, and
	// it would have been an intermittent red in CI rather than here.
	t.Cleanup(func() { _ = cmd.Process.Kill() })
}

// TestALauncherThatFailedIsNotALaunch is the defect this file exists for.
//
// exec.Cmd.Start answers "was a process spawned". The caller is asking "did a
// browser open". On a container with xdg-open installed and no display those
// two answers disagree, and the flow used to report the spawn as the open,
// telling somebody a browser had opened when nothing had and nothing could.
func TestALauncherThatFailedIsNotALaunch(t *testing.T) {
	cmd := launcher(t, "fail")
	start(t, cmd)

	err := settled(cmd, launchGrace)
	if err == nil {
		t.Fatal("a launcher that exited 3 without opening anything was reported as a launch")
	}
	// The message has to say what happened rather than quote an exit status at
	// somebody, because this is the sentence that decides which of two things
	// the flow tells them next.
	if !strings.Contains(err.Error(), "without opening anything") {
		t.Errorf("the failure does not say what went wrong: %v", err)
	}
}

// TestALauncherThatExitedCleanlyIsALaunch.
//
// The common desktop case, and the one that has to keep working: xdg-open and
// open both hand the URL over and exit 0 straight away.
func TestALauncherThatExitedCleanlyIsALaunch(t *testing.T) {
	cmd := launcher(t, "ok")
	start(t, cmd)

	if err := settled(cmd, launchGrace); err != nil {
		t.Fatalf("a launcher that exited 0 was reported as a failure: %v", err)
	}
}

// TestALauncherStillRunningIsALaunchAndIsNotWaitedFor holds the half of the
// contract the grace window exists to protect.
//
// Some desktops keep the launcher alive for as long as the browser it opened,
// which is the reason OpenBrowser uses Start rather than Run and the reason
// that has not changed. Waiting for it would hang the flow until the browser
// was closed, which is long after the listener needed the callback.
//
// So the assertion is about the clock as much as about the answer: a launcher
// that is still running is a launch, and finding that out costs the window and
// not the process's lifetime.
func TestALauncherStillRunningIsALaunchAndIsNotWaitedFor(t *testing.T) {
	cmd := launcher(t, "linger")
	start(t, cmd)

	const grace = 50 * time.Millisecond

	began := time.Now()
	err := settled(cmd, grace)
	took := time.Since(began)

	if err != nil {
		t.Fatalf("a launcher that was still running was reported as a failure: %v", err)
	}
	// The subprocess sleeps for thirty seconds. Anything close to that means
	// the flow is waiting for the browser rather than for the launcher.
	if took > time.Second {
		t.Fatalf("settled waited %v for a launcher that was still running", took)
	}
	if took < grace {
		t.Fatalf("settled answered after %v, which is inside its own window: a launcher "+
			"that fails a moment later would be reported as a launch", took)
	}
}

// TestTheFlowSaysWhichOfTheTwoThingsHappened is the claim a person reads.
//
// Its twin, TestABrowserThatWillNotLaunchDoesNotFailTheFlow, covers the
// refusal branch through a whole login. This covers both branches at the one
// place the sentence is chosen, because the branch that was wrong is the
// success one and nothing asserted it.
func TestTheFlowSaysWhichOfTheTwoThingsHappened(t *testing.T) {
	const consent = "https://consent.example/o/oauth2/v2/auth?client_id=x"

	// What a real failed launch produces on this machine, obtained by running
	// one rather than by writing out what it is assumed to look like.
	cmd := launcher(t, "fail")
	start(t, cmd)
	settledFailure := settled(cmd, launchGrace)
	if settledFailure == nil {
		t.Fatal("the stand-in launcher was reported as a launch, so this test has no failure to use")
	}

	for _, tc := range []struct {
		name       string
		launch     error
		want, deny string
	}{
		{
			name: "a browser that opened",
			want: "Opened a browser",
			deny: "Could not open a browser",
		},
		{
			name:   "a launcher that is not on this machine",
			launch: exec.ErrNotFound,
			want:   "Could not open a browser",
			deny:   "Opened a browser",
		},
		{
			name: "a launcher that spawned and then failed, which is the container case",
			// The error settled itself produces, built by running the
			// stand-in launcher rather than written out, so this case is
			// distinct from the one above rather than a second spelling of
			// it. Before this change OpenBrowser returned nil here and the
			// flow took the branch below's opposite.
			launch: settledFailure,
			want:   "Could not open a browser",
			deny:   "Opened a browser",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reported := &collectingReporter{}
			f := &Flow{
				Report:  reported,
				Browser: func(string) error { return tc.launch },
			}

			f.open(consent)

			said := reported.text()
			if !strings.Contains(said, tc.want) {
				t.Errorf("the flow did not say %q:\n%s", tc.want, said)
			}
			if strings.Contains(said, tc.deny) {
				t.Errorf("the flow said %q, which did not happen:\n%s", tc.deny, said)
			}
			// Either way the URL is on the screen. It is the only thing that
			// makes the flow recoverable when the browser is somewhere else,
			// and it is not conditional on which branch was taken.
			if !strings.Contains(said, consent) {
				t.Errorf("the consent URL was not printed:\n%s", said)
			}
		})
	}
}

// TestOnlyAnHTTPURLIsHandedToTheDesktop.
//
// The URL is built by this package and never arrives from outside, so this
// guards a future caller rather than today's. It is worth a test for the
// reason the check is worth four lines: the argument goes to a program whose
// entire job is to decide what to do with a URL by its scheme, and the schemes
// a desktop will act on include ones that run things.
func TestOnlyAnHTTPURLIsHandedToTheDesktop(t *testing.T) {
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://consent.example/auth?client_id=x", true},
		{"http://127.0.0.1:8080/", true},

		// A scheme a desktop may act on, which is the whole point.
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"data:text/html,<script>", false},
		{"vscode://file.invalid/etc/passwd", false},
		{"smb://host.invalid/share", false},

		// Read as a flag by whatever is on the other end.
		{"-h", false},
		{"--version", false},
		{"-https://consent.example/", false},

		{"", false},
		{"consent.example", false},
		{" https://consent.example/", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			err := checkBrowserURL(tc.url)
			if tc.ok && err != nil {
				t.Errorf("checkBrowserURL(%q) = %v, want it accepted", tc.url, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("checkBrowserURL(%q) was accepted", tc.url)
			}
		})
	}
}

// TestEachPlatformGetsItsOwnLauncher pins the mapping, which is otherwise a
// switch nobody reads until it is wrong on somebody else's machine.
func TestEachPlatformGetsItsOwnLauncher(t *testing.T) {
	for _, tc := range []struct {
		goos, name string
		args       []string
	}{
		{"darwin", "open", nil},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler"}},
		{"linux", "xdg-open", nil},

		// Everything else falls through to the freedesktop launcher, which is
		// right for the BSDs and is a better guess than failing.
		{"freebsd", "xdg-open", nil},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			name, args := browserCommand(tc.goos)
			if name != tc.name {
				t.Errorf("browserCommand(%q) = %q, want %q", tc.goos, name, tc.name)
			}
			if strings.Join(args, " ") != strings.Join(tc.args, " ") {
				t.Errorf("browserCommand(%q) args = %v, want %v", tc.goos, args, tc.args)
			}
		})
	}
}

// TestOpenBrowserReportsWhatTheLauncherDid holds the wire between the halves.
//
// Every other test in this file drives settled or Flow.open on its own, and
// that is not enough: replacing the settle step in openWith with a bare nil,
// which is exactly the line that shipped, passed all of them. A defect can be
// reintroduced through a seam that nothing crosses.
//
// The launcher is this test binary, so the assertion is the same on a machine
// with a browser and one without.
func TestOpenBrowserReportsWhatTheLauncherDid(t *testing.T) {
	self := os.Args[0]
	args := []string{"-test.run=^TestBrowserLauncherHelper$"}

	for _, tc := range []struct {
		name, mode string
		wantErr    bool
	}{
		{
			name:    "spawned and then failed, which is the container this was found on",
			mode:    "fail",
			wantErr: true,
		},
		{
			name: "spawned and exited cleanly, which is an ordinary desktop",
			mode: "ok",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The subprocess inherits this, which is how the stand-in learns
			// what to be. Restored when the test ends, so the helper is inert
			// again for every other test in the binary.
			t.Setenv(helperEnv, tc.mode)

			err := openWith("https://consent.example/o/oauth2/v2/auth?client_id=x", self, args)
			if tc.wantErr && err == nil {
				t.Fatal("a launcher that opened nothing was reported as having opened a browser")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("a launcher that worked was reported as a failure: %v", err)
			}
		})
	}
}

// TestAURLTheDesktopMayNotHaveNeverReachesAProcess.
//
// The scheme check runs before anything is spawned, so a refusal costs no
// subprocess. Asserted by pointing the launcher at a command that would fail
// loudly if it ever ran.
func TestAURLTheDesktopMayNotHaveNeverReachesAProcess(t *testing.T) {
	err := openWith("file:///etc/passwd", "this-command-does-not-exist.invalid", nil)
	if err == nil {
		t.Fatal("a file:// URL was handed to the desktop")
	}
	if !strings.Contains(err.Error(), "not http or https") {
		t.Errorf("the refusal came from spawning rather than from the check: %v", err)
	}
}
