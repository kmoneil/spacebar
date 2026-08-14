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

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
)

// TestExitCodesAreStable is the whole reason the codes are named constants.
//
// The numbers are a contract with every script and agent that calls this tool
// (SPEC.md §11.1). Renaming a constant is refactoring; changing what number it
// holds is a breaking change to something we cannot see, so the numbers are
// written out here rather than derived.
func TestExitCodesAreStable(t *testing.T) {
	for _, tc := range []struct {
		code ExitCode
		want int
		name string
	}{
		{ExitOK, 0, "ExitOK"},
		{ExitError, 1, "ExitError"},
		{ExitUsage, 2, "ExitUsage"},
		{ExitAPI, 3, "ExitAPI"},
		{ExitAuthRequired, 4, "ExitAuthRequired"},
		{ExitUnsupported, 5, "ExitUnsupported"},
		{ExitRateLimited, 6, "ExitRateLimited"},
		{ExitRefused, 7, "ExitRefused"},
	} {
		if int(tc.code) != tc.want {
			t.Errorf("%s = %d, want %d; these numbers are a public contract", tc.name, tc.code, tc.want)
		}
	}
}

func TestExitCodeOf(t *testing.T) {
	if got := ExitCodeOf(nil); got != ExitOK {
		t.Errorf("ExitCodeOf(nil) = %d, want %d", got, ExitOK)
	}

	// An error with no code of its own is generic, not a success. Getting this
	// backwards would report every unclassified failure as a clean run.
	if got := ExitCodeOf(errors.New("something")); got != ExitError {
		t.Errorf("ExitCodeOf(plain error) = %d, want %d", got, ExitError)
	}

	if got := ExitCodeOf(Usagef("bad flag")); got != ExitUsage {
		t.Errorf("ExitCodeOf(Usagef) = %d, want %d", got, ExitUsage)
	}

	// Found through a wrapper, which is the case that matters: a code set deep
	// in a call stack has to survive being annotated on the way out, or every
	// caller has to remember not to annotate.
	wrapped := fmt.Errorf("while sending: %w", Errorf("PERMISSION_DENIED", ExitAPI, "denied"))
	if got := ExitCodeOf(wrapped); got != ExitAPI {
		t.Errorf("ExitCodeOf(wrapped) = %d, want %d", got, ExitAPI)
	}
}

func TestReportJSONEnvelope(t *testing.T) {
	var buf bytes.Buffer
	exit := Report(&buf, Errorf("PERMISSION_DENIED", ExitAPI, "chat apps are disabled for this OU"), true)

	if exit != ExitAPI {
		t.Errorf("exit = %d, want %d", exit, ExitAPI)
	}

	var env struct {
		Error struct {
			Code     string `json:"code"`
			Message  string `json:"message"`
			ExitCode int    `json:"exit_code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("the error envelope is not valid JSON: %v\ngot: %s", err, buf.String())
	}
	if env.Error.Code != "PERMISSION_DENIED" {
		t.Errorf("code = %q, want PERMISSION_DENIED", env.Error.Code)
	}
	if env.Error.ExitCode != int(ExitAPI) {
		t.Errorf("exit_code = %d, want %d", env.Error.ExitCode, ExitAPI)
	}
	if env.Error.Message != "chat apps are disabled for this OU" {
		t.Errorf("message = %q", env.Error.Message)
	}
}

// TestReportOfAPlainErrorStillHasACode holds the shape of the envelope for the
// case nobody plans for. A consumer switching on .error.code must never have to
// handle the key being absent.
func TestReportOfAPlainErrorStillHasACode(t *testing.T) {
	var buf bytes.Buffer
	Report(&buf, errors.New("boom"), true)

	var env map[string]map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if env["error"]["code"] != "ERROR" {
		t.Errorf("code = %v, want ERROR", env["error"]["code"])
	}
}

func TestReportOfNilWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	if exit := Report(&buf, nil, true); exit != ExitOK {
		t.Errorf("exit = %d, want %d", exit, ExitOK)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %q on success; nothing should be written", buf.String())
	}
}

// TestUseColorHonoursEveryVeto checks each condition on its own, because they
// are ORed vetoes and a bug in one is invisible while the others hold.
func TestUseColorHonoursEveryVeto(t *testing.T) {
	// Not a terminal, so the answer is no whatever else is set. A pipe is a
	// program that will store what we write.
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating a pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = pipeR.Close()
		_ = pipeW.Close()
	})

	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")

	if UseColor(pipeW, false) {
		t.Error("colour is on for a pipe; stdout that is not a terminal is data")
	}
	if UseColor(pipeW, true) {
		t.Error("--no-color did not win")
	}

	// NO_COLOR set but empty is not a veto: that is how a user clears it for
	// one command in a shell that cannot unset a variable inline. Only a
	// non-empty value means no.
	t.Setenv("NO_COLOR", "1")
	if UseColor(pipeW, false) {
		t.Error("NO_COLOR=1 did not win")
	}

	if IsTTY(pipeW) {
		t.Error("IsTTY says a pipe is a terminal")
	}
}
