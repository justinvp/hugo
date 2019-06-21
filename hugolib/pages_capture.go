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

package hugolib

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/gohugoio/hugo/resources"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"

	"github.com/gohugoio/hugo/common/hugio"

	"github.com/gohugoio/hugo/resources/resource"

	"github.com/gohugoio/hugo/source"

	"github.com/gohugoio/hugo/hugofs"

	"github.com/gohugoio/hugo/common/loggers"
	"github.com/spf13/afero"
)

const contentClassifierMetaKey = "classifier"

const (
	contentClassifierLeaf    = "branch"
	contentClassifierBranch  = "leaf"
	contentClassifierFile    = "zfile" // Sort below
	contentClassifierContent = "zcontent"
)

func newPagesCollector(sp *source.SourceSpec, logger *loggers.Logger, proc pagesCollectorProcessorProvider) *pagesCollector {
	//numWorkers := config.GetNumWorkerMultiplier() * 3

	return &pagesCollector{
		fs:     sp.SourceFs,
		proc:   proc,
		sp:     sp,
		logger: logger,
	}
}

type fileinfoBundle struct {
	header    hugofs.FileMetaInfo
	resources []hugofs.FileMetaInfo
}

type pagesCollector struct {
	sp         *source.SourceSpec
	fs         afero.Fs
	logger     *loggers.Logger
	numWorkers int

	proc pagesCollectorProcessorProvider
}

func (c *pagesCollector) Collect() error {
	c.proc.Start(context.Background())

	preHook := func(dir hugofs.FileMetaInfo, path string, readdir []hugofs.FileMetaInfo) error {

		var (
			isBranchBundle bool
			isLeafBundle   bool
		)

		setClassifier := func(fi hugofs.FileMetaInfo, classifier string) {
			fi.Meta()[contentClassifierMetaKey] = classifier
		}

		for _, fi := range readdir {
			if fi.IsDir() {
				continue
			}
			tp, isContent := classifyBundledFile(fi.Name())

			switch tp {
			case bundleLeaf:
				setClassifier(fi, contentClassifierLeaf)
				isLeafBundle = true
			case bundleBranch:
				setClassifier(fi, contentClassifierBranch)
				isBranchBundle = true
				break
			case bundleNot:
				classifier := contentClassifierFile
				if isContent {
					classifier = contentClassifierContent
				}
				setClassifier(fi, classifier)
			}
		}

		if isBranchBundle {
			if err := c.handleBundleBranch(readdir); err != nil {
				return err
			}
			// A branch bundle is only this directory level, so keep walking.
			return nil
		} else if isLeafBundle {
			if err := c.handleBundleLeaf(dir, path, readdir); err != nil {
				return err
			}
			return filepath.SkipDir
		}

		if err := c.handleFiles(readdir); err != nil {
			return nil
		}

		return nil
	}

	wfn := func(info hugofs.FileMetaInfo, err error) error {
		if err != nil {
			return err
		}

		return nil
	}

	w := hugofs.NewWalkway(hugofs.WalkwayConfig{
		Fs:      c.fs,
		HookPre: preHook,
		WalkFn:  wfn})

	err := w.Walk()

	c.proc.Close()

	if err != nil {
		return err
	}

	return c.proc.Wait()

}

func (c *pagesCollector) isBundleHeader(fi hugofs.FileMetaInfo) bool {
	class := fi.Meta().Classifier()
	return class == "leaf" || class == "branch"
}

func (c *pagesCollector) getLang(fi hugofs.FileMetaInfo) string {
	lang := fi.Meta().Lang()
	if lang != "" {
		return lang
	}

	return c.sp.DefaultContentLanguage
}

