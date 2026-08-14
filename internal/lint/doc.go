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

// Package lint holds the repository to its own rules.
//
// It ships no code. Everything here is a test, and every test asserts
// something a comment somewhere else claims: that go.mod and the workflows
// name the same toolchain, that a tool version is declared once, that NOTICE
// lists what is actually linked. Those claims rot silently: nothing breaks
// when they stop being true, which is exactly why they need a gate rather than
// a convention.
//
// The rule for adding one: if a code comment says "asserted by internal/lint",
// the assertion belongs here and has to exist. A comment that describes a gate
// nobody wrote is worse than no comment, because it stops the next person
// looking.
package lint
