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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gohugoio/hugo/modules"

	"github.com/pkg/errors"

	radix "github.com/hashicorp/go-immutable-radix"
	"github.com/spf13/afero"
)

var filepathSeparator = string(filepath.Separator)

// NewRootMappingFs creates a new RootMappingFs on top of the provided with
// of root mappings with some optional metadata about the root.
// Note that From represents a virtual root that maps to the actual filename in To.
func NewRootMappingFs(fs afero.Fs, rms ...RootMapping) (*RootMappingFs, error) {
	rootMapToReal := radix.New().Txn()

	for _, rm := range rms {
		(&rm).clean()

		fromBase := modules.ResolveComponentFolder(rm.From)
		if fromBase == "" {
			panic("TODO(bep) unrecognised base: " + rm.From) // TODO(bep) mod
		}

		if len(rm.To) < 2 {
			panic(fmt.Sprintf("invalid root mapping; from/to: %s/%s", rm.From, rm.To))
		}

		_, err := fs.Stat(rm.To)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		rm.path = strings.TrimPrefix(strings.TrimPrefix(rm.From, fromBase), filepathSeparator)
		key := []byte(rm.rootKey())
		var mappings []RootMapping
		v, found := rootMapToReal.Get(key)
		if found {
			// There may be more than one language pointing to the same root.
			mappings = v.([]RootMapping)
		}
		mappings = append(mappings, rm)
		rootMapToReal.Insert(key, mappings)
	}

	rfs := &RootMappingFs{Fs: fs,
		virtualRoots:  rms,
		rootMapToReal: rootMapToReal.Commit().Root()}

	return rfs, nil
}

// NewRootMappingFsFromFromTo is a convenicence variant of NewRootMappingFs taking
// From and To as string pairs.
func NewRootMappingFsFromFromTo(fs afero.Fs, fromTo ...string) (*RootMappingFs, error) {
	rms := make([]RootMapping, len(fromTo)/2)
	for i, j := 0, 0; j < len(fromTo); i, j = i+1, j+2 {
		rms[i] = RootMapping{
			From: fromTo[j],
			To:   fromTo[j+1],
		}

	}

	return NewRootMappingFs(fs, rms...)
}

type RootMapping struct {
	From string
	path string // everything below content, i18n, layouts...
	To   string

	// File metadata (lang etc.)
	Meta FileMeta
}

func (rm *RootMapping) clean() {
	rm.From = filepath.Clean(rm.From)
	rm.To = filepath.Clean(rm.To)
}

func (r RootMapping) filename(name string) string {
	return filepath.Join(r.To, strings.TrimPrefix(name, r.From))
}

func (r RootMapping) rootKey() string {
	return r.From
}

// A RootMappingFs maps several roots into one. Note that the root of this filesystem
// is directories only, and they will be returned in Readdir and Readdirnames
// in the order given.
type RootMappingFs struct {
	afero.Fs
	rootMapToReal *radix.Node
	virtualRoots  []RootMapping
}

func (fs *RootMappingFs) Dirs(base string) ([]FileMetaInfo, error) {
	roots := fs.getRootsWithPrefix(base)

	if roots == nil {
		return nil, nil
	}

	fss := make([]FileMetaInfo, len(roots))
	for i, r := range roots {
		bfs := afero.NewBasePathFs(fs.Fs, r.To)
		bfs = decoratePath(bfs, func(name string) string {
			p := strings.TrimPrefix(name, r.To)
			if r.path != "" {
				// Make sure it's mounted to a any sub path, e.g. blog
				p = filepath.Join(r.path, p)
			}
			p = strings.TrimLeft(p, filepathSeparator)
			return p
		})
		fs := decorateDirs(bfs, r.Meta)
		fi, err := fs.Stat("")
		if err != nil {
			return nil, errors.Wrap(err, "RootMappingFs.Dirs")
		}
		fss[i] = fi.(FileMetaInfo)
	}

	return fss, nil
}

type MultiFileInfo interface {
	FileMetaInfo
	Fis() []FileMetaInfo
}

type multiFileInfo struct {
	FileMetaInfo
	fis []FileMetaInfo
}

func newMultiFileInfo(fis ...FileMetaInfo) os.FileInfo {
	return &multiFileInfo{FileMetaInfo: fis[0], fis: fis}
}

func (fi *multiFileInfo) Fis() []FileMetaInfo {
	return fi.fis
}

func (fs *RootMappingFs) statRoot(root RootMapping, name string) (os.FileInfo, bool, error) {
	filename := root.filename(name)

	var b bool
	var fi os.FileInfo
	var err error

	if ls, ok := fs.Fs.(afero.Lstater); ok {
		fi, b, err = ls.LstatIfPossible(filename)
		if err != nil {
			return nil, b, err
		}

	} else {
		fi, err = fs.Fs.Stat(filename)
		if err != nil {
			return nil, b, err
		}
	}

	// TODO(bep) mod check this vs base
	opener := func() (afero.File, error) {
		return fs.Fs.Open(filename)
	}

	return decorateFileInfo("rm-fs", fi, fs.Fs, opener, "", "", root.Meta), b, nil
}