func (c *pagesCollector) addToBundle(info hugofs.FileMetaInfo, bundles map[string]*fileinfoBundle) {
	getBundle := func(lang string) *fileinfoBundle {
		return bundles[lang]
	}

	cloneBundle := func(lang string) *fileinfoBundle {
		// Every bundled file needs a content file header.
		// Use the default content language if found, else just
		// pick one.
		var (
			source *fileinfoBundle
			found  bool
		)

		source, found = bundles[c.sp.DefaultContentLanguage]
		if !found {
			for _, b := range bundles {
				source = b
				break
			}
		}

		clone := c.cloneFileInfo(source.header)
		clone.Meta()["lang"] = lang

		return &fileinfoBundle{
			header: clone,
		}
	}

	lang := c.getLang(info)
	bundle := getBundle(lang)
	isBundleHeader := c.isBundleHeader(info)
	if bundle == nil {
		if isBundleHeader {
			bundle = &fileinfoBundle{header: info}
			bundles[lang] = bundle
		} else {
			bundle = cloneBundle(lang)
			bundles[lang] = bundle
		}
	}

	if !isBundleHeader {
		bundle.resources = append(bundle.resources, info)
	}
}

func (c *pagesCollector) cloneFileInfo(fi hugofs.FileMetaInfo) hugofs.FileMetaInfo {
	cm := hugofs.FileMeta{}
	meta := fi.Meta()
	if meta == nil {
		panic(fmt.Sprintf("not meta: %v", fi.Name()))
	}
	for k, v := range meta {
		cm[k] = v
	}

	return hugofs.NewFileMetaInfo(fi, cm)
}

func (c *pagesCollector) handleBundleBranch(readdir []hugofs.FileMetaInfo) error {
	c.sortBundleDir(readdir)

	// Maps bundles to its language.
	bundles := make(map[string]*fileinfoBundle)

	for _, fim := range readdir {
		if fim.IsDir() {
			continue
		}

		c.addToBundle(fim, bundles)
	}

	return c.proc.Process(bundles)

}

func (c *pagesCollector) handleBundleLeaf(dir hugofs.FileMetaInfo, path string, readdir []hugofs.FileMetaInfo) error {

	c.sortBundleDir(readdir)

	// Maps bundles to its language.
	bundles := make(map[string]*fileinfoBundle)

	walk := func(info hugofs.FileMetaInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		c.addToBundle(info, bundles)

		return nil
	}

	// Start a new walker from the given path.
	w := hugofs.NewWalkway(hugofs.WalkwayConfig{
		Root:       path,
		Fs:         c.fs,
		Info:       dir,
		DirEntries: readdir,
		WalkFn:     walk})

	if err := w.Walk(); err != nil {
		return err
	}

	return c.proc.Process(bundles)

}

func (c *pagesCollector) handleFiles(fis []hugofs.FileMetaInfo) error {
	for _, fi := range fis {
		if fi.IsDir() {
			continue
		}
		if err := c.proc.Process(fi); err != nil {
			return err
		}
	}
	return nil
}

// Sort a bundle dir so the index files come first.
func (c *pagesCollector) sortBundleDir(fis []hugofs.FileMetaInfo) {
	sort.Slice(fis, func(i, j int) bool {
		fii, fij := fis[i], fis[j]
		fim, fjm := fii.Meta(), fij.Meta()

		ic, jc := fim.Classifier(), fjm.Classifier()

		if ic < jc {
			return true
		}

		return fii.Name() < fij.Name()

	})
}

type pagesCollectorProcessorProvider interface {
	Close()
	Process(item interface{}) error
	Start(ctx context.Context) context.Context
	Wait() error
}

func newPagesProcessor(h *HugoSites, sp *source.SourceSpec, partialBuild bool) *pagesProcessor {
	return &pagesProcessor{
		h:            h,
		sp:           sp,
		partialBuild: partialBuild,
		pagesChan:    make(chan *pageState, 4),
	}
}

type pagesProcessor struct {
	h  *HugoSites
	sp *source.SourceSpec

	// The output Pages
	pagesChan chan *pageState

	partialBuild bool // TODO(bep) mod set

	g *errgroup.Group
}

func (proc *pagesProcessor) Close() {
	close(proc.pagesChan)
}

func (proc *pagesProcessor) sendError(err error) {
	if err == nil {
		return
	}
	proc.h.SendError(err)
}

