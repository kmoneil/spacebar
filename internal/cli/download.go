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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
	"github.com/kmoneil/spacebar/internal/transport"
)

// downloadRow is the --json shape of one file written.
type downloadRow struct {
	// Path is where it landed, which is the thing a caller acts on next.
	Path string `json:"path"`

	Name        string `json:"name"`
	ContentName string `json:"content_name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Bytes       int    `json:"bytes"`
}

func newMessagesDownloadCmd(opts *Options) *cobra.Command {
	var (
		out   string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "download MESSAGE",
		Short: "Write a message's attachments to a directory",
		Long: `Write a message's attachments to a directory.

  ` + meta.AppName + ` messages download spaces/AAAAAAA/messages/BBBBBBB
  ` + meta.AppName + ` messages download spaces/AAAAAAA/messages/BBBBBBB --out ~/Downloads

Every file lands inside --out, which defaults to the working directory, and
nowhere else. The name comes from the message, which means it comes from
whoever posted it: it is reduced to a base name before it is joined, so an
attachment called "../../.ssh/authorized_keys" is written as a file with a
strange name in the directory you asked for.

An existing file is not overwritten unless --force says so, because the name is
not yours and a download should not be able to replace something you have.

A Drive attachment is not downloaded. Chat returns a reference to it rather
than the bytes, and fetching it is Drive's API rather than this one. Those are
listed and skipped rather than silently missing.`,

		Args: exactlyOne("messages download needs a message.\n" +
			"  %s messages download spaces/AAAAAAA/messages/BBBBBBB"),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "messages download", transport.CanRead); err != nil {
				return err
			}

			message, err := opened.Transport.GetMessage(cmd.Context(), args[0])
			if err != nil {
				return finish(r, opened, err)
			}
			if len(message.Attachment) == 0 {
				r.Note("that message has no attachments.")
				return nil
			}

			dir, err := downloadDir(out)
			if err != nil {
				return err
			}

			for _, file := range message.Attachment {
				if err := downloadOne(cmd, r, opened, dir, file, force); err != nil {
					return finish(r, opened, err)
				}
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&out, "out", ".", "the directory to write into")
	f.BoolVar(&force, "force", false, "overwrite a file that is already there")
	return cmd
}

// downloadDir resolves and creates the directory files will land in.
func downloadDir(out string) (string, error) {
	if out == "" {
		out = "."
	}

	dir, err := filepath.Abs(out)
	if err != nil {
		return "", output.Usagef("cannot use %q as a directory: %v", out, err)
	}
	if err := os.MkdirAll(dir, config.DirMode); err != nil {
		return "", output.Usagef("cannot create %q: %v", dir, err)
	}
	return dir, nil
}

// downloadOne fetches one attachment and writes it inside dir.
func downloadOne(cmd *cobra.Command, r *output.Renderer, opened *profile.Open, dir string,
	file chat.Attachment, force bool,
) error {
	if file.AttachmentDataRef == nil || file.AttachmentDataRef.ResourceName == "" {
		// A Drive file. Said out loud rather than skipped quietly, because a
		// caller counting files against attachments would otherwise be missing
		// one with nothing to explain it.
		r.Warn("%s is a Drive file rather than an upload, so its bytes are not here to fetch.",
			nameOf(file))
		return nil
	}

	body, err := opened.Transport.Download(cmd.Context(), file.AttachmentDataRef.ResourceName)
	if err != nil {
		return err
	}

	path := filepath.Join(dir, SafeFilename(file.ContentName))
	if !force {
		if _, err := os.Stat(path); err == nil {
			return output.Errorf("EXISTS", output.ExitUsage,
				"%s is already there, and the name came from the message rather than from you.\n"+
					"Pass --force to overwrite it, or --out to write somewhere else.", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return output.Usagef("cannot write to %s: %v", path, err)
		}
	}

	if err := os.WriteFile(path, body, config.FileMode); err != nil {
		return output.Usagef("cannot write %s: %v", path, err)
	}

	return r.Item(downloadRow{
		Path:        path,
		Name:        file.Name,
		ContentName: file.ContentName,
		ContentType: file.ContentType,
		Bytes:       len(body),
	}, path, fmt.Sprintf("%d", len(body)), file.ContentType)
}

// nameOf is what to call an attachment in a message about it.
func nameOf(file chat.Attachment) string {
	if file.ContentName != "" {
		return file.ContentName
	}
	return file.Name
}

// SafeFilename reduces a name that came from a message to one that cannot leave
// the directory it is joined onto.
//
// The name is chosen by whoever posted the message, which is the whole reason
// this exists: "../../.ssh/authorized_keys" is a filename as far as the API is
// concerned, and joining it onto a directory the operator named would write
// wherever the sender chose.
//
// Both separators, because a Unix host is not safe from a Windows path. A `\`
// is an ordinary character in a Unix filename, so filepath.Base leaves
// "..\\..\\evil" whole, and joining that produces one strangely named file
// inside the directory rather than an escape. It is still replaced, because the
// name is going to be read by a person who should not have to work that out.
//
// What is left after all of it can still be empty, a dot, or a pair of dots,
// and each of those is a path element rather than a name. Those become a
// generic name instead: a download that lands as "attachment" is worse to look
// at and impossible to misread.
func SafeFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', 0:
			return '_'
		}
		return r
	}, name)

	cleaned = filepath.Base(strings.TrimSpace(cleaned))
	switch cleaned {
	case "", ".", "..", string(filepath.Separator):
		return "attachment"
	}
	return cleaned
}
