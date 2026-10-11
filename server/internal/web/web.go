// Package web раздаёт собранный Angular-интерфейс, вшитый в бинарник.
//
// Сборка Angular копируется в static/ (./tasks.sh build), а go:embed кладёт её в
// бинарник. На свежем клоне там только .gitkeep: так go build/vet/test
// работают без сборки фронта, а сервер без -dev отказывается стартовать.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed all:static
var embedded embed.FS

// Static — вшитая сборка сайта (содержимое static/).
func Static() fs.FS {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err) // каталог static вшит всегда, ошибка тут невозможна
	}
	return sub
}

// Имена с хешем содержимого от Angular (main-BW4S54IJ.js) не меняются —
// их можно кэшировать навсегда. Остальное (favicon, стикеры) — на час.
var hashedName = regexp.MustCompile(`-[A-Z0-9]{8}\.(js|css)$`)

// Сжимать имеет смысл только текст; картинки уже сжаты.
var compressible = map[string]bool{
	".html": true, ".js": true, ".css": true, ".json": true, ".svg": true, ".txt": true,
	".webmanifest": true,
}

func init() {
	// На NixOS нет /etc/mime.types, а встроенная таблица Go этих типов не знает.
	_ = mime.AddExtensionType(".woff2", "font/woff2")
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

type asset struct {
	body         []byte
	gzipped      []byte // nil, если сжатие не выигрывает
	etag         string
	contentType  string
	cacheControl string
}

// Site отвечает на запросы файлами сборки, всё держит в памяти.
type Site struct {
	assets map[string]*asset
}

// ErrNoBuild — в static/ нет index.html: фронт не собран (./tasks.sh build).
var ErrNoBuild = errors.New("в сборке нет index.html — сначала ./tasks.sh build (или запуск с -dev)")

// Load читает все файлы сборки, считает ETag и сжатые копии.
// allowEmpty разрешает старт без сборки (режим -dev).
func Load(files fs.FS, allowEmpty bool) (*Site, error) {
	site := &Site{assets: map[string]*asset{}}
	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || isHidden(name) {
			return err
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		site.assets[name] = newAsset(name, body)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("чтение сборки: %w", err)
	}
	if _, ok := site.assets["index.html"]; !ok && !allowEmpty {
		return nil, ErrNoBuild
	}
	return site, nil
}

func newAsset(name string, body []byte) *asset {
	sum := sha256.Sum256(body)
	a := &asset{
		body:         body,
		etag:         `"` + hex.EncodeToString(sum[:12]) + `"`,
		contentType:  mime.TypeByExtension(path.Ext(name)),
		cacheControl: "public, max-age=3600",
	}
	if a.contentType == "" {
		a.contentType = http.DetectContentType(body)
	}
	switch {
	case name == "index.html":
		// Страница ссылается на файлы с хешем — её саму браузер сверяет всегда.
		a.cacheControl = "no-cache"
	case hashedName.MatchString(name):
		a.cacheControl = "public, max-age=31536000, immutable"
	}
	if compressible[path.Ext(name)] {
		a.gzipped = gzipBytes(body)
	}
	return a
}

func gzipBytes(body []byte) []byte {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = w.Write(body)
	_ = w.Close()
	if buf.Len() >= len(body) {
		return nil
	}
	return buf.Bytes()
}

// Скрытые файлы (.gitkeep, .well-known и прочее) не раздаём.
func isHidden(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func (s *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}

	// На index.html — только корень (маршрутов у приложения нет, состояние
	// поиска в ?q=); неизвестный путь — честный 404.
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	a, ok := s.assets[name]
	if !ok || isHidden(name) || strings.HasSuffix(r.URL.Path, "/") && name != "index.html" {
		http.NotFound(w, r)
		return
	}

	h := w.Header()
	h.Set("Content-Type", a.contentType)
	h.Set("Cache-Control", a.cacheControl)
	h.Set("X-Content-Type-Options", "nosniff")
	// Не no-referrer: встроенный плеер YouTube без Referer отказывается играть.
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	if name == "index.html" {
		h.Set("Content-Security-Policy", csp)
	}

	body, etag := a.body, a.etag
	if a.gzipped != nil {
		h.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			h.Set("Content-Encoding", "gzip")
			body, etag = a.gzipped, strings.TrimSuffix(a.etag, `"`)+`-gz"`
			// С Content-Encoding ServeContent длину не ставит (ответ ушёл бы
			// чанками); для Range он перезапишет её сам.
			h.Set("Content-Length", strconv.Itoa(len(body)))
		}
	}
	h.Set("ETag", etag)
	http.ServeContent(w, r, "", noModTime, bytes.NewReader(body))
}

// csp разрешает, кроме своего, только плеер YouTube и его картинки.
// style-src 'unsafe-inline' нужен Angular: стили компонентов он вставляет
// тегами <style>.
const csp = "default-src 'self'; " +
	"script-src 'self' https://www.youtube.com; " +
	"frame-src https://www.youtube-nocookie.com https://www.youtube.com; " +
	"img-src 'self' data: https://i.ytimg.com https://yt3.ggpht.com https://yt3.googleusercontent.com; " +
	"style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; " +
	"object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

// Нулевое время — ServeContent не ставит Last-Modified: у вшитых файлов его
// нет, условные запросы идут по ETag.
var noModTime time.Time

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		// «gzip;q=0» означает отказ от gzip.
		if key, value, ok := strings.Cut(strings.TrimSpace(params), "="); ok && strings.TrimSpace(key) == "q" {
			q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			return err == nil && q > 0
		}
		return true
	}
	return false
}
