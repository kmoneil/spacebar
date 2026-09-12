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

package lint

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/fuzz-verdict.sh decides what a red nightly means, and nothing held
// it.
//
// It is the one gate here whose whole job is to stop a habit forming. A sweep
// that cries wolf teaches people to press re-run, and a habit of pressing
// re-run is how a real crasher gets waved through, which is the reason
// .github/workflows/fuzz-nightly.yml gives three times for being shaped the way
// it is. So the script has to be right in both directions: a find it calls an
// engine fault is a bug nobody hears about, and an engine fault it calls a find
// is the habit.
//
// On 2026-09-12 it called one wrong. internal/store spent its whole ten
// minutes at about 35,000 executions a second, turned up no new coverage, and
// failed on the fuzzing coordinator's own deadline at 601.02s; the script had
// no pattern for that, fell through to its default, and the sweep reported a
// crashing input that does not exist with no artifact to show for it.
//
// Run as a subprocess rather than reimplemented here, for the reason
// hook_test.go runs the commit-msg hook that way: a second copy of the rules in
// Go is a second thing to disagree with the shell, and what has to be true is
// that the file under scripts/ classifies these, because that file is what the
// nightly runs.

// deadlineRun is the tail of the 2026-09-12 nightly.
//
// Written out rather than reduced to the one line that matches, so that it
// reads as what happened: a budget spent in full, nothing new found, and a
// failure that says nothing about the code under test.
const deadlineRun = `fuzz: elapsed: 10m0s, execs: 21549523 (31142/sec), new interesting: 0 (total: 601)
fuzz: elapsed: 10m1s, execs: 21549523 (0/sec), new interesting: 0 (total: 601)
--- FAIL: FuzzARecordOnlyAnswersForItsOwnSpace (601.02s)
    context deadline exceeded
FAIL
exit status 1
FAIL	github.com/kmoneil/spacebar/internal/store	601.028s
make: *** [Makefile:247: fuzz] Error 1
`

// crashRun is what a find looks like: Go names the input it wrote.
const crashRun = `fuzz: elapsed: 12s, execs: 405112 (33759/sec), new interesting: 3 (total: 44)
--- FAIL: FuzzSanitize (12.10s)
    --- FAIL: FuzzSanitize (0.00s)
        sanitize_test.go:141: a C1 control reached the column: "\u0085"

    Failing input written to testdata/fuzz/FuzzSanitize/8f2e1a0b5c3d4e6f
    To re-run:
    go test -run=FuzzSanitize/8f2e1a0b5c3d4e6f
FAIL
`

// quotedDeadline is a property failure whose own message happens to contain the
// words the coordinator's does.
//
// It is why the deadline is matched as a whole line and not as a substring. The
// coordinator's error is printed alone, under the target, by the same writer
// that indents a t.Fatalf; a message that merely ends in those words is a find
// like any other, and a grep for the words anywhere in the log would have
// waved it through.
const quotedDeadline = `fuzz: elapsed: 4s, execs: 138422 (34605/sec), new interesting: 0 (total: 9)
--- FAIL: FuzzAPathStaysOnTheBase (4.02s)
    --- FAIL: FuzzAPathStaysOnTheBase (0.00s)
        chat_test.go:88: building the request: context deadline exceeded

FAIL
`

// engineLog is a run the fuzzing harness ended, with the coordinator's message
// exactly where a property failure's message would be. That is the whole
// difficulty: the two are told apart by what they say and by whether an input
// was written, never by where they appear.
func engineLog(msg string) string {
	return "fuzz: elapsed: 3s, execs: 104233 (34744/sec), new interesting: 0 (total: 12)\n" +
		"--- FAIL: FuzzTranslate (3.10s)\n    " + msg + "\nFAIL\nexit status 1\n"
}

