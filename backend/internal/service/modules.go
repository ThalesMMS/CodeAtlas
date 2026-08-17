package service

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// ModuleDetectionVersion is the versioned heuristic set; bump on any change that
// can reassign symbols to different modules (it affects stable slugs/IDs).
const ModuleDetectionVersion = "module-detect.v1"

// ignoredModuleDir lists directory segments that never form a module: vendored,
// generated or VCS/build noise. Detection never reads or evaluates repo code.
var ignoredModuleDir = map[string]struct{}{
	"vendor": {}, "node_modules": {}, ".git": {}, "dist": {}, "build": {},
	"generated": {}, ".next": {}, "out": {}, "coverage": {}, "testdata": {},
}

// detectModules groups indexed symbols into deterministic modules. It uses
// directory/package structure plus language signals (cmd/internal/pkg for Go,
// src/packages for JS/TS) without executing or importing any repository code.
// Results are sorted by slug for stable output.
func detectModules(symbols []domain.Symbol) []domain.WikiModule {
	type accum struct {
		paths    map[string]struct{}
		files    int
		symbols  int
		language string
	}
	groups := make(map[string]*accum)
	for _, symbol := range symbols {
		dir := moduleKey(symbol.Path)
		if dir == "" {
			continue
		}
		group, ok := groups[dir]
		if !ok {
			group = &accum{paths: make(map[string]struct{})}
			groups[dir] = group
		}
		group.paths[symbol.Path] = struct{}{}
		if symbol.Kind == "file" {
			group.files++
		} else {
			group.symbols++
		}
		if symbol.Language != "" && group.language == "" {
			group.language = symbol.Language
		}
	}

	modules := make([]domain.WikiModule, 0, len(groups))
	for name, group := range groups {
		paths := make([]string, 0, len(group.paths))
		for path := range group.paths {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		modules = append(modules, domain.WikiModule{
			Slug:           "module-" + slugify(name),
			Name:           name,
			Language:       group.language,
			Paths:          paths,
			BoundaryReason: boundaryReason(name),
			Entrypoint:     isEntrypointModule(name),
			FileCount:      group.files,
			SymbolCount:    group.symbols,
		})
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Slug < modules[j].Slug })
	return modules
}

// moduleKey maps a file path to its module name (top package/dir), returning ""
// for ignored paths. cmd/internal/pkg keep one extra segment so e.g. `cmd/api`
// and `cmd/worker` are distinct modules.
func moduleKey(path string) string {
	path = filepath.ToSlash(path)
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	for _, part := range parts[:len(parts)-1] {
		if _, ignored := ignoredModuleDir[part]; ignored {
			return ""
		}
	}
	if len(parts) <= 1 {
		return "raiz"
	}
	head := parts[0]
	// Go layout signals: the segment under cmd/internal/pkg names the module.
	if (head == "cmd" || head == "internal" || head == "pkg") && len(parts) >= 3 {
		return head + "/" + parts[1]
	}
	// JS/TS monorepo signals: packages/<name> and apps/<name> are separate modules.
	if (head == "packages" || head == "apps") && len(parts) >= 3 {
		return head + "/" + parts[1]
	}
	if head == "src" && len(parts) >= 3 {
		return "src/" + parts[1]
	}
	return head
}

func boundaryReason(name string) string {
	switch {
	case strings.HasPrefix(name, "cmd/"):
		return "Go command (entrypoint binary)"
	case strings.HasPrefix(name, "internal/"):
		return "Go internal package (not importable externally)"
	case strings.HasPrefix(name, "pkg/"):
		return "Go reusable package"
	case strings.HasPrefix(name, "packages/"), strings.HasPrefix(name, "apps/"):
		return "JS/TS workspace package"
	case strings.HasPrefix(name, "src/"):
		return "source subtree"
	case name == "raiz":
		return "repository root files"
	default:
		return "top-level package directory"
	}
}

func isEntrypointModule(name string) bool {
	return strings.HasPrefix(name, "cmd/") || name == "cmd"
}

// modulesHaveTests reports whether any detected module contains test symbols, so
// the manifest only declares a testing page when there is evidence for one.
func modulesHaveTests(symbols []domain.Symbol) bool {
	for _, symbol := range symbols {
		if symbol.Kind == "test" || strings.Contains(strings.ToLower(symbol.Path), "_test.") || strings.Contains(strings.ToLower(symbol.Path), ".test.") {
			return true
		}
	}
	return false
}
