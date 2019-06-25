// Copyright 2018 The Hugo Authors. All rights reserved.
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

// Package filesystems provides the fine grained file systems used by Hugo. These
// are typically virtual filesystems that are composites of project and theme content.
package filesystems

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cast"

	"github.com/pkg/errors"

	"github.com/gohugoio/hugo/modules"

	"github.com/gohugoio/hugo/config"

	"github.com/gohugoio/hugo/hugofs"

	"fmt"

	"github.com/gohugoio/hugo/hugolib/paths"
	"github.com/gohugoio/hugo/langs"
	"github.com/spf13/afero"
)

var filePathSeparator = string(filepath.Separator)

// BaseFs contains the core base filesystems used by Hugo. The name "base" is used
// to underline that even if they can be composites, they all have a base path set to a specific
// resource folder, e.g "/my-project/content". So, no absolute filenames needed.
type BaseFs struct {

	// SourceFilesystems contains the different source file systems.
	*SourceFilesystems

	// The filesystem used to publish the rendered site.
	// This usually maps to /my-project/public.
	PublishFs afero.Fs

	theBigFs *filesystemsCollector

	// TODO(bep) improve the "theme interaction"
	AbsThemeDirs []string
}

func (fs *BaseFs) WatchDirs() []hugofs.FileMetaInfo {
	var dirs []hugofs.FileMetaInfo
	for _, dirSet := range [][]hugofs.FileMetaInfo{
		fs.I18n.Dirs,
		fs.Data.Dirs,
		fs.Content.Dirs,
		fs.Assets.Dirs,
	} {
		for _, dir := range dirSet {
			if dir.Meta().Watch() {
				dirs = append(dirs, dir)
			}
		}
	}

	return dirs
}

// RelContentDir tries to create a path relative to the content root from
// the given filename. The return value is the path and language code.
func (b *BaseFs) RelContentDir(filename string) string {
	for _, dir := range b.SourceFilesystems.Content.Dirs {
		dirname := dir.Meta().Filename()
		if strings.HasPrefix(filename, dirname) {
			rel := strings.TrimPrefix(filename, dirname)
			return strings.TrimPrefix(rel, filePathSeparator)
		}
	}
	// Either not a content dir or already relative.
	return filename
}

// SourceFilesystems contains the different source file systems. These can be
// composite file systems (theme and project etc.), and they have all root
// set to the source type the provides: data, i18n, static, layouts.
type SourceFilesystems struct {
	Content    *SourceFilesystem
	Data       *SourceFilesystem
	I18n       *SourceFilesystem
	Layouts    *SourceFilesystem
	Archetypes *SourceFilesystem
	Assets     *SourceFilesystem
	Resources  *SourceFilesystem

	// Writable filesystem to the project's resources directory.
	ResourcesCache afero.Fs

	// This is a unified read-only view of the project's and themes' workdir.
	Work *SourceFilesystem

	// When in multihost we have one static filesystem per language. The sync
	// static files is currently done outside of the Hugo build (where there is
	// a concept of a site per language).
	// When in non-multihost mode there will be one entry in this map with a blank key.
	Static map[string]*SourceFilesystem
}

// A SourceFilesystem holds the filesystem for a given source type in Hugo (data,
// i18n, layouts, static) and additional metadata to be able to use that filesystem
// in server mode.
// TODO(bep) mod clean up
type SourceFilesystem struct {
	// This is a virtual composite filesystem. It expects path relative to a context.
	Fs afero.Fs

	// This filesystem as separate root directories, starting from project and down
	// to the themes/modules.
	Dirs []hugofs.FileMetaInfo

	// This is the base source filesystem. In real Hugo, this will be the OS filesystem.
	// Use this if you need to resolve items in Dirnames below.
	SourceFs afero.Fs

	// Dirnames is absolute filenames to the directories in this filesystem.
	Dirnames []string

	// When syncing a source folder to the target (e.g. /public), this may
	// be set to publish into a subfolder. This is used for static syncing
	// in multihost mode.
	PublishFolder string
}

