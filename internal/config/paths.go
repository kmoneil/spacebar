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

package config

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/kmoneil/spacebar/internal/meta"
)

// FileName is the configuration file inside Dir.
const FileName = "config.json"

// DirMode and FileMode are what this tool creates. SPEC.md §15.2 sets both.
//
// config.json holds no secrets, by construction and by assertion: every field
// that could carry one holds a reference instead, and Load refuses a file where
// that is not true. The modes are still tight, because the next file in this
// directory is the credential fallback from §5.3, and a directory somebody
// widened once stays wide.
const (
	DirMode  os.FileMode = 0o700
	FileMode os.FileMode = 0o600
)

// Dir returns the directory this tool keeps its configuration in.
//
// SPEC.md §5.1 asks for $XDG_CONFIG_HOME, falling back to ~/.config, with
// %AppData% on Windows. That is deliberately not os.UserConfigDir, which agrees
// on Unix and Windows and then returns ~/Library/Application Support on macOS.
// A macOS user of a terminal tool looks in ~/.config, the spec names ~/.config,
// and a config file somewhere the documentation does not mention is a config
// file its owner cannot find.
//
// XDG_CONFIG_HOME is honoured first everywhere, including on Windows, because
// somebody who sets it has said where they want their files.
func Dir() (string, error) {
	if v, ok := os.LookupEnv("XDG_CONFIG_HOME"); ok && v != "" {
		// The base directory specification says to ignore a relative value.
		// Ignoring it silently would put the file somewhere other than where
		// the person who set the variable expects, and they would have no way
		// to tell, so this refuses instead.
		if !filepath.IsAbs(v) {
			return "", configErr("XDG_CONFIG_HOME is not an absolute path, so there is no directory to use.\n"+
				"Set it to an absolute path, or unset it to use ~/.config/%s.", meta.AppName)
		}
		return filepath.Join(v, meta.AppName), nil
	}

	if runtime.GOOS == "windows" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", configErr("cannot locate %%AppData%%: %v", err)
		}
		return filepath.Join(base, meta.AppName), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", configErr("cannot locate the home directory: %v\n"+
			"Set XDG_CONFIG_HOME to an absolute path.", err)
	}
	return filepath.Join(home, ".config", meta.AppName), nil
}

// Path returns the configuration file.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}