// Open opens the named file for reading.
func (fs *RootMappingFs) Open(name string) (afero.File, error) {
	if fs.isRoot(name) {
		return &rootMappingFile{name: name, fs: fs}, nil
	}

	fi, err := fs.Stat(name)
	if err != nil {
		return nil, err
	}

	multifi, ok := fi.(MultiFileInfo)
	if !ok {
		return fi.(FileMetaInfo).Meta().Open()
	}

	if !multifi.IsDir() {
		return nil, errors.New("multiple matches in Open()")
	}

	// Create a union file
	// UnionFile base, layer on top
	return fs.newUnionFile(multifi.Fis()...)

}

func (fs *RootMappingFs) newUnionFile(fis ...FileMetaInfo) (afero.File, error) {
	f, err := fis[0].Meta().Open()
	if len(fis) == 1 {
		return f, err
	}

	next, err := fs.newUnionFile(fis[1:]...)
	if err != nil {
		return nil, err
	}

	uf := &afero.UnionFile{Base: f, Layer: next}

	uf.Merger = func(lofi, bofi []os.FileInfo) ([]os.FileInfo, error) {
		return append(bofi, lofi...), nil
	}

	return uf, nil

}

func (fs *RootMappingFs) open(root RootMapping, name string) (afero.File, error) {
	filename := root.filename(name)
	f, err := fs.Fs.Open(filename)
	if err != nil {
		return nil, err
	}
	return &rootMappingFile{File: f, name: name, fs: fs, rm: root}, nil
}

// LstatIfPossible returns the os.FileInfo structure describing a given file.
// It attempts to use Lstat if supported or defers to the os.  In addition to
// the FileInfo, a boolean is returned telling whether Lstat was called.
func (fs *RootMappingFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {

	if fs.isRoot(name) {
		opener := func() (afero.File, error) {
			return &rootMappingFile{name: name, fs: fs}, nil
		}
		return newDirNameOnlyFileInfo(name, true, opener), false, nil
	}

	roots := fs.getRoots(name)
	if len(roots) == 0 {
		return nil, false, os.ErrNotExist
	}

	var (
		fis []FileMetaInfo
		b   bool
		fi  os.FileInfo
		err error
	)

	for _, root := range roots {
		fi, b, err = fs.statRoot(root, name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, false, err
		}
		fis = append(fis, fi.(FileMetaInfo))
	}

	if len(fis) == 0 {
		return nil, false, os.ErrNotExist
	}

	if len(fis) == 1 {
		return fis[0], b, nil
	}

	return newMultiFileInfo(fis...), b, nil

}

// Stat returns the os.FileInfo structure describing a given file.  If there is
// an error, it will be of type *os.PathError.
func (fs *RootMappingFs) Stat(name string) (os.FileInfo, error) {
	fi, _, err := fs.LstatIfPossible(name)
	return fi, err

}

func (fs *RootMappingFs) isRoot(name string) bool {
	return name == "" || name == filepathSeparator

}

func (fs *RootMappingFs) getRoot(name string) (RootMapping, bool, error) {
	roots := fs.getRoots(name)
	if len(roots) == 0 {
		return RootMapping{}, false, nil
	}
	if len(roots) > 1 {
		return RootMapping{}, false, errors.Errorf("got %d matches for %q (%v)", len(roots), name, roots)
	}

	return roots[0], true, nil
}

func (fs *RootMappingFs) getRoots(name string) []RootMapping {
	nameb := []byte(filepath.Clean(name))
	_, v, found := fs.rootMapToReal.LongestPrefix(nameb)
	if !found {
		return nil
	}

	return v.([]RootMapping)
}

func (fs *RootMappingFs) getRootsWithPrefix(prefix string) []RootMapping {
	prefixb := []byte(filepath.Clean(prefix))
	var roots []RootMapping

	fs.rootMapToReal.WalkPrefix(prefixb, func(b []byte, v interface{}) bool {
		roots = append(roots, v.([]RootMapping)...)
		return false
	})

	return roots
}

type rootMappingFile struct {
	afero.File
	fs   *RootMappingFs
	name string
	rm   RootMapping
}

func (f *rootMappingFile) Close() error {
	if f.File == nil {
		return nil
	}
	return f.File.Close()
}

func (f *rootMappingFile) Name() string {
	return f.name
}

func (f *rootMappingFile) Readdir(count int) ([]os.FileInfo, error) {

	if f.fs.isRoot(f.name) {
		dirsn := make([]os.FileInfo, 0)
		for i := 0; i < len(f.fs.virtualRoots); i++ {
			if count != -1 && i >= count {
				break
			}

			rm := f.fs.virtualRoots[i]

			opener := func() (afero.File, error) {
				return f.fs.Open(rm.From)
			}

			fi := newDirNameOnlyFileInfo(rm.From, false, opener)
			if rm.Meta != nil {
				mergeFileMeta(rm.Meta, fi.Meta())
			}
			dirsn = append(dirsn, fi)
		}
		return dirsn, nil
	}

	if f.File == nil {
		panic(fmt.Sprintf("no File for %s", f.name))
	}

	fis, err := f.File.Readdir(count)
	if err != nil {
		return nil, err
	}

	for i, fi := range fis {

		fis[i] = decorateFileInfo("rm-f", fi, f.fs, nil, "", filepath.Join(f.rm.path, fi.Name()), nil)

	}

	return fis, nil

}

func (f *rootMappingFile) Readdirnames(count int) ([]string, error) {
	dirs, err := f.Readdir(count)
	if err != nil {
		return nil, err
	}
	dirss := make([]string, len(dirs))
	for i, d := range dirs {
		dirss[i] = d.Name()
	}
	return dirss, nil
}
