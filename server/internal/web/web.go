// Package web раздаёт собранный Angular-интерфейс, вшитый в бинарник.
//
// Сборка Angular копируется в static/ (./tasks.sh build), а go:embed кладёт её
// в бинарник. На свежем клоне там только .gitkeep. Поэтому go build, vet и test
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

// Имена с хешем содержимого от Angular (main-BW4S54IJ.js) не меняются,
// поэтому их можно кэшировать навсегда. Остальное (favicon, стикеры)
// кэшируется на час.
var hashedName = regexp.MustCompile(`-[A-Z0-9]{8}\.(js|css)$`)

// Сжимать имеет смысл только текст. Картинки уже сжаты.
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

// ErrNoBuild — в static/ нет index.html, значит фронт не собран (./tasks.sh build).
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
	hash := sha256.Sum256(body)
	file := &asset{
		body:         body,
		etag:         `"` + hex.EncodeToString(hash[:12]) + `"`,
		contentType:  mime.TypeByExtension(path.Ext(name)),
		cacheControl: "public, max-age=3600",
	}
	if file.contentType == "" {
		file.contentType = http.DetectContentType(body)
	}
	switch {
	case name == "index.html":
		// Страница ссылается на файлы с хешем, поэтому её саму браузер сверяет всегда.
		file.cacheControl = "no-cache"
	case hashedName.MatchString(name):
		file.cacheControl = "public, max-age=31536000, immutable"
	}
	if compressible[path.Ext(name)] {
		file.gzipped = gzipBytes(body)
	}
	return file
}

func gzipBytes(body []byte) []byte {
	var buf bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = writer.Write(body)
	_ = writer.Close()
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

// ServeHTTP отдаёт файл сборки по пути запроса.
func (site *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}

	// На index.html ведёт только корень. Маршрутов у приложения нет, состояние
	// поиска лежит в ?q=. Неизвестный путь получает честный 404.
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	file, ok := site.assets[name]
	if !ok || isHidden(name) || strings.HasSuffix(r.URL.Path, "/") && name != "index.html" {
		http.NotFound(w, r)
		return
	}

	header := w.Header()
	header.Set("Content-Type", file.contentType)
	header.Set("Cache-Control", file.cacheControl)
	header.Set("X-Content-Type-Options", "nosniff")
	// Не no-referrer, потому что встроенный плеер YouTube без Referer отказывается играть.
	header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	if name == "index.html" {
		header.Set("Content-Security-Policy", csp)
	}

	body, etag := file.body, file.etag
	if file.gzipped != nil {
		header.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			header.Set("Content-Encoding", "gzip")
			body, etag = file.gzipped, strings.TrimSuffix(file.etag, `"`)+`-gz"`
			// С Content-Encoding ServeContent длину не ставит, и ответ ушёл бы
			// чанками. Для Range он перезапишет длину сам.
			header.Set("Content-Length", strconv.Itoa(len(body)))
		}
	}
	header.Set("ETag", etag)
	http.ServeContent(w, r, "", noModTime, bytes.NewReader(body))
}

// csp разрешает, кроме своего, только плеер YouTube и его картинки.
// style-src 'unsafe-inline' нужен Angular, потому что стили компонентов он
// вставляет тегами <style>.
const csp = "default-src 'self'; " +
	"script-src 'self' https://www.youtube.com; " +
	"frame-src https://www.youtube-nocookie.com https://www.youtube.com; " +
	"img-src 'self' data: https://i.ytimg.com https://yt3.ggpht.com https://yt3.googleusercontent.com; " +
	"style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; " +
	"object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

// Нулевое время нужно, чтобы ServeContent не ставил Last-Modified. У вшитых
// файлов его нет, условные запросы идут по ETag.
var noModTime time.Time

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		// «gzip;q=0» означает отказ от gzip.
		if key, value, ok := strings.Cut(strings.TrimSpace(params), "="); ok && strings.TrimSpace(key) == "q" {
			quality, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			return err == nil && quality > 0
		}
		return true
	}
	return false
}
