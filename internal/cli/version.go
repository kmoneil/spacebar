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
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/meta"
)

// versionInfo is the --json shape of `spacebar version`.
//
// The release gate parses this to check that a tagged build reports the tag it
// was built from, so the field names are a contract with CI as much as with a
// caller.
type versionInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

func newVersionCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version of this binary",
		Long: `Print the version of this binary.

A build from source reports 0.0.0-dev, because it is one. Only a release
build carries a version, and the release gate refuses a tag whose binary
disagrees with it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := versionInfo{
				Name:    meta.AppName,
				Version: meta.Version,
				Commit:  meta.Commit,
				Go:      meta.GoVersion(),
				OS:      runtime.GOOS,
				Arch:    runtime.GOARCH,
			}

			w := cmd.OutOrStdout()
			if opts.JSON {
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}

			_, err := fmt.Fprintf(w, "%s %s\ncommit  %s\ngo      %s\nos/arch %s/%s\n",
				info.Name, info.Version, info.Commit, info.Go, info.OS, info.Arch)
			return err
		},
	}
}