// ContentStaticAssetFs will create a new composite filesystem from the content,
// static, and asset filesystems. The site language is needed to pick the correct static filesystem.
// The order is content, static and then assets.
// TODO(bep) check usage
func (s SourceFilesystems) ContentStaticAssetFs(lang string) afero.Fs {
	staticFs := s.StaticFs(lang)

	base := afero.NewCopyOnWriteFs(s.Assets.Fs, staticFs)
	return afero.NewCopyOnWriteFs(base, s.Content.Fs)

}

// StaticFs returns the static filesystem for the given language.
// This can be a composite filesystem.
func (s SourceFilesystems) StaticFs(lang string) afero.Fs {
	var staticFs afero.Fs = hugofs.NoOpFs

	if fs, ok := s.Static[lang]; ok {
		staticFs = fs.Fs
	} else if fs, ok := s.Static[""]; ok {
		staticFs = fs.Fs
	}

	return staticFs
}

// StatResource looks for a resource in these filesystems in order: static, assets and finally content.
// If found in any of them, it returns FileInfo and the relevant filesystem.
// Any non os.IsNotExist error will be returned.
// An os.IsNotExist error wil be returned only if all filesystems return such an error.
// Note that if we only wanted to find the file, we could create a composite Afero fs,
// but we also need to know which filesystem root it lives in.
func (s SourceFilesystems) StatResource(lang, filename string) (fi os.FileInfo, fs afero.Fs, err error) {
	for _, fsToCheck := range []afero.Fs{s.StaticFs(lang), s.Assets.Fs, s.Content.Fs} {
		fs = fsToCheck
		fi, err = fs.Stat(filename)
		if err == nil || !os.IsNotExist(err) {
			return
		}
	}
	// Not found.
	return
}

// IsStatic returns true if the given filename is a member of one of the static
// filesystems.
func (s SourceFilesystems) IsStatic(filename string) bool {
	for _, staticFs := range s.Static {
		if staticFs.Contains(filename) {
			return true
		}
	}
	return false
}

// IsContent returns true if the given filename is a member of the content filesystem.
func (s SourceFilesystems) IsContent(filename string) bool {
	return s.Content.Contains(filename)
}

// IsLayout returns true if the given filename is a member of the layouts filesystem.
func (s SourceFilesystems) IsLayout(filename string) bool {
	return s.Layouts.Contains(filename)
}

// IsData returns true if the given filename is a member of the data filesystem.
func (s SourceFilesystems) IsData(filename string) bool {
	return s.Data.Contains(filename)
}

// IsAsset returns true if the given filename is a member of the asset filesystem.
func (s SourceFilesystems) IsAsset(filename string) bool {
	return s.Assets.Contains(filename)
}

// IsI18n returns true if the given filename is a member of the i18n filesystem.
func (s SourceFilesystems) IsI18n(filename string) bool {
	return s.I18n.Contains(filename)
}

// MakeStaticPathRelative makes an absolute static filename into a relative one.
// It will return an empty string if the filename is not a member of a static filesystem.
func (s SourceFilesystems) MakeStaticPathRelative(filename string) string {
	for _, staticFs := range s.Static {
		rel := staticFs.MakePathRelative(filename)
		if rel != "" {
			return rel
		}
	}
	return ""
}

// MakePathRelative creates a relative path from the given filename.
// It will return an empty string if the filename is not a member of this filesystem.
func (d *SourceFilesystem) MakePathRelative(filename string) string {
	for _, currentPath := range d.Dirnames {
		if strings.HasPrefix(filename, currentPath) {
			return strings.TrimPrefix(filename, currentPath)
		}
	}
	return ""
}

func (d *SourceFilesystem) RealFilename(rel string) string {
	fi, err := d.Fs.Stat(rel)
	if err != nil {
		return rel
	}
	if realfi, ok := fi.(hugofs.FileMetaInfo); ok {
		return realfi.Meta().Filename()
	}

	return rel
}

// Contains returns whether the given filename is a member of the current filesystem.
func (d *SourceFilesystem) Contains(filename string) bool {
	for _, dir := range d.Dirs {
		if strings.HasPrefix(filename, dir.Meta().Filename()) {
			return true
		}
	}
	return false
}

