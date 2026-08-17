package webui_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ThalesMMS/CodeAtlas/internal/webui"
)

func TestValidateAssetsAcceptsViteManifestAndReferencedAssets(t *testing.T) {
	t.Parallel()
	assets := viteAssets()

	if err := webui.ValidateAssets(assets); err != nil {
		t.Fatalf("ValidateAssets() error = %v", err)
	}

	manifest, err := webui.Manifest(assets)
	if err != nil {
		t.Fatalf("Manifest() error = %v", err)
	}
	if manifest.Editor != "monaco" || manifest.EditorVersion != "0.53.0" {
		t.Fatalf("manifest editor = %s@%s, want monaco@0.53.0", manifest.Editor, manifest.EditorVersion)
	}
}

func TestValidateAssetsRejectsMissingReferencedAsset(t *testing.T) {
	t.Parallel()
	assets := viteAssets()
	delete(assets, "assets/main-Abc12345.js")

	if err := webui.ValidateAssets(assets); err == nil {
		t.Fatal("ValidateAssets() error = nil, want missing asset error")
	}
}

func TestValidateAssetsIgnoresNonAssetHTMLReferences(t *testing.T) {
	t.Parallel()
	assets := viteAssets()
	assets["index.html"] = &fstest.MapFile{Data: []byte(`<!doctype html>
<html>
  <head>
    <script type="module" src="/assets/main-Abc12345.js"></script>
    <link rel="stylesheet" href="/assets/main-Abc12345.css">
    <link rel="help" href="/wiki">
  </head>
  <body>
    <a href="#details">details</a>
    <a href="mailto:security@example.com">security</a>
    <img src="data:image/gif;base64,R0lGODlhAQABAAAAACw=">
  </body>
</html>`)}

	if err := webui.ValidateAssets(assets); err != nil {
		t.Fatalf("ValidateAssets() error = %v", err)
	}
}

func TestHandlerWithAssetsSetsCacheHeaders(t *testing.T) {
	t.Parallel()
	handler := webui.HandlerWithAssets(viteAssets())

	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK {
		t.Fatalf("index status = %d, want 200", index.Code)
	}
	if got := index.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Fatalf("index Cache-Control = %q, want short public cache", got)
	}
	if index.Header().Get("ETag") == "" {
		t.Fatal("index ETag is empty")
	}

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/assets/main-Abc12345.js", nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", asset.Code)
	}
	if got := asset.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q, want immutable cache", got)
	}
}

func TestHandlerWithAssetsRevalidatesIndexByETag(t *testing.T) {
	t.Parallel()
	handler := webui.HandlerWithAssets(viteAssets())

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	request := httptest.NewRequest(http.MethodGet, "/route/inside/spa", nil)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	conditional := httptest.NewRecorder()
	handler.ServeHTTP(conditional, request)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional index = %d body=%q, want 304 empty", conditional.Code, conditional.Body.String())
	}
}

func TestHandlerWithAssetsNegotiatesPrecompressedAssets(t *testing.T) {
	t.Parallel()
	handler := webui.HandlerWithAssets(viteAssets())
	tests := []struct {
		name           string
		acceptEncoding string
		wantEncoding   string
		wantBody       string
	}{
		{name: "brotli preferred on tie", acceptEncoding: "gzip, br", wantEncoding: "br", wantBody: "brotli-data"},
		{name: "quality selects gzip", acceptEncoding: "br;q=0.4, gzip;q=0.9", wantEncoding: "gzip", wantBody: "gzip-data"},
		{name: "identity without header", wantBody: "console.log('ok')"},
		{name: "disabled encodings", acceptEncoding: "br;q=0, gzip;q=0", wantBody: "console.log('ok')"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/assets/main-Abc12345.js", nil)
			request.Header.Set("Accept-Encoding", tc.acceptEncoding)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if got := response.Header().Get("Content-Encoding"); got != tc.wantEncoding {
				t.Fatalf("Content-Encoding = %q, want %q", got, tc.wantEncoding)
			}
			if got := response.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Fatalf("Vary = %q, want Accept-Encoding", got)
			}
			if got := response.Body.String(); got != tc.wantBody {
				t.Fatalf("body = %q, want %q", got, tc.wantBody)
			}
			if got := response.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
				t.Fatalf("Content-Type = %q, want JavaScript", got)
			}
		})
	}
}

func TestHandlerWithAssetsUsesFallbackMIMEForCompressedSourceMap(t *testing.T) {
	t.Parallel()
	assets := viteAssets()
	assets["assets/main-Abc12345.js.map"] = &fstest.MapFile{Data: []byte(`{"version":3}`)}
	assets["assets/main-Abc12345.js.map.br"] = &fstest.MapFile{Data: []byte("compressed-map")}
	handler := webui.HandlerWithAssets(assets)
	request := httptest.NewRequest(http.MethodGet, "/assets/main-Abc12345.js.map", nil)
	request.Header.Set("Accept-Encoding", "br")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("compressed source map response = %d/%q", response.Code, response.Header().Get("Content-Encoding"))
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func viteAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                       {Data: []byte(`<!doctype html><html><head><script type="module" crossorigin src="/assets/main-Abc12345.js"></script><link rel="stylesheet" crossorigin href="/assets/main-Abc12345.css"></head><body></body></html>`)},
		"assets/main-Abc12345.js":          {Data: []byte("console.log('ok')")},
		"assets/main-Abc12345.js.gz":       {Data: []byte("gzip-data")},
		"assets/main-Abc12345.js.br":       {Data: []byte("brotli-data")},
		"assets/main-Abc12345.css":         {Data: []byte("body{}")},
		"assets/editor.worker-Abc12345.js": {Data: []byte("self.onmessage=()=>{}")},
		"assets/ts.worker-Abc12345.js":     {Data: []byte("self.onmessage=()=>{}")},
		"codeatlas-manifest.json": {Data: []byte(`{
			"buildVersion": "test",
			"editor": "monaco",
			"editorVersion": "0.53.0",
			"assets": {
				"entrypoints": ["assets/main-Abc12345.js"],
				"styles": ["assets/main-Abc12345.css"],
				"workers": ["assets/editor.worker-Abc12345.js", "assets/ts.worker-Abc12345.js"]
			}
		}`)},
	}
}

var _ fs.FS = fstest.MapFS{}
