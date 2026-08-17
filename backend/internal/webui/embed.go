package webui

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
)

//go:embed dist/*
var assets embed.FS

const (
	manifestName             = "codeatlas-manifest.json"
	cspNoncePlaceholder      = "__CODEATLAS_CSP_NONCE__"
	settingsTokenPlaceholder = "__CODEATLAS_SETTINGS_TOKEN__"
)

var (
	htmlAssetPattern   = regexp.MustCompile(`\b(?:src|href)="([^"]+)"`)
	hashedAssetPattern = regexp.MustCompile(`^assets/.+-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$`)
)

type BuildManifest struct {
	BuildVersion  string         `json:"buildVersion"`
	Editor        string         `json:"editor"`
	EditorVersion string         `json:"editorVersion"`
	Assets        ManifestAssets `json:"assets"`
}

type ManifestAssets struct {
	Entrypoints []string `json:"entrypoints"`
	Styles      []string `json:"styles"`
	Workers     []string `json:"workers"`
	Other       []string `json:"other"`
}

// Assets returns the embedded frontend asset tree rooted at dist. It is the
// single source of truth for the built UI and lets other packages (e.g. the
// readiness probes) inspect the embedded files without starting an HTTP server.
func Assets() fs.FS {
	subtree, err := fs.Sub(assets, "dist")
	if err != nil {
		// The embed directive guarantees dist exists at build time.
		panic(err)
	}
	return subtree
}

func Handler() http.Handler {
	return HandlerWithAssets(Assets())
}

// HandlerWithCSP binds each HTML response to a fresh style nonce. The callback
// keeps the security policy owned by the HTTP API while this package injects
// the same nonce into the otherwise immutable embedded index.
func HandlerWithCSP(policy func(string) string) http.Handler {
	return handlerWithAssets(Assets(), policy, "", false)
}

// HandlerWithCSPAndSettingsToken injects a process-local settings token only
// into uncached HTML responses. Static assets remain immutable and never carry
// the token.
func HandlerWithCSPAndSettingsToken(policy func(string) string, token string) http.Handler {
	return handlerWithAssets(Assets(), policy, token, true)
}

func HandlerWithAssets(subtree fs.FS) http.Handler {
	return handlerWithAssets(subtree, nil, "", false)
}

func handlerWithAssets(subtree fs.FS, cspPolicy func(string) string, settingsToken string, injectSettingsToken bool) http.Handler {
	validationErr := ValidateAssets(subtree)
	index, indexErr := fs.ReadFile(subtree, "index.html")
	if validationErr == nil && cspPolicy != nil && bytes.Count(index, []byte(cspNoncePlaceholder)) != 1 {
		validationErr = fmt.Errorf("index.html must contain exactly one CSP nonce placeholder")
	}
	settingsPlaceholderCount := bytes.Count(index, []byte(settingsTokenPlaceholder))
	if validationErr == nil && injectSettingsToken && settingsPlaceholderCount != 1 {
		validationErr = fmt.Errorf("index.html must contain exactly one settings token placeholder")
	}
	if !injectSettingsToken && settingsPlaceholderCount == 1 {
		index = bytes.Replace(index, []byte(settingsTokenPlaceholder), nil, 1)
	}
	indexETag := ""
	if indexErr == nil {
		digest := sha256.Sum256(index)
		indexETag = fmt.Sprintf(`"%x"`, digest[:16])
	}
	files := http.FileServer(http.FS(subtree))
	serveHTML := func(response http.ResponseWriter, request *http.Request) {
		nonce := ""
		if cspPolicy != nil {
			var err error
			nonce, err = newCSPNonce()
			if err != nil {
				http.Error(response, "frontend nonce unavailable", http.StatusInternalServerError)
				return
			}
			response.Header().Set("Content-Security-Policy", cspPolicy(nonce))
		}
		serveIndex(response, request, index, indexErr, indexETag, nonce, settingsToken, injectSettingsToken)
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if validationErr != nil {
			http.Error(response, "frontend assets invalid: "+validationErr.Error(), http.StatusServiceUnavailable)
			return
		}
		clean := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		if clean == "." || clean == "" || clean == "index.html" {
			serveHTML(response, request)
			return
		}
		if _, err := fs.Stat(subtree, clean); err == nil {
			setCacheHeaders(response, clean)
			serveAsset(response, request, subtree, files, clean)
			return
		}
		serveHTML(response, request)
	})
}

func Manifest(subtree fs.FS) (BuildManifest, error) {
	data, err := fs.ReadFile(subtree, manifestName)
	if err != nil {
		return BuildManifest{}, fmt.Errorf("read %s: %w", manifestName, err)
	}
	var manifest BuildManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return BuildManifest{}, fmt.Errorf("parse %s: %w", manifestName, err)
	}
	if manifest.BuildVersion == "" || manifest.Editor == "" || manifest.EditorVersion == "" {
		return BuildManifest{}, fmt.Errorf("%s missing buildVersion/editor/editorVersion", manifestName)
	}
	return manifest, nil
}