// RealDirs gets a list of absolute paths to directories starting from the given
// path.
func (d *SourceFilesystem) RealDirs(from string) []string {
	var dirnames []string
	for _, dir := range d.Dirs {
		dirname := filepath.Join(dir.Meta().Filename(), from)
		if _, err := d.SourceFs.Stat(dirname); err == nil {
			dirnames = append(dirnames, dirname)
		}
	}
	return dirnames
}

// WithBaseFs allows reuse of some potentially expensive to create parts that remain
// the same across sites/languages.
func WithBaseFs(b *BaseFs) func(*BaseFs) error {
	return func(bb *BaseFs) error {
		bb.theBigFs = b.theBigFs
		bb.AbsThemeDirs = b.AbsThemeDirs
		return nil
	}
}

func newRealBase(base afero.Fs) afero.Fs {
	return hugofs.DecorateBasePathFs(base.(*afero.BasePathFs))

}

// NewBase builds the filesystems used by Hugo given the paths and options provided.NewBase
func NewBase(p *paths.Paths, options ...func(*BaseFs) error) (*BaseFs, error) {
	fs := p.Fs

	publishFs := afero.NewBasePathFs(fs.Destination, p.AbsPublishDir)

	b := &BaseFs{
		PublishFs: publishFs,
	}

	for _, opt := range options {
		if err := opt(b); err != nil {
			return nil, err
		}
	}

	builder := newSourceFilesystemsBuilder(p, b)
	sourceFilesystems, err := builder.Build()
	if err != nil {
		return nil, errors.Wrap(err, "build filesystems")
	}

	b.SourceFilesystems = sourceFilesystems
	b.theBigFs = builder.theBigFs
	// TODO(bep) mod	b.AbsThemeDirs = builder.absThemeDirs

	return b, nil
}

type sourceFilesystemsBuilder struct {
	p        *paths.Paths
	sourceFs afero.Fs
	result   *SourceFilesystems
	theBigFs *filesystemsCollector
}

func newSourceFilesystemsBuilder(p *paths.Paths, b *BaseFs) *sourceFilesystemsBuilder {
	sourceFs := hugofs.NewBaseFileDecorator(p.Fs.Source)
	return &sourceFilesystemsBuilder{p: p, sourceFs: sourceFs, theBigFs: b.theBigFs, result: &SourceFilesystems{}}
}

func (b *sourceFilesystemsBuilder) newSourceFilesystem(fs afero.Fs, dirs []hugofs.FileMetaInfo) *SourceFilesystem {
	return &SourceFilesystem{
		Fs:       fs,
		Dirs:     dirs,
		SourceFs: b.sourceFs,
	}
}
func (b *sourceFilesystemsBuilder) Build() (*SourceFilesystems, error) {

	if b.theBigFs == nil {
		projectMounts, err := b.createProjectMounts()
		if err != nil {
			return nil, err
		}

		theBigFs, err := b.createMainOverlayFs(projectMounts, b.p)
		if err != nil {
			return nil, errors.Wrap(err, "create main fs")
		}
		if theBigFs.overlay == nil {
			panic("createThemesFs returned nil")
		}
		b.theBigFs = theBigFs
	}

	createView := func(id string, dirs []hugofs.FileMetaInfo) *SourceFilesystem {
		if b.theBigFs == nil {
			panic("no fs")
		}
		if b.theBigFs.overlay == nil {
			panic("no overlay")
		}

		return b.newSourceFilesystem(afero.NewBasePathFs(b.theBigFs.overlay, id), dirs)

	}

	b.theBigFs.finalizeDirs()

	b.result.Archetypes = createView("archetypes", nil)
	b.result.Layouts = createView("layouts", b.theBigFs.layoutsDirs)
	b.result.Assets = createView("assets", b.theBigFs.assetsDirs)
	b.result.Resources = createView("resources", nil)
	b.result.ResourcesCache = afero.NewBasePathFs(b.sourceFs, b.p.AbsResourcesDir)

	// Data, i18n and content cannot use the overlay fs
	dataFs, err := hugofs.NewSliceFs(b.theBigFs.dataDirs...)
	if err != nil {
		return nil, err
	}

	b.result.Data = b.newSourceFilesystem(dataFs, b.theBigFs.dataDirs)

	i18nFs, err := hugofs.NewSliceFs(b.theBigFs.i18nDirs...)
	if err != nil {
		return nil, err
	}
	b.result.I18n = b.newSourceFilesystem(i18nFs, b.theBigFs.i18nDirs)

	// TODO(bep) mod contentFilesystems probably needs to be reversed
	contentDirs := b.theBigFs.contentDirs

	contentFs, err := hugofs.NewLanguageFs2(b.p.Languages.AsSet(), afero.NewBasePathFs(b.theBigFs.overlay, "content"))
	if err != nil {
		return nil, errors.Wrap(err, "create content filesystem")
	}
	b.result.Content = b.newSourceFilesystem(contentFs, contentDirs)

	return b.result, err

}