// verdict runs the script over a log in a throwaway repository and reports
// whether it called the run a find.
//
// The repository is a real one because the script's first and strongest check
// asks git whether the package's testdata/fuzz holds anything untracked, which
// is how a find is recognised without depending on Go's wording.
func verdict(t *testing.T, log string, crasher bool) (bool, string) {
	t.Helper()

	const pkg = "internal/store"

	dir := t.TempDir()
	git := exec.Command("git", "init", "--quiet")
	git.Dir = dir
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	if crasher {
		corpus := filepath.Join(dir, pkg, "testdata", "fuzz", "FuzzARecordOnlyAnswersForItsOwnSpace")
		if err := os.MkdirAll(corpus, 0o700); err != nil {
			t.Fatalf("making the corpus directory: %v", err)
		}
		input := "go test fuzz v1\n[]byte(\"\\xff\")\n"
		if err := os.WriteFile(filepath.Join(corpus, "8f2e1a0b5c3d4e6f"), []byte(input), 0o600); err != nil {
			t.Fatalf("writing the crasher: %v", err)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "fuzz.log"), []byte(log), 0o600); err != nil {
		t.Fatalf("writing the log: %v", err)
	}

	script := filepath.Join(repoRoot(t), "scripts", "fuzz-verdict.sh")
	cmd := exec.Command("sh", script, "fuzz.log", pkg)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return false, string(out)
	}
	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		t.Fatalf("running the verdict script: %v\n%s", err, out)
	}
	if code := exit.ExitCode(); code != 1 {
		t.Fatalf("the script exited %d, which is neither a find nor an engine fault:\n%s", code, out)
	}
	return true, string(out)
}

// called names a verdict the way the script's own log line does.
func called(find bool) string {
	if find {
		return "FIND"
	}
	return "ENGINE"
}

// TestTheVerdictScriptTellsAFindFromTheFuzzingEngine.
//
// Every engine case here is a message the harness produced with the code under
// test having done nothing wrong, and every find is an input that exists. The
// two cases that matter most are the pair that share a log: a deadline with
// nothing on disk is the engine, and the same deadline with an input beside it
// is a find, because a deadline that lands while a crash is being minimized
// still writes that crash to testdata/fuzz and returns the deadline as its
// error. That is the ordering inside the script, and it is the reason a check
// for the deadline can be added at all.
func TestTheVerdictScriptTellsAFindFromTheFuzzingEngine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		log     string
		crasher bool
		find    bool
	}{
		{
			name: "the deadline the coordinator reported on 2026-09-12",
			log:  deadlineRun,
			find: false,
		},
		{
			name:    "the same deadline with the input it was minimizing",
			log:     deadlineRun,
			crasher: true,
			find:    true,
		},
		{
			name:    "an input Go wrote and named",
			log:     crashRun,
			crasher: true,
			find:    true,
		},
		{
			name: "an input Go named that nothing on disk backs",
			log:  crashRun,
			find: true,
		},
		{
			name: "a failure whose own message quotes the deadline",
			log:  quotedDeadline,
			find: true,
		},
		{
			name: "a worker that started and never fuzzed anything",
			log:  engineLog("fuzzing process terminated without fuzzing a value: EOF"),
			find: false,
		},
		{
			name: "a worker that died",
			log:  engineLog("fuzzing process hung or terminated unexpectedly: exit status 2"),
			find: false,
		},
		{
			name: "a worker that never started",
			log:  engineLog("failed to start fuzzing process: fork/exec: resource temporarily unavailable"),
			find: false,
		},
		{
			name: "a worker that would not be waited for",
			log:  engineLog("waiting for fuzzing process: signal: killed"),
			find: false,
		},
		{
			name: "a failure nothing here recognises, which is a find until somebody says otherwise",
			log:  engineLog("something nobody has seen before"),
			find: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			find, out := verdict(t, tc.log, tc.crasher)
			if find != tc.find {
				t.Fatalf("the script said %s and this is %s:\n%s",
					called(find), called(tc.find), out)
			}
			// The exit code is what the workflow branches on and the log line
			// is what a person reads, and they have to agree: the whole design
			// is that the verdict is stated rather than inferred.
			if want := "verdict: " + called(tc.find); !strings.Contains(out, want) {
				t.Fatalf("the script exited like a %s without saying %q:\n%s",
					called(tc.find), want, out)
			}
		})
	}
}
