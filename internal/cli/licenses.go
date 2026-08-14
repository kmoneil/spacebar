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

package cli

import (
	"io"

	"github.com/spf13/cobra"

	spacebar "github.com/kmoneil/spacebar"
	"github.com/kmoneil/spacebar/internal/meta"
)

func newLicensesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "licenses",
		Short: "Print the licence of every dependency in this binary",
		Long: `Print the licence of every dependency linked into this binary.

Apache-2.0 §4 requires those notices to travel with the work, and a
single binary has nowhere to put a file. This command is where they
went: it prints what ` + meta.AppName + ` was built with, from the binary
itself, with no network and no checkout involved.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := io.WriteString(cmd.OutOrStdout(), spacebar.ThirdPartyLicenses)
			return err
		},
	}
}
