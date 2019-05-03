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
	"testing"

	"github.com/pkg/errors"

	"github.com/gohugoio/hugo/htesting"

	"github.com/spf13/afero"

	"github.com/stretchr/testify/require"
)

func TestWalk(t *testing.T) {
	assert := require.New(t)

	fs := NewFilenameDecorator(afero.NewMemMapFs())

	afero.WriteFile(fs, "b.txt", []byte("content"), 0777)
	afero.WriteFile(fs, "a.txt", []byte("content"), 0777)
	afero.WriteFile(fs, "c.txt", []byte("content"), 0777)

	names, err := collectFilenames(fs, "", "")

	assert.NoError(err)
	assert.Equal([]string{"/a.txt", "/b.txt", "/c.txt"}, names)
}

func TestWalkRootMappingFs(t *testing.T) {
	assert := require.New(t)
	fs := NewFilenameDecorator(afero.NewMemMapFs())

	testfile := "test.txt"

	assert.NoError(afero.WriteFile(fs, filepath.Join("a/b", testfile), []byte("some content"), 0755))
	assert.NoError(afero.WriteFile(fs, filepath.Join("c/d", testfile), []byte("some content"), 0755))
	assert.NoError(afero.WriteFile(fs, filepath.Join("e/f", testfile), []byte("some content"), 0755))

	rm := []RootMapping{
		RootMapping{
			From: "static/b",
			To:   "e/f",
		},
		RootMapping{
			From: "static/a",
			To:   "c/d",
		},

		RootMapping{
			From: "static/c",
			To:   "a/b",
		},
	}

	rfs, err := NewRootMappingFs(fs, rm...)
	assert.NoError(err)

	names, err := collectFilenames(rfs, "", "")

	assert.NoError(err)
	assert.Equal([]string{"e/f/test.txt", "c/d/test.txt", "a/b/test.txt"}, names)

}

func TestWalkSymbolicLink(t *testing.T) {
	assert := require.New(t)
	workDir, clean, err := htesting.CreateTempDir(Os, "hugo-walk-sym")
	assert.NoError(err)
	defer clean()
	wd, _ := os.Getwd()
	defer func() {
		os.Chdir(wd)
	}()

	fs := NewFilenameDecorator(Os)

	blogDir := filepath.Join(workDir, "blog")
	docsDir := filepath.Join(workDir, "docs")
	blogReal := filepath.Join(blogDir, "real")
	assert.NoError(os.MkdirAll(blogReal, 0777))
	assert.NoError(os.MkdirAll(docsDir, 0777))
	afero.WriteFile(fs, filepath.Join(blogReal, "a.txt"), []byte("content"), 0777)
	afero.WriteFile(fs, filepath.Join(docsDir, "b.txt"), []byte("content"), 0777)

	os.Chdir(blogDir)
	assert.NoError(os.Symlink("real", "symlinked"))
	os.Chdir(blogReal)
	assert.NoError(os.Symlink("../real", "cyclic"))
	os.Chdir(docsDir)
	assert.NoError(os.Symlink("../blog/real/cyclic", "docsreal"))

	t.Run("OS Fs", func(t *testing.T) {
		assert := require.New(t)

		names, err := collectFilenames(fs, workDir, workDir)
		assert.NoError(err)

		assert.Equal([]string{"WORK_DIR/blog/real/a.txt", "WORK_DIR/docs/b.txt"}, names)
	})

	t.Run("BasePath Fs", func(t *testing.T) {
		assert := require.New(t)

		docsFs := afero.NewBasePathFs(fs, docsDir)

		names, err := collectFilenames(docsFs, workDir, "")
		assert.NoError(err)

		// Note: the docsreal folder is considered cyclic when walking from the root, but this works.
		assert.Equal([]string{"WORK_DIR/docs/b.txt", "WORK_DIR/docs/docsreal/a.txt"}, names)
	})

}

func collectFilenames(fs afero.Fs, workDir, root string) ([]string, error) {
	var names []string

	walkFn := func(info FileMetaInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		filename := info.Meta().Filename()
		filename = filepath.ToSlash(filename)
		if workDir != "" {
			filename = strings.Replace(filename, workDir, "WORK_DIR", -1)
		}
		names = append(names, filename)

		return nil
	}

	w := NewWalkway(fs, root, walkFn)

	err := w.Walk()

	return names, err

}

func BenchmarkWalk(b *testing.B) {
	assert := require.New(b)
	fs := NewFilenameDecorator(afero.NewMemMapFs())

	writeFiles := func(dir string, numfiles int) {
		for i := 0; i < numfiles; i++ {
			filename := filepath.Join(dir, fmt.Sprintf("file%d.txt", i))
			assert.NoError(afero.WriteFile(fs, filename, []byte("content"), 0777))
		}
	}

	const numFilesPerDir = 20

	writeFiles("root", numFilesPerDir)
	writeFiles("root/l1_1", numFilesPerDir)
	writeFiles("root/l1_1/l2_1", numFilesPerDir)
	writeFiles("root/l1_1/l2_2", numFilesPerDir)
	writeFiles("root/l1_2", numFilesPerDir)
	writeFiles("root/l1_2/l2_1", numFilesPerDir)
	writeFiles("root/l1_3", numFilesPerDir)

	walkFn := func(info FileMetaInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		filename := info.Meta().Filename()
		if !strings.HasPrefix(filename, "root") {
			return errors.New(filename)
		}

		return nil
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := NewWalkway(fs, "root", walkFn)

		if err := w.Walk(); err != nil {
			b.Fatal(err)
		}
	}

}
