// Copyright 2019 The Hugo Authors. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package files

import (
	"path/filepath"
	"strings"
)

var (
	// This should be the only list of valid extensions for content files.
	contentFileExtensions = []string{
		"html", "htm",
		"mdown", "markdown", "md",
		"asciidoc", "adoc", "ad",
		"rest", "rst",
		"mmark",
		"org",
		"pandoc", "pdc"}

	contentFileExtensionsSet map[string]bool
)

func init() {
	contentFileExtensionsSet = make(map[string]bool)
	for _, ext := range contentFileExtensions {
		contentFileExtensionsSet[ext] = true
	}
}

func IsContentFile(filename string) bool {
	return contentFileExtensionsSet[strings.TrimPrefix(filepath.Ext(filename), ".")]
}

func IsContentExt(ext string) bool {
	return contentFileExtensionsSet[ext]
}

const (
	ContentClassLeaf    = "leaf"
	ContentClassBranch  = "branch"
	ContentClassFile    = "zfile" // Sort below
	ContentClassContent = "zcontent"
)

func ClassifyContentFile(filename string) string {
	if !IsContentFile(filename) {
		return ContentClassFile
	}
	if strings.HasPrefix(filename, "_index.") {
		return ContentClassBranch
	}

	if strings.HasPrefix(filename, "index.") {
		return ContentClassLeaf
	}

	return ContentClassContent
}
