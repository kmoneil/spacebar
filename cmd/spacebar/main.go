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

// Command spacebar is a terminal client and MCP server for Google Chat.
//
// This file is deliberately the whole of package main. os.Exit does not run
// deferred functions, so the only thing that may live beside it is the call
// that returns the code. Everything with a defer to run stays inside
// internal/cli, where it is also testable without a subprocess.
package main

import (
	"os"

	"github.com/kmoneil/spacebar/internal/cli"
)

func main() {
	os.Exit(int(cli.Execute(os.Args[1:])))
}