func ValidateAssets(subtree fs.FS) error {
	index, err := fs.ReadFile(subtree, "index.html")
	if err != nil {
		return fmt.Errorf("missing index.html: %w", err)
	}
	if len(index) == 0 {
		return fmt.Errorf("empty index.html")
	}
	manifest, err := Manifest(subtree)
	if err != nil {
		return err
	}
	for _, name := range append(htmlAssetReferences(string(index)), manifest.AssetList()...) {
		clean, err := cleanAssetPath(name)
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(subtree, clean)
		if err != nil {
			return fmt.Errorf("missing asset %s: %w", clean, err)
		}
		if len(data) == 0 {
			return fmt.Errorf("empty asset %s", clean)
		}
	}
	return nil
}

func serveIndex(response http.ResponseWriter, request *http.Request, data []byte, err error, etag, nonce, settingsToken string, injectSettingsToken bool) {
	if err != nil {
		http.Error(response, "frontend not built", http.StatusServiceUnavailable)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if nonce != "" || injectSettingsToken {
		response.Header().Set("Cache-Control", "no-store")
		if nonce != "" {
			data = bytes.Replace(data, []byte(cspNoncePlaceholder), []byte(nonce), 1)
		}
		if injectSettingsToken {
			data = bytes.Replace(data, []byte(settingsTokenPlaceholder), []byte(html.EscapeString(settingsToken)), 1)
		}
		_, _ = response.Write(data)
		return
	}
	response.Header().Set("Cache-Control", "public, max-age=60")
	response.Header().Set("ETag", etag)
	if request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = response.Write(data)
}

func newCSPNonce() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(raw[:]), nil
}

func serveAsset(response http.ResponseWriter, request *http.Request, subtree fs.FS, files http.Handler, clean string) {
	available := map[string]string{}
	for encoding, suffix := range map[string]string{"br": ".br", "gzip": ".gz"} {
		if _, err := fs.Stat(subtree, clean+suffix); err == nil {
			available[encoding] = clean + suffix
		}
	}
	if len(available) == 0 {
		files.ServeHTTP(response, request)
		return
	}
	response.Header().Set("Vary", "Accept-Encoding")
	encoding := preferredEncoding(request.Header.Get("Accept-Encoding"), available)
	encodedPath, ok := available[encoding]
	if !ok {
		files.ServeHTTP(response, request)
		return
	}
	response.Header().Set("Content-Encoding", encoding)
	if contentType := assetContentType(clean); contentType != "" {
		response.Header().Set("Content-Type", contentType)
	}
	clone := request.Clone(request.Context())
	clonedURL := *request.URL
	clonedURL.Path = "/" + encodedPath
	clonedURL.RawPath = ""
	clone.URL = &clonedURL
	files.ServeHTTP(response, clone)
}

func assetContentType(clean string) string {
	extension := strings.ToLower(path.Ext(clean))
	known := map[string]string{
		".css":   "text/css; charset=utf-8",
		".js":    "text/javascript; charset=utf-8",
		".map":   "application/json",
		".woff":  "font/woff",
		".woff2": "font/woff2",
	}
	if contentType := known[extension]; contentType != "" {
		return contentType
	}
	return mime.TypeByExtension(extension)
}

func preferredEncoding(header string, available map[string]string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	quality := map[string]float64{}
	wildcard := -1.0
	for _, raw := range strings.Split(header, ",") {
		parts := strings.Split(raw, ";")
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		q := 1.0
		for _, parameter := range parts[1:] {
			key, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if found && strings.EqualFold(strings.TrimSpace(key), "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					q = 0
				} else {
					q = parsed
				}
			}
		}
		if name == "*" {
			wildcard = q
		} else if name != "" {
			quality[name] = q
		}
	}
	qualityFor := func(name string) float64 {
		if q, ok := quality[name]; ok {
			return q
		}
		if wildcard >= 0 {
			return wildcard
		}
		return 0
	}
	best := ""
	bestQuality := 0.0
	for _, encoding := range []string{"br", "gzip"} {
		if _, ok := available[encoding]; !ok {
			continue
		}
		if q := qualityFor(encoding); q > bestQuality {
			best, bestQuality = encoding, q
		}
	}
	return best
}

func (m BuildManifest) AssetList() []string {
	out := make([]string, 0, len(m.Assets.Entrypoints)+len(m.Assets.Styles)+len(m.Assets.Workers)+len(m.Assets.Other))
	out = append(out, m.Assets.Entrypoints...)
	out = append(out, m.Assets.Styles...)
	out = append(out, m.Assets.Workers...)
	out = append(out, m.Assets.Other...)
	return out
}

func htmlAssetReferences(html string) []string {
	matches := htmlAssetPattern.FindAllStringSubmatch(html, -1)
	refs := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		clean, err := cleanAssetPath(match[1])
		if err != nil || !strings.HasPrefix(clean, "assets/") {
			continue
		}
		refs = append(refs, clean)
	}
	return refs
}

func cleanAssetPath(name string) (string, error) {
	raw := strings.TrimPrefix(name, "/")
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", fmt.Errorf("unsafe asset path %q", name)
		}
	}
	clean := strings.TrimPrefix(path.Clean("/"+raw), "/")
	if clean == "." || clean == "" {
		return "", fmt.Errorf("unsafe asset path %q", name)
	}
	return clean, nil
}

func setCacheHeaders(response http.ResponseWriter, clean string) {
	switch {
	case clean == manifestName || strings.HasPrefix(clean, ".vite/"):
		response.Header().Set("Cache-Control", "no-store")
	case hashedAssetPattern.MatchString(clean):
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
}