func (b *sourceFilesystemsBuilder) createFs(
	mkdir bool,
	readOnly bool,
	dirKey, themeFolder string) (*SourceFilesystem, error) {
	s := &SourceFilesystem{
		SourceFs: b.sourceFs,
	}

	if themeFolder == "" {
		themeFolder = filePathSeparator
	}

	var dir string
	if dirKey != "" {
		dir = b.p.Cfg.GetString(dirKey)
		if dir == "" {
			return s, fmt.Errorf("config %q not set", dirKey)
		}
	}

	var fs afero.Fs

	absDir := b.p.AbsPathify(dir)
	existsInSource := b.existsInSource(absDir)
	if !existsInSource && mkdir {
		// We really need this directory. Make it.
		if err := b.sourceFs.MkdirAll(absDir, 0777); err == nil {
			existsInSource = true
		}
	}
	if existsInSource {
		fs = newRealBase(afero.NewBasePathFs(b.sourceFs, absDir))
		s.Dirnames = []string{absDir}
	}

	if b.hasModules() {
		if !strings.HasPrefix(themeFolder, filePathSeparator) {
			themeFolder = filePathSeparator + themeFolder
		}
		bfs := afero.NewBasePathFs(b.theBigFs.overlay, themeFolder)
		themeFolderFs := newRealBase(bfs)

		if fs == nil {
			fs = themeFolderFs
		} else {
			fs = afero.NewCopyOnWriteFs(themeFolderFs, fs)
		}

		// TODO(bep) mod
		/*
			for _, absThemeDir := range b.absThemeDirs {
				absThemeFolderDir := filepath.Join(absThemeDir, themeFolder)
				if b.existsInSource(absThemeFolderDir) {
					s.Dirnames = append(s.Dirnames, absThemeFolderDir)
				}
			}
		*/
	}

	if fs == nil {
		s.Fs = hugofs.NoOpFs
	} else if readOnly {
		s.Fs = afero.NewReadOnlyFs(fs)
	} else {
		s.Fs = fs
	}

	return s, nil
}

func (b *sourceFilesystemsBuilder) hasModules() bool {
	return len(b.p.AllModules) > 0
}

func (b *sourceFilesystemsBuilder) existsInSource(abspath string) bool {
	exists, _ := afero.Exists(b.sourceFs, abspath)
	return exists
}

func (b *sourceFilesystemsBuilder) createStaticFs() error {
	isMultihost := b.p.Cfg.GetBool("multihost")
	ms := make(map[string]*SourceFilesystem)
	b.result.Static = ms

	if isMultihost {
		for _, l := range b.p.Languages {
			s := &SourceFilesystem{
				SourceFs:      b.sourceFs,
				PublishFolder: l.Lang}
			staticDirs := removeDuplicatesKeepRight(getStaticDirs(l))
			if len(staticDirs) == 0 {
				continue
			}

			for _, dir := range staticDirs {
				absDir := b.p.AbsPathify(dir)
				if !b.existsInSource(absDir) {
					continue
				}

				s.Dirnames = append(s.Dirnames, absDir)
			}

			fs, err := createOverlayFs(b.sourceFs, s.Dirnames)
			if err != nil {
				return err
			}

			if b.hasModules() {
				themeFolder := "static"
				fs = afero.NewCopyOnWriteFs(newRealBase(afero.NewBasePathFs(b.theBigFs.overlay, themeFolder)), fs)
				// TODO(bep) mod
				/*for _, absThemeDir := range b.absThemeDirs {
					s.Dirnames = append(s.Dirnames, filepath.Join(absThemeDir, themeFolder))
				}*/
			}

			s.Fs = fs
			ms[l.Lang] = s

		}

		return nil
	}

	s := &SourceFilesystem{
		SourceFs: b.sourceFs,
	}

	var staticDirs []string

	for _, l := range b.p.Languages {
		staticDirs = append(staticDirs, getStaticDirs(l)...)
	}

	staticDirs = removeDuplicatesKeepRight(staticDirs)
	if len(staticDirs) == 0 {
		return nil
	}

	for _, dir := range staticDirs {
		absDir := b.p.AbsPathify(dir)
		if !b.existsInSource(absDir) {
			continue
		}
		s.Dirnames = append(s.Dirnames, absDir)
	}

	fs, err := createOverlayFs(b.sourceFs, s.Dirnames)
	if err != nil {
		return err
	}

	if b.hasModules() {
		themeFolder := "static"
		fs = afero.NewCopyOnWriteFs(newRealBase(afero.NewBasePathFs(b.theBigFs.overlay, themeFolder)), fs)
		// TODO(bep) mod
		/*for _, absThemeDir := range b.absThemeDirs {
			s.Dirnames = append(s.Dirnames, filepath.Join(absThemeDir, themeFolder))
		}*/
	}

	s.Fs = fs
	ms[""] = s

	return nil
}

