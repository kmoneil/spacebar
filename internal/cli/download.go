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
	"unicode/utf8"

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

Nor can the directory take them somewhere else. A symlink already sitting there
under the name an attachment happens to have is refused rather than followed,
so a download cannot write through one into a file outside --out. That matters
where the directory is not only yours: a shared CI workspace, /tmp, a synced
folder.

An existing file is not overwritten unless --force says so, because the name is
not yours and a download should not be able to replace something you have.
--force replaces the name rather than following it, so it overwrites the file
you can see and not whatever a link points at.

A Drive attachment is not downloaded. Chat returns a reference to it rather
than the bytes, and fetching it is Drive's API rather than this one. Those are
listed and skipped rather than silently missing.`,

		Args: exactlyOne("messages download needs a message.\n" +
			"  %s messages download spaces/AAAAAAA/messages/BBBBBBB"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDownload(cmd, opts, args[0], out, force)
		},
	}

	f := cmd.Flags()
	f.StringVar(&out, "out", ".", "the directory to write into")
	f.BoolVar(&force, "force", false, "overwrite a file that is already there")
	return cmd
}

// runDownload is the whole command, split out of it because opening the root
// took it over the complexity ceiling.
//
// The split is where the two jobs already were: this fetches the message and
// decides where the files go, and downloadOne fetches and writes one of them.
func runDownload(cmd *cobra.Command, opts *Options, message, out string, force bool) error {
	r := renderer(cmd, opts)

	opened, err := openProfile(opts, r)
	if err != nil {
		return err
	}
	if err := transport.Require(opened.Transport, "messages download", transport.CanRead); err != nil {
		return err
	}

	found, err := opened.Transport.GetMessage(cmd.Context(), message)
	if err != nil {
		return finish(r, opened, err)
	}
	if len(found.Attachment) == 0 {
		r.Note("that message has no attachments.")
		return nil
	}

	dir, err := downloadDir(out)
	if err != nil {
		return err
	}

	// Opened once for the whole message rather than per attachment, and held
	// for as long as the writing lasts. What it is for is in writeInto: every
	// file lands through this handle, so a symlink somebody planted in the
	// directory cannot take the bytes anywhere else.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return output.Usagef("cannot write into %s: %v", dir, err)
	}
	defer func() { _ = root.Close() }()

	for _, file := range found.Attachment {
		if err := downloadOne(cmd, r, opened, root, dir, file, force); err != nil {
			return finish(r, opened, err)
		}
	}
	return nil
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
func downloadOne(cmd *cobra.Command, r *output.Renderer, opened *profile.Open, root *os.Root, dir string,
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

	name := SafeFilename(file.ContentName)
	path := filepath.Join(dir, name)
	if err := writeInto(root, name, path, body, force); err != nil {
		return err
	}

	return r.Item(downloadRow{
		Path:        path,
		Name:        file.Name,
		ContentName: file.ContentName,
		ContentType: file.ContentType,
		Bytes:       len(body),
	}, path, fmt.Sprintf("%d", len(body)), file.ContentType)
}

// tempPrefix is what a --force write is staged under before it is renamed into
// place. A leading dot so that an interrupted run leaves something a directory
// listing does not lead with, and the product name from meta so that a rename
// stays a change to that constant.
const tempPrefix = "." + meta.AppName + "-download-"

// writeInto writes one attachment's bytes to name inside root.
//
// This is the half of the containment claim that SafeFilename does not make.
// SafeFilename decides what the name may be, and it is correct: whatever came
// off the message, what comes out is one name with no separator in it. What it
// cannot do is say anything about what is already sitting in the directory
// under that name, and the write used to be:
//
//	if _, err := os.Stat(path); err == nil { ...refuse... }
//	os.WriteFile(path, body, mode)
//
// os.Stat follows symlinks, so a **dangling** symlink at that path answers
// ErrNotExist, the existence guard passes, and os.WriteFile follows the same
// link and creates the file at its target. Attachment bytes are chosen by
// whoever posted the message, so somebody who could plant a name in the
// download directory could write content of their choosing anywhere the
// operator can write. A shared CI workspace, --out /tmp, or a synced folder is
// all that takes. There was a plain check-then-use race beside it, and the
// dangling link needed no race at all.
//
// Everything goes through os.Root, which resolves each component against the
// directory handle and refuses to leave it. Two shapes, because the two answers
// are different:
//
//   - Without --force, O_CREATE|O_EXCL. It refuses anything already at that
//     name, symlink or not, dangling or not, and it is the existence check as
//     well as the write, so there is no window between them to lose.
//   - With --force, the bytes are staged under a temporary name in the same
//     directory and renamed over the target. A rename replaces the name rather
//     than following what it points at, which is what --force means: overwrite
//     the file with this name, not whatever this name refers to. It is also
//     atomic, so an interrupted overwrite leaves the old file rather than half
//     of a new one.
//
// os.Root is also what refuses a Windows reserved device name: os.Root's own
// documentation says file names may not reference NUL, COM1 and the rest. On
// Windows a download called NUL would otherwise write to the null device, with
// the bytes discarded and os.Stat answering as though a file were there. It is
// refused at the platform-facing API rather than by a list kept here, which is
// the right place for it: a list would have to be maintained against somebody
// else's operating system.
func writeInto(root *os.Root, name, path string, body []byte, force bool) error {
	if !force {
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, config.FileMode)
		if errors.Is(err, os.ErrExist) {
			return output.Errorf("EXISTS", output.ExitUsage,
				"%s is already there, and the name came from the message rather than from you.\n"+
					"Pass --force to overwrite it, or --out to write somewhere else.", path)
		}
		if err != nil {
			return output.Usagef("cannot write %s: %v", path, err)
		}
		return writeAndClose(f, path, body)
	}

	staged := stagedName(name)
	temp, err := root.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, config.FileMode)
	if err != nil {
		return output.Usagef("cannot write %s: %v", path, err)
	}
	if err := writeAndClose(temp, path, body); err != nil {
		_ = root.Remove(staged)
		return err
	}
	if err := root.Rename(staged, name); err != nil {
		_ = root.Remove(staged)
		return output.Usagef("cannot replace %s: %v", path, err)
	}
	return nil
}

// maxFilenameBytes is the length a single name has to fit in.
//
// 255 on every filesystem this is likely to meet: ext4, APFS, NTFS and ZFS all
// stop there. It is not read from anywhere because nothing portable reports it,
// and being wrong in the safe direction costs a truncated temporary name that
// nobody sees.
const maxFilenameBytes = 255

// stagedName is the temporary name a --force write is built under.
//
// The prefix cannot simply be prepended. A name of 236 to 255 bytes fits a
// filesystem on its own and does not once nineteen more are in front of it, so
// prepending would have made --force fail on a band of names that worked
// before, and only on that band, which is the kind of regression that is found
// by somebody with an unusual attachment rather than by a test.
//
// Cut on a rune boundary, because the result is a name a person may see in a
// directory listing after an interrupted run and half a character is worse to
// look at than a short one. Two long names that truncate to the same staged
// name collide, and the O_EXCL open refuses the second rather than letting one
// download overwrite the other's temporary file.
func stagedName(name string) string {
	staged := tempPrefix + name
	if len(staged) <= maxFilenameBytes {
		return staged
	}

	staged = staged[:maxFilenameBytes]
	for len(staged) > len(tempPrefix) {
		r, size := utf8.DecodeLastRuneInString(staged)
		if r != utf8.RuneError || size != 1 {
			break
		}
		staged = staged[:len(staged)-1]
	}
	return staged
}

// writeAndClose writes the bytes and closes the handle, naming the path a
// person asked for rather than the one inside the root.
func writeAndClose(f *os.File, path string, body []byte) error {
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return output.Usagef("cannot write %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		return output.Usagef("cannot write %s: %v", path, err)
	}
	return nil
}

// nameOf is what to call an attachment in a message about it.
func nameOf(file chat.Attachment) string {
	if file.ContentName != "" {
		return file.ContentName
	}
	return file.Name
}

// fallbackFilename is what a name that cannot be one becomes.
//
// A download that lands as "attachment" is worse to look at and impossible to
// misread, which is the trade every branch below makes.
const fallbackFilename = "attachment"

// SafeFilename reduces a name that came from a message to one that cannot leave
// the directory it is joined onto.
//
// The name is chosen by whoever posted the message, which is the whole reason
// this exists: "../../.ssh/authorized_keys" is a filename as far as the API is
// concerned, and joining it onto a directory the operator named would write
// wherever the sender chose.
//
// This decides what the name may be and says nothing about what is already in
// the directory under it. That is writeInto's half, and the two are here
// together because a reader who finds one and not the other would reasonably
// conclude the claim was whole.
//
// Both separators, because a Unix host is not safe from a Windows path. A `\`
// is an ordinary character in a Unix filename, so filepath.Base leaves
// "..\\..\\evil" whole, and joining that produces one strangely named file
// inside the directory rather than an escape. It is still replaced, because the
// name is going to be read by a person who should not have to work that out.
//
// A colon goes the same way and for the same reason, which is the one this did
// not do. On Windows "report.txt:hidden" is not a filename, it is an alternate
// data stream on a file called report.txt, so a download under that name writes
// into a file the operator already has rather than creating one, and a
// directory listing afterwards shows nothing new. It is an ordinary character
// on Unix, exactly as a backslash is, and is replaced on both for exactly the
// reason a backslash is: one answer, wherever it ran.
//
// What is left after all of it can still be empty, a dot, or a pair of dots,
// and each of those is a path element rather than a name.
//
// filepath.IsLocal is the layer under that, and it is deliberately not the only
// one. It is what would refuse a Windows reserved device name lexically, and on
// Unix it refuses almost nothing this has not already handled, so it earns its
// place as a second opinion rather than as the check: what actually refuses
// NUL and COM1 where it matters is os.Root, in writeInto, at the platform's own
// boundary rather than in a rule written here.
func SafeFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', 0:
			return '_'
		}
		return r
	}, name)

	cleaned = filepath.Base(strings.TrimSpace(cleaned))
	switch cleaned {
	case "", ".", "..", string(filepath.Separator):
		return fallbackFilename
	}
	if !filepath.IsLocal(cleaned) {
		return fallbackFilename
	}
	return cleaned
}