func (proc *pagesProcessor) Process(item interface{}) error {
	send := func(p *pageState, err error) {
		if err != nil {
			proc.sendError(err)
		} else {
			proc.pagesChan <- p
		}
	}

	switch v := item.(type) {
	// Page bundles mapped to their language.
	case map[string]*fileinfoBundle:
		for _, bundle := range v {
			send(proc.newPageFromBundle(bundle))
		}
	case hugofs.FileMetaInfo:
		meta := v.Meta()
		classifier := meta.Classifier()
		switch classifier {
		case contentClassifierContent:
			send(proc.newPageFromFi(v, nil))
		case contentClassifierFile:
			proc.sendError(proc.copyFile(v))
		default:
			panic(fmt.Sprintf("invalid classifier: %q", classifier))
		}
	}

	return nil
}

func (proc *pagesProcessor) copyFile(fim hugofs.FileMetaInfo) error {
	meta := fim.Meta()
	s := proc.getSite(meta.Lang())
	f, err := meta.Open()
	if err != nil {
		return errors.Wrap(err, "copyFile: failed to open")
	}

	target := meta.Path()

	defer f.Close()

	return s.publish(&s.PathSpec.ProcessingStats.Files, target, f)

}

func (proc *pagesProcessor) Start(ctx context.Context) context.Context {
	g, ctx := proc.startProcessor(ctx)
	proc.g = g
	return ctx
}

func (proc *pagesProcessor) Wait() error { return proc.g.Wait() }

func (proc *pagesProcessor) newPageFromBundle(b *fileinfoBundle) (*pageState, error) {
	p, err := proc.newPageFromFi(b.header, nil)
	if err != nil {
		return nil, err
	}

	if len(b.resources) > 0 {

		resources := make(resource.Resources, len(b.resources))

		for i, rfi := range b.resources {
			meta := rfi.Meta()
			classifier := meta.Classifier()
			var r resource.Resource
			switch classifier {
			case contentClassifierContent:
				r, err = proc.newPageFromFi(rfi, p)
				if err != nil {
					return nil, err
				}
			case contentClassifierFile:
				r, err = proc.newResource(rfi, p)
				if err != nil {
					return nil, err
				}
			default:
				panic(fmt.Sprintf("invalid classifier: %q", classifier))
			}

			resources[i] = r

		}

		p.addResources(resources...)
	}

	return p, nil
}

func (proc *pagesProcessor) newResource(fim hugofs.FileMetaInfo, owner *pageState) (resource.Resource, error) {

	// TODO(bep) consolidate with multihost logic + clean up
	outputFormats := owner.m.outputFormats()
	seen := make(map[string]bool)
	var targetBasePaths []string
	// Make sure bundled resources are published to all of the ouptput formats'
	// sub paths.
	for _, f := range outputFormats {
		p := f.Path
		if seen[p] {
			continue
		}
		seen[p] = true
		targetBasePaths = append(targetBasePaths, p)

	}

	meta := fim.Meta()
	r := func() (hugio.ReadSeekCloser, error) {
		return meta.Open()
	}

	return owner.s.ResourceSpec.New(
		resources.ResourceSourceDescriptor{
			TargetPaths:        owner.getTargetPaths,
			OpenReadSeekCloser: r,
			RelTargetFilename:  meta.Path(),
			TargetBasePaths:    targetBasePaths,
		})

}

func (proc *pagesProcessor) newPageFromFi(fim hugofs.FileMetaInfo, owner *pageState) (*pageState, error) {
	fi, err := newFileInfo2(proc.sp, fim)
	if err != nil {
		return nil, err
	}

	var s *Site
	meta := fim.Meta()

	if owner != nil {
		s = owner.s
	} else {
		lang := meta.Lang()
		s = proc.getSite(lang)
	}

	r := func() (hugio.ReadSeekCloser, error) {
		return meta.Open()
	}

	return newPageWithContent(fi, s, owner != nil, r)
}

func (proc *pagesProcessor) getSite(lang string) *Site {
	if lang == "" {
		return proc.h.Sites[0]
	}

	for _, s := range proc.h.Sites {
		if lang == s.Lang() {
			return s
		}
	}
	return proc.h.Sites[0]
}

func (proc *pagesProcessor) startProcessor(ctx context.Context) (*errgroup.Group, context.Context) {
	proc.pagesChan = make(chan *pageState, 4)
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		for p := range proc.pagesChan {
			s := p.s
			p.forceRender = proc.partialBuild

			if p.forceRender {
				s.replacePage(p)
			} else {
				s.addPage(p)
			}
		}
		return nil
	})

	return g, ctx
}