func getStaticDirs(cfg config.Provider) []string {
	var staticDirs []string
	for i := -1; i <= 10; i++ {
		staticDirs = append(staticDirs, getStringOrStringSlice(cfg, "staticDir", i)...)
	}
	return staticDirs
}

func getStringOrStringSlice(cfg config.Provider, key string, id int) []string {

	if id >= 0 {
		key = fmt.Sprintf("%s%d", key, id)
	}

	return config.GetStringSlicePreserveString(cfg, key)

}

func (b *sourceFilesystemsBuilder) createProjectMounts() (mountsDescriptor, error) {

	contentMounts, err := b.createContentMounts()
	if err != nil {
		return mountsDescriptor{}, err
	}

	staticMounts, err := b.createStaticMounts()
	if err != nil {
		return mountsDescriptor{}, err
	}

	getDir := func(id string) string {
		return b.p.Cfg.GetString(id)
	}

	i18nDir := getDir("i18nDir")
	layoutsDir := getDir("layoutDir")
	dataDir := getDir("dataDir")
	archetypeDir := getDir("archetypeDir")

	assetsDir := getDir("assetDir")
	resourcesDir := getDir("resourceDir")

	mounts := []modules.Mount{
		modules.Mount{Source: i18nDir, Target: "i18n"},
		modules.Mount{Source: layoutsDir, Target: "layouts"},
		modules.Mount{Source: dataDir, Target: "data"},
		modules.Mount{Source: archetypeDir, Target: "archetypes"},

		modules.Mount{Source: assetsDir, Target: "assets"},
		modules.Mount{Source: resourcesDir, Target: "resources"},
	}

	mounts = append(contentMounts, mounts...)
	mounts = append(staticMounts, mounts...)

	dir := b.p.WorkingDir
	if dir == "." {
		dir = ""
	}

	return mountsDescriptor{
		mounts:        mounts,
		dir:           dir,
		isMainProject: true,
		watch:         true,
	}, nil

	// TODO(bep) mod static, archetype etc + check
}

