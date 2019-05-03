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

package hugofs

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/pkg/errors"

	"github.com/spf13/afero"
)

type WalkFunc func(info FileMetaInfo, err error) error

type Walkway struct {
	fs   afero.Fs
	root string
	fi   FileMetaInfo

	walkFn WalkFunc
	walked bool

	// We may traverse symbolic links and bite ourself.
	seen map[string]bool
}

func NewWalkway(fs afero.Fs, root string, walkFn WalkFunc) *Walkway {
	return &Walkway{
		fs:     fs,
		root:   root,
		walkFn: walkFn,
		seen:   make(map[string]bool)}
}

func NewWalkwayFromFi(fi FileMetaInfo, walkFn WalkFunc) *Walkway {
	return &Walkway{
		fs:     fi.Meta().Fs(),
		fi:     fi,
		walkFn: walkFn,
		seen:   make(map[string]bool)}
}

// TODO(bep) make content use this
func (w *Walkway) Walk() error {
	if w.walked {
		panic("this walkway is already walked")
	}
	w.walked = true

	if w.fs == NoOpFs {
		return nil
	}

	var fi FileMetaInfo
	if w.fi != nil {
		fi = w.fi
	} else {
		info, err := lstatIfPossible(w.fs, w.root)
		if err != nil {
			return w.walkFn(nil, errors.Wrap(err, "walk"))
		}
		fi = info.(FileMetaInfo)
	}

	if !fi.IsDir() {
		return w.walkFn(nil, errors.New("file to walk must be a directory"))
	}

	return w.walk(w.root, fi, w.walkFn)

}

// if the filesystem supports it, use Lstat, else use fs.Stat
func lstatIfPossible(fs afero.Fs, path string) (os.FileInfo, error) {
	if lfs, ok := fs.(afero.Lstater); ok {
		fi, _, err := lfs.LstatIfPossible(path)
		return fi, err
	}
	return fs.Stat(path)
}

// walk recursively descends path, calling walkFn.
// It follow symlinks if supported by the filesystem, but only the same path once.
func (w *Walkway) walk(path string, info FileMetaInfo, walkFn WalkFunc) error {
	err := walkFn(info, nil)
	if err != nil {
		if info.IsDir() && err == filepath.SkipDir {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}

	var isSym bool
	info, isSym, err = w.resolveSymlink(info)
	if err != nil {
		return walkFn(info, err)
	}

	meta := info.Meta()
	filename := meta.Filename()
	filenameToOpen := path // may be a composite
	if isSym {
		// Symlinks will only work in the filesystems defined by the project,
		// (not theme components), and we do follow them.
		filenameToOpen = filename

	}

	// Prevent infinite recursion.
	w.isSeen(filename)

	f, err := w.fs.Open(filenameToOpen)

	if err != nil {
		return walkFn(info, errors.Wrapf(err, "walk: open %q (path: %q)", filenameToOpen, path))
	}

	fis, err := f.Readdir(-1)
	f.Close()
	if err != nil {
		return walkFn(info, err)
	}

	if !meta.IsOrdered() {
		sort.Slice(fis, func(i, j int) bool {
			fii := fis[i].(FileMetaInfo)
			fij := fis[j].(FileMetaInfo)
			return fii.Name() < fij.Name()
		})
	}

	for _, fi := range fis {
		fim := fi.(FileMetaInfo)
		var err error
		var isSym bool
		fim, isSym, err = w.resolveSymlink(fim)

		if err != nil {
			return walkFn(fim, err)
		}

		if fim.IsDir() {
			// Prevent infinite recursion
			filename := fim.Meta().Filename()
			if w.isSeen(filename) && isSym {
				// Possible cyclic reference
				// TODO(bep) mod check if we log some warning about this in the
				// existing content walker.
				continue
			}
		}

		// Note that we use the original Name even if it's a symlink.
		err = w.walk(filepath.Join(path, fi.Name()), fim, walkFn)
		if err != nil {
			if !fi.IsDir() || err != filepath.SkipDir {
				return err
			}
		}
	}
	return nil
}

func (w *Walkway) isSeen(filename string) bool {
	if w.seen[filename] {
		return true
	}

	w.seen[filename] = true
	return false
}

func (w *Walkway) resolveSymlink(fi FileMetaInfo) (FileMetaInfo, bool, error) {
	if !isSymlink(fi) {
		// Nothing to do
		return fi, false, nil
	}

	meta := fi.Meta()

	// If it's a symlink it is the OS filesystem.
	link, err := filepath.EvalSymlinks(meta.Filename())
	if err != nil {
		return fi, false, err
	}

	lfi, err := os.Stat(link)
	if err != nil {
		return nil, false, err
	}

	// TODO(bep) mod consider this, maybe add a symlink flag or something.
	// TODO(bep) mod probably better to preserve the source filename and not link (for errors)
	meta[metaKeyFilename] = link

	return NewFileMetaInfo(lfi, meta), true, nil

}
