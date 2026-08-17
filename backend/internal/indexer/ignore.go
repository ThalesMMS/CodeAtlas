package indexer

import sharedignore "github.com/ThalesMMS/CodeAtlas/internal/ignore"

var defaultIgnoredDirectories = sharedignore.DefaultIgnoredDirectories

type ignoreMatcher struct {
	inner sharedignore.Matcher
}

func loadIgnoreMatcher(root string) ignoreMatcher {
	return ignoreMatcher{inner: sharedignore.LoadMatcher(root)}
}

func (m ignoreMatcher) ignored(relative string, isDir bool) bool {
	return m.inner.Ignored(relative, isDir)
}