func (b *sourceFilesystemsBuilder) createStaticMounts() ([]modules.Mount, error) {
	isMultihost := b.p.Cfg.GetBool("multihost")
	languages := b.p.Languages
	var mounts []modules.Mount

	if isMultihost {
		for _, language := range languages {
			dirs := removeDuplicatesKeepRight(getStaticDirs(language))
			for _, dir := range dirs {
				mounts = append(mounts, modules.Mount{
					Source: b.p.AbsPathify(dir),
					Target: "static",
					Lang:   language.Lang,
				})
			}
		}
	} else {
		// Just create one big filesystem.
		var dirs []string
		for _, language := range languages {
			dirs = append(dirs, removeDuplicatesKeepRight(getStaticDirs(language))...)
		}

		for _, dir := range dirs {
			mounts = append(mounts, modules.Mount{
				Source: b.p.AbsPathify(dir),
				Target: "static",
			})
		}
	}

	return mounts, nil
}
func (b *sourceFilesystemsBuilder) createContentMounts() ([]modules.Mount, error) {

	defaultContentLanguage := b.p.DefaultContentLanguage
	languages := b.p.Languages

	var contentLanguages langs.Languages
	var contentDirSeen = make(map[string]bool)
	languageSet := make(map[string]bool)

	// The default content language needs to be first.
	for _, language := range languages {
		if language.Lang == defaultContentLanguage {
			contentLanguages = append(contentLanguages, language)
			contentDirSeen[language.ContentDir] = true
		}
		languageSet[language.Lang] = true
	}

	for _, language := range languages {
		if contentDirSeen[language.ContentDir] {
			continue
		}
		if language.ContentDir == "" {
			language.ContentDir = defaultContentLanguage
		}
		contentDirSeen[language.ContentDir] = true
		contentLanguages = append(contentLanguages, language)

	}

	mounts := make([]modules.Mount, len(contentLanguages))

	for i, language := range contentLanguages {
		mounts[i] = modules.Mount{
			Source: b.p.AbsPathify(language.ContentDir),
			Target: "content",
			Lang:   language.Lang,
		}
	}

	return mounts, nil

}

func (b *sourceFilesystemsBuilder) createMainOverlayFs(projectMounts mountsDescriptor, p *paths.Paths) (*filesystemsCollector, error) {

	mods := p.AllModules

	modsReversed := make([]mountsDescriptor, len(mods)+1)

	modsReversed[len(modsReversed)-1] = projectMounts

	// The theme components are ordered from left to right.
	// We need to revert it to get the
	// overlay logic below working as expected, with the project on top (first).
	for i := len(mods) - 1; i >= 0; i-- {
		mod := mods[i]
		dir := mod.Dir()

		modsReversed[i] = mountsDescriptor{
			mounts: mod.Mounts(),
			dir:    dir,
			watch:  !mod.IsGoMod(), // TODO(bep) mod consider Replace
		}
	}

	collector := &filesystemsCollector{
		sourceProject: b.sourceFs,
		sourceModules: b.sourceFs, // TODO(bep) mod: add a no symlinc fs
	}

	err := b.createOverlayFs(collector, modsReversed)

	// TODO(bep) mod remove New* if not used.
	//collector.overlay = hugofs.NewCompositeDirDecorator(collector.overlay)

	return collector, err

}

func (b *sourceFilesystemsBuilder) isContentMount(mnt modules.Mount) bool {
	return strings.HasPrefix(mnt.Target, "content")
}

func (b *sourceFilesystemsBuilder) createModFs(
	collector *filesystemsCollector,
	md mountsDescriptor) error {

	var fromTo []hugofs.RootMapping

	absPathify := func(path string) string {
		return paths.AbsPathify(md.dir, path)
	}

	for _, mount := range md.mounts {
		mountWeight := 1
		if md.isMainProject {
			mountWeight++
		}
		rm := hugofs.RootMapping{
			From: mount.Target,
			To:   absPathify(mount.Source),
			Meta: hugofs.FileMeta{
				"watch":       md.watch,
				"mountWeight": mountWeight,
			},
		}

		if b.isContentMount(mount) {
			lang := mount.Lang
			if lang == "" {
				lang = b.p.DefaultContentLanguage
			}
			rm.Meta["lang"] = lang
		}
		fromTo = append(fromTo, rm)
	}

	modBase := collector.sourceProject
	if !md.isMainProject {
		modBase = collector.sourceModules
	}

	rmfs, err := hugofs.NewRootMappingFs(modBase, fromTo...)
	if err != nil {
		return err
	}

	// These need special merge.
	contentDirs, err := rmfs.Dirs("content")
	if err != nil {
		return err
	}

	dataDirs, err := rmfs.Dirs("data")
	if err != nil {
		return err
	}

	i18nDirs, err := rmfs.Dirs("i18n")
	if err != nil {
		return err
	}

	// We need these to get the watch dir list
	assetsDirs, err := rmfs.Dirs("assets")
	if err != nil {
		return err
	}
	layoutsDirs, err := rmfs.Dirs("layouts")
	if err != nil {
		return err
	}

	collector.contentDirs = append(collector.contentDirs, contentDirs...)
	collector.dataDirs = append(collector.dataDirs, dataDirs...)
	collector.i18nDirs = append(collector.i18nDirs, i18nDirs...)
	collector.layoutsDirs = append(collector.layoutsDirs, layoutsDirs...)
	collector.assetsDirs = append(collector.assetsDirs, assetsDirs...)

	if collector.overlay == nil {
		collector.overlay = rmfs
	} else {
		collector.overlay = hugofs.NewLanguageCompositeFs(collector.overlay, rmfs)
	}

	return nil

}

