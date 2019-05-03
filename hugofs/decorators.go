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
	"strings"

	"github.com/spf13/afero"
)

func decorateDirs(fs afero.Fs, meta FileMeta) afero.Fs {
	ffs := &fileDecoratorFs{Fs: fs}

	decorator := func(fi os.FileInfo, name string) (os.FileInfo, error) {
		if !fi.IsDir() {
			// Leave regular files as they are.
			return fi, nil
		}

		return decorateFileInfo("dird", fi, fs, nil, "", "", meta), nil
	}

	ffs.decorate = decorator

	return ffs

}

func decoratePath(fs afero.Fs, createPath func(name string) string) afero.Fs {

	ffs := &fileDecoratorFs{Fs: fs}

	decorator := func(fi os.FileInfo, name string) (os.FileInfo, error) {
		path := createPath(name)

		return decorateFileInfo("pathd", fi, fs, nil, "", path, nil), nil
	}

	ffs.decorate = decorator

	return ffs

}

// DecorateBasePathFs adds Path info to files and directories in the
// provided BasePathFs, using the base as base.
func DecorateBasePathFs(base *afero.BasePathFs) afero.Fs {
	basePath, _ := base.RealPath("")
	if !strings.HasSuffix(basePath, filepathSeparator) {
		basePath += filepathSeparator
	}

	ffs := &fileDecoratorFs{Fs: base}

	decorator := func(fi os.FileInfo, name string) (os.FileInfo, error) {
		path := strings.TrimPrefix(name, basePath)

		return decorateFileInfo("bpd", fi, base, nil, "", path, nil), nil
	}

	ffs.decorate = decorator

	return ffs
}

// NewFilenameDecorator decorates the given Fs to provide the real filename
// and an Opener func.
func NewFilenameDecorator(fs afero.Fs) afero.Fs {

	ffs := &fileDecoratorFs{Fs: fs}

	decorator := func(fi os.FileInfo, filename string) (os.FileInfo, error) {
		opener := func() (afero.File, error) {
			return fs.Open(filename)
		}

		return decorateFileInfo("fnd", fi, fs, opener, filename, "", nil), nil
	}

	ffs.decorate = decorator
	return ffs
}

// NewCompositeDirDecorator decorates the given filesystem to make sure
// that directories is always opened by that filesystem.
// TODO(bep) mod check if needed
func NewCompositeDirDecorator(fs afero.Fs) afero.Fs {

	decorator := func(fi os.FileInfo, name string) (os.FileInfo, error) {
		if !fi.IsDir() {
			return fi, nil
		}
		opener := func() (afero.File, error) {
			return fs.Open(name)
		}

		return decorateFileInfo("compd", fi, fs, opener, "", "", nil), nil
	}

	return &fileDecoratorFs{Fs: fs, decorate: decorator}
}

type fileDecoratorFs struct {
	afero.Fs
	decorate func(fi os.FileInfo, filename string) (os.FileInfo, error)
}

func (fs *fileDecoratorFs) Stat(name string) (os.FileInfo, error) {
	fi, err := fs.Fs.Stat(name)
	if err != nil {
		return nil, err
	}

	return fs.decorate(fi, name)

}

func (b *fileDecoratorFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	var (
		fi  os.FileInfo
		err error
		ok  bool
	)

	if lstater, isLstater := b.Fs.(afero.Lstater); isLstater {
		fi, ok, err = lstater.LstatIfPossible(name)
	} else {
		fi, err = b.Fs.Stat(name)
	}

	if err != nil {
		return nil, false, err
	}

	fi, err = b.decorate(fi, name)

	return fi, ok, err
}

func (fs *fileDecoratorFs) Open(name string) (afero.File, error) {
	f, err := fs.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	return &fileDecoratorFile{File: f, fs: fs}, nil
}

type fileDecoratorFile struct {
	afero.File
	fs *fileDecoratorFs
}

func (l *fileDecoratorFile) Readdir(c int) (ofi []os.FileInfo, err error) {
	fis, err := l.File.Readdir(c)
	if err != nil {
		return nil, err
	}

	fisp := make([]os.FileInfo, len(fis))

	for i, fi := range fis {
		filename := filepath.Join(l.Name(), fi.Name())
		fi, err = l.fs.decorate(fi, filename)
		if err != nil {
			return nil, err
		}
		fisp[i] = fi
	}

	return fisp, err
}
