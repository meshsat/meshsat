package demo

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

//go:embed assets/demo.css assets/demo.js
var assets embed.FS

// AssetPrefix is where the demo's own two files are served; nothing of the
// web app lives there.
const AssetPrefix = "/_demo/"

// SPACSP is the web app's Content-Security-Policy on the demo: its own
// files, the map tiles it loads in the browser, the codec2 voice decoder's
// wasm, nothing else. The service worker's inline registration is taken
// out of the page instead of allowed (InjectIndex).
const SPACSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https://*.basemaps.cartocdn.com https://*.tile.openstreetmap.org; " +
	"font-src 'self'; media-src 'self' blob:; connect-src 'self'; worker-src 'self' blob:; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'"

var swRegistration = regexp.MustCompile(`(?s)<script>[^<]*serviceWorker[^<]*</script>`)

// bannerHTML stands above the web app on every page, outside #app, so the
// app's own code is untouched.
const bannerHTML = `<div id="meshsat-demo-banner" role="note"><strong>Demo</strong> ` +
	`<span>Made-up data. No radios, nothing is sent.</span> ` +
	`<a href="https://meshsat.net/">meshsat.net</a></div>`

// InjectIndex is the web app's index.html as the demo serves it: the demo's
// stylesheet and script in the head, the banner first in the body, and the
// service worker's inline registration removed (a public demo must not
// install a worker that keeps the app in a visitor's browser).
func InjectIndex(index []byte) []byte {
	out := swRegistration.ReplaceAll(index, nil)
	head := []byte(`<link rel="stylesheet" href="` + AssetPrefix + `demo.css"><script src="` + AssetPrefix + `demo.js"></script>`)
	out = bytes.Replace(out, []byte("</head>"), append(head, []byte("</head>")...), 1)
	if i := bytes.Index(out, []byte("<body")); i >= 0 {
		if j := bytes.IndexByte(out[i:], '>'); j >= 0 {
			at := i + j + 1
			out = append(out[:at:at], append([]byte(bannerHTML), out[at:]...)...)
		}
	}
	return out
}

// SPA serves the built web app from dist: its files as they are, index.html
// injected, and every other path (a history route like /map) as index.html.
// A missing file with an extension is a 404, not the app.
type SPA struct {
	dist  fs.FS
	index []byte
	files http.Handler
}

// NewSPA builds the handler. dist holds index.html and assets/.
func NewSPA(dist fs.FS) (*SPA, error) {
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, err
	}
	return &SPA{dist: dist, index: InjectIndex(index), files: http.FileServer(http.FS(dist))}, nil
}

func (s *SPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean("/" + r.URL.Path)
	if strings.HasPrefix(p, AssetPrefix) {
		name := "assets/" + strings.TrimPrefix(p, AssetPrefix)
		b, err := assets.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(name, ".css") {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
		return
	}
	if p == "/" || p == "/index.html" {
		s.serveIndex(w)
		return
	}
	if p == "/service-worker.js" {
		// No worker on the demo; a browser that kept one from elsewhere
		// gets nothing to update to.
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(p, "/")
	if st, err := fs.Stat(s.dist, rel); err == nil && !st.IsDir() {
		if strings.HasPrefix(rel, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		s.files.ServeHTTP(w, r)
		return
	}
	if path.Ext(rel) != "" {
		http.NotFound(w, r)
		return
	}
	s.serveIndex(w)
}

func (s *SPA) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(s.index)
}