func printFs(fs afero.Fs, path string, w io.Writer) {
	if fs == nil {
		return
	}
	afero.Walk(fs, path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		var filename string
		if fim, ok := info.(hugofs.FileMetaInfo); ok {
			filename = fim.Meta().Filename()
			path = fim.Meta().Path()
		}
		fmt.Fprintf(w, "    %q %q\n", path, filename)
		return nil
	})
}

const contentBase = "content"

type filesystemsCollector struct {
	sourceProject afero.Fs // Source for project folders
	sourceModules afero.Fs // Source for modules/themes

	overlay afero.Fs

	watchDirs []string // TODO(bep) mod

	// These need special merge.
	contentDirs []hugofs.FileMetaInfo
	i18nDirs    []hugofs.FileMetaInfo
	dataDirs    []hugofs.FileMetaInfo

	// Collect these to get a complete watch dirs list
	// TODO(bep) mod static
	layoutsDirs []hugofs.FileMetaInfo
	assetsDirs  []hugofs.FileMetaInfo
}

func (c *filesystemsCollector) reverseFileMetaInfos(fis []hugofs.FileMetaInfo) []hugofs.FileMetaInfo {
	for i := len(fis)/2 - 1; i >= 0; i-- {
		opp := len(fis) - 1 - i
		fis[i], fis[opp] = fis[opp], fis[i]
	}

	return fis
}

func (c *filesystemsCollector) finalizeDirs() {
	// Pulls project dirs to the top
	c.orderByMountWeight(c.dataDirs)
	c.orderByMountWeight(c.i18nDirs)
	c.orderByMountWeight(c.contentDirs)
	c.orderByMountWeight(c.assetsDirs)
	c.orderByMountWeight(c.layoutsDirs)
}

func (c *filesystemsCollector) orderByMountWeight(fis []hugofs.FileMetaInfo) {
	// Pull project dirs in front
	sort.Slice(fis, func(i, j int) bool {
		ri, rj := fis[i], fis[j]

		wi := cast.ToInt(ri.Meta()["mountWeight"])
		wj := cast.ToInt(rj.Meta()["mountWeight"])

		return wi > wj
	})
}

type mountsDescriptor struct {
	mounts        []modules.Mount
	dir           string
	watch         bool // whether this is a candidate for watching in server mode.
	isMainProject bool
}

func (b *sourceFilesystemsBuilder) createOverlayFs(collector *filesystemsCollector, mounts []mountsDescriptor) error {
	if len(mounts) == 0 {
		return nil
	}

	err := b.createModFs(collector, mounts[0])
	if err != nil {
		return err
	}

	if len(mounts) == 1 {
		return nil
	}

	return b.createOverlayFs(collector, mounts[1:])
}

// TODO(bep) mod remove
func createOverlayFs(source afero.Fs, absPaths []string) (afero.Fs, error) {
	if len(absPaths) == 0 {
		return hugofs.NoOpFs, nil
	}

	if len(absPaths) == 1 {
		return afero.NewReadOnlyFs(newRealBase(afero.NewBasePathFs(source, absPaths[0]))), nil
	}

	base := afero.NewReadOnlyFs(newRealBase(afero.NewBasePathFs(source, absPaths[0])))
	overlay, err := createOverlayFs(source, absPaths[1:])
	if err != nil {
		return nil, err
	}

	return afero.NewCopyOnWriteFs(base, overlay), nil
}

func removeDuplicatesKeepRight(in []string) []string {
	seen := make(map[string]bool)
	var out []string
	for i := len(in) - 1; i >= 0; i-- {
		v := in[i]
		if seen[v] {
			continue
		}
		out = append([]string{v}, out...)
		seen[v] = true
	}

	return out
}
