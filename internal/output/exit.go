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

// Package output owns everything this tool writes: the exit code it leaves
// behind, whether stdout is a terminal, and how a result or a failure is
// rendered.
//
// It is the only package that decides those things. An escaping rule or a
// redaction rule that lives here holds everywhere; one that lives at a call
// site holds until the next call site forgets it.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ExitCode is the status this process leaves behind.
//
// The set is a contract with the scripts and agents that call this tool
// (SPEC.md §11.1). A caller has to be able to tell "your authorization expired"
// from "that space does not exist" without reading a message, because messages
// get rewritten and exit codes must not. Adding a code is a change to that
// contract; changing what one means is a breaking one.
type ExitCode int

const (
	// ExitOK is success.
	ExitOK ExitCode = 0

	// ExitError is a failure with nothing more specific to say about it.
	ExitError ExitCode = 1

	// ExitUsage is a bad flag, a bad argument, or a target that resolved to
	// more than one space. Nothing was sent.
	ExitUsage ExitCode = 2

	// ExitAPI is a network or API failure that outlived the retry policy.
	ExitAPI ExitCode = 3

	// ExitAuthRequired is a missing or expired authorization. It has its own
	// code because SPEC.md §6.7 makes it routine rather than exceptional: an
	// OAuth client in External + Testing expires its refresh tokens every
	// seven days, and a caller needs to react to that differently from a
	// genuine failure.
	ExitAuthRequired ExitCode = 4

	// ExitUnsupported is a capability the active profile's transport does not
	// have: reading over a write-only webhook, say. Refused before any
	// request is made (SPEC.md §8.2).
	ExitUnsupported ExitCode = 5

	// ExitRateLimited is a 429 that outlived the backoff. Distinct from
	// ExitAPI because the right response is to wait, not to investigate.
	ExitRateLimited ExitCode = 6

	// ExitRefused is a confirmation that was required and not given, including
	// the case where stdin is not a terminal so it could not be asked for
	// (SPEC.md §11.3).
	ExitRefused ExitCode = 7
)

// Error is a failure that knows how it should leave the process.
//
// Code is the machine-readable half and travels in --json output; Message is
// for a person. Both are part of the output contract, so neither is a good
// place to put a raw upstream string.
type Error struct {
	Code    string
	Message string
	Exit    ExitCode
	Err     error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Err }

// Errorf builds an Error with a formatted message.
func Errorf(code string, exit ExitCode, format string, a ...any) *Error {
	return &Error{Code: code, Exit: exit, Message: fmt.Sprintf(format, a...)}
}

// Usagef is the error for anything the caller got wrong before a request was
// made: an unknown flag, a missing argument, an ambiguous target.
func Usagef(format string, a ...any) *Error {
	return Errorf("USAGE", ExitUsage, format, a...)
}

// ExitCodeOf reports how err should leave the process. Anything that does not
// carry a code of its own is a generic failure.
func ExitCodeOf(err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Exit
	}
	return ExitError
}

// errorEnvelope is the shape a failure takes in --json mode (SPEC.md §11.2).
// It is a wrapper object rather than a bare error so that a caller reading a
// stream can tell a failure from a result by its key alone.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	ExitCode int    `json:"exit_code"`
}

// Report writes err to w and returns the code the process should exit with.
//
// w is always stderr. SPEC.md §11.2 puts every log, warning, and failure there
// even in --json mode, so that stdout carries results and nothing else: a
// caller that pipes stdout into jq must never be handed a half-written
// document followed by an error object.
func Report(w io.Writer, err error, asJSON bool) ExitCode {
	if err == nil {
		return ExitOK
	}
	exit := ExitCodeOf(err)

	if asJSON {
		code := "ERROR"
		if e, ok := errors.AsType[*Error](err); ok {
			code = e.Code
		}
		env := errorEnvelope{Error: errorBody{
			Code:     code,
			Message:  err.Error(),
			ExitCode: int(exit),
		}}
		enc := json.NewEncoder(w)
		if encErr := enc.Encode(env); encErr != nil {
			// The original failure is the story. If even reporting it fails,
			// there is nowhere better to report that to than the stream that
			// just refused the first attempt, so the result is discarded
			// rather than pretended about.
			_, _ = fmt.Fprintf(w, "error: %v\n", err)
		}
		return exit
	}

	// Discarded for the same reason: a caller cannot act on "we could not tell
	// you why we failed", and the exit code carries the verdict regardless.
	_, _ = fmt.Fprintf(w, "error: %v\n", err)
	return exit
}
