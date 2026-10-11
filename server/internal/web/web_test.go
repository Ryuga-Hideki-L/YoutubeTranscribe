package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

var testBuild = fstest.MapFS{
	"index.html":          {Data: []byte("<!doctype html><title>Дословно</title><app-root></app-root>" + strings.Repeat(" ", 2000))},
	"main-BW4S54IJ.js":    {Data: []byte("console.log(1);" + strings.Repeat(";", 2000))},
	"styles-OK3NFUGO.css": {Data: []byte("body{}")},
	"favicon.svg":         {Data: []byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>")},
	"icons/logo.webp":     {Data: []byte("RIFF....WEBP")},
	".gitkeep":            {Data: nil},
}

func serve(t *testing.T, site *Site, method, target string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	site.ServeHTTP(rec, req)
	return rec
}

func load(t *testing.T) *Site {
	t.Helper()
	site, err := Load(testBuild, false)
	if err != nil {
		t.Fatal(err)
	}
	return site
}

func TestRootServesIndexWithoutCache(t *testing.T) {
	rec := serve(t, load(t), http.MethodGet, "/?q=привет", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q", got)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "youtube-nocookie") {
		t.Error("у страницы нет CSP с плеером YouTube")
	}
}

func TestCacheHeaders(t *testing.T) {
	site := load(t)
	cases := map[string]string{
		"/main-BW4S54IJ.js":    "public, max-age=31536000, immutable",
		"/styles-OK3NFUGO.css": "public, max-age=31536000, immutable",
		"/favicon.svg":         "public, max-age=3600",
		"/icons/logo.webp":     "public, max-age=3600",
	}
	for target, want := range cases {
		if got := serve(t, site, http.MethodGet, target, nil).Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control = %q, ждали %q", target, got, want)
		}
	}
	if got := serve(t, site, http.MethodGet, "/icons/logo.webp", nil).Header().Get("Content-Type"); got != "image/webp" {
		t.Errorf("webp: Content-Type = %q", got)
	}
}

func TestNotFound(t *testing.T) {
	site := load(t)
	for _, target := range []string{"/missing.js", "/.gitkeep", "/icons/", "/icons", "/api", "/../index.html"} {
		rec := serve(t, site, http.MethodGet, target, nil)
		if target == "/../index.html" {
			// path.Clean сводит выход за корень к /index.html. Это та же страница, утечки нет.
			if rec.Code != http.StatusOK {
				t.Errorf("%s: код %d", target, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: код %d, ждали 404", target, rec.Code)
		}
	}
}

func TestGzipNegotiation(t *testing.T) {
	site := load(t)
	plain := serve(t, site, http.MethodGet, "/main-BW4S54IJ.js", nil)
	zipped := serve(t, site, http.MethodGet, "/main-BW4S54IJ.js", map[string]string{"Accept-Encoding": "br, gzip"})
	refused := serve(t, site, http.MethodGet, "/main-BW4S54IJ.js", map[string]string{"Accept-Encoding": "gzip;q=0"})

	if zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("gzip не отдан клиенту, который его принимает")
	}
	if refused.Header().Get("Content-Encoding") != "" {
		t.Error("gzip отдан при gzip;q=0")
	}
	if got := zipped.Header().Get("Content-Length"); got != strconv.Itoa(zipped.Body.Len()) {
		t.Errorf("Content-Length сжатого ответа = %q, тело %d байт", got, zipped.Body.Len())
	}
	if zipped.Header().Get("ETag") == plain.Header().Get("ETag") {
		t.Error("у сжатого и несжатого варианта одинаковый ETag")
	}
	reader, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(reader)
	if !bytes.Equal(body, plain.Body.Bytes()) {
		t.Error("распакованное тело не совпадает с исходным")
	}
	// Картинки не сжимаются и не объявляют Vary.
	if serve(t, site, http.MethodGet, "/icons/logo.webp", map[string]string{"Accept-Encoding": "gzip"}).Header().Get("Content-Encoding") != "" {
		t.Error("webp сжат повторно")
	}
}

func TestRangeOverGzip(t *testing.T) {
	rec := serve(t, load(t), http.MethodGet, "/main-BW4S54IJ.js", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-9"})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("код %d, ждали 206", rec.Code)
	}
	if got := rec.Header().Get("Content-Length"); got != "10" || rec.Body.Len() != 10 {
		t.Errorf("Content-Length %q, тело %d байт — ждали 10", got, rec.Body.Len())
	}
}

func TestConditionalRequest(t *testing.T) {
	site := load(t)
	etag := serve(t, site, http.MethodGet, "/favicon.svg", nil).Header().Get("ETag")
	rec := serve(t, site, http.MethodGet, "/favicon.svg", map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: код %d, ждали 304", rec.Code)
	}
}

func TestMethods(t *testing.T) {
	site := load(t)
	if rec := serve(t, site, http.MethodHead, "/", nil); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD: код %d, тело %d байт", rec.Code, rec.Body.Len())
	}
	if rec := serve(t, site, http.MethodPost, "/", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: код %d", rec.Code)
	}
}

func TestLoadWithoutBuild(t *testing.T) {
	empty := fstest.MapFS{".gitkeep": {}}
	if _, err := Load(empty, false); err != ErrNoBuild {
		t.Errorf("без сборки: %v, ждали ErrNoBuild", err)
	}
	if _, err := Load(empty, true); err != nil {
		t.Errorf("режим -dev: %v", err)
	}
}

func TestEmbeddedBuild(t *testing.T) {
	site, err := Load(Static(), false)
	if err == ErrNoBuild {
		// ./tasks.sh test собирает фронт и ставит CI=1. Там пропуск дал бы ложный зелёный результат.
		if os.Getenv("CI") != "" {
			t.Fatal("сборки нет в static/, а тесты запущены как CI — ./tasks.sh test собирает её сам")
		}
		t.Skip("сборки нет в static/ — ./tasks.sh build")
	}
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(t, site, http.MethodGet, "/", nil)
	if !strings.Contains(rec.Body.String(), "<app-root>") {
		t.Error("в index.html нет <app-root>")
	}
	// Тексты лицензий раздаются вместе с сайтом.
	for _, target := range []string{"/3rdpartylicenses.txt"} {
		if code := serve(t, site, http.MethodGet, target, nil).Code; code != http.StatusOK {
			t.Errorf("%s: код %d", target, code)
		}
	}
}
