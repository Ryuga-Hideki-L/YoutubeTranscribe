// Package api — HTTP API для интерфейса: каналы, индексация, поиск и поток
// изменений (SSE).
//
// Сервер слушает только loopback, но чужая страница в браузере всё равно может
// слать запросы на 127.0.0.1. Guard защищает от этого тремя проверками.
// Host должен быть локальным, это закрывает DNS rebinding. Запросы с
// Sec-Fetch-Site: cross-site отклоняются. Меняющие запросы принимаются только
// с JSON, а такой запрос чужая страница без preflight CORS отправить не может.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/indexer"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/library"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/youtube"
)

// MaxLimit — больше видео с канала за раз не берём.
const MaxLimit = 1000

// Server — обработчики API.
type Server struct {
	lib          *library.Library
	indexing     *indexer.Indexer
	dataAPI      *youtube.Client
	defaultLimit int
	shutdown     chan struct{}
}

// New создаёт API. Значение defaultLimit берётся, если клиент не указал лимит.
func New(lib *library.Library, indexing *indexer.Indexer, dataAPI *youtube.Client, defaultLimit int) *Server {
	return &Server{lib: lib, indexing: indexing, dataAPI: dataAPI, defaultLimit: defaultLimit, shutdown: make(chan struct{})}
}

// Shutdown завершает потоки SSE. Без этого http.Server.Shutdown ждал бы их вечно.
func (server *Server) Shutdown() { close(server.shutdown) }

// Register вешает обработчики на mux.
func (server *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/state", server.state)
	mux.HandleFunc("GET /api/events", server.events)
	mux.HandleFunc("GET /api/search", server.search)
	mux.HandleFunc("POST /api/resolve", server.resolve)
	mux.HandleFunc("POST /api/channels", server.add)
	mux.HandleFunc("POST /api/channels/{id}/refresh", server.refresh)
	mux.HandleFunc("POST /api/channels/{id}/cancel", server.cancel)
	mux.HandleFunc("DELETE /api/channels/{id}", server.remove)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "нет такого метода API")
	})
}

// Guard защищает от чужих страниц и DNS rebinding. Подробности в описании пакета.
func Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			http.Error(w, "чужой Host", http.StatusMisdirectedRequest)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// Пускаем same-origin и none (адрес набран вручную). Остальное,
			// включая same-site, отклоняем.
			fetchSite := r.Header.Get("Sec-Fetch-Site")
			if fetchSite != "" && fetchSite != "same-origin" && fetchSite != "none" {
				writeError(w, http.StatusForbidden, "cross_site", "запрос с чужого сайта")
				return
			}
			if r.Method == http.MethodPost {
				mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if mediaType != "application/json" {
					writeError(w, http.StatusUnsupportedMediaType, "json_only", "нужен Content-Type: application/json")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// localHost проверяет, что Host указывает на этот компьютер.
// Подходят localhost, любой *.localhost и адреса loopback.
func localHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ChannelView — канал из библиотеки и его задание на индексацию.
type ChannelView struct {
	library.Summary
	Job *indexer.Job `json:"job,omitempty"`
}

// State — всё, что интерфейсу нужно знать о библиотеке.
// Приходит и обычным ответом, и потоком SSE.
type State struct {
	HasKey       bool          `json:"hasKey"`
	DefaultLimit int           `json:"defaultLimit"`
	BlockedUntil time.Time     `json:"blockedUntil,omitzero"`
	Restricted   bool          `json:"restricted,omitempty"` // DNS включает Restricted Mode YouTube
	Channels     []ChannelView `json:"channels"`
}

func (server *Server) snapshot() State {
	jobs := server.indexing.Jobs()
	state := State{
		HasKey:       server.dataAPI.HasKey(),
		DefaultLimit: server.defaultLimit,
		BlockedUntil: server.indexing.BlockedUntil(),
		Restricted:   server.indexing.Restricted(),
		Channels:     []ChannelView{},
	}
	for _, summary := range server.lib.Summaries() {
		view := ChannelView{Summary: summary}
		if job, ok := jobs[summary.ID]; ok {
			view.Job = &job
		}
		state.Channels = append(state.Channels, view)
	}
	return state
}

func (server *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, server.snapshot())
}

// events — поток SSE. При подключении отправляется полный снимок, потом ещё
// один после каждого изменения. EventSource сам переподключается после сна.
// Снимки идут не чаще 4 раз в секунду. Пока ничего не меняется, обработчик
// просто ждёт, без таймеров.
func (server *Server) events(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	flusher := http.NewResponseController(w)

	changed, unsubscribe := server.indexing.Subscribe()
	defer unsubscribe()

	sendSnapshot := func() bool {
		raw, err := json.Marshal(server.snapshot())
		if err != nil {
			return false
		}
		if _, err := w.Write([]byte("event: state\ndata: ")); err != nil {
			return false
		}
		if _, err := w.Write(raw); err != nil {
			return false
		}
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return false
		}
		return flusher.Flush() == nil
	}
	if !sendSnapshot() {
		return
	}
	for {
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		case <-server.shutdown:
			return
		}
		if !sendSnapshot() {
			return
		}
		// Склеиваем частые изменения. Следующий снимок не раньше чем через 250 мс.
		select {
		case <-time.After(250 * time.Millisecond):
		case <-r.Context().Done():
			return
		case <-server.shutdown:
			return
		}
	}
}

func (server *Server) search(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	writeJSON(w, http.StatusOK, server.lib.Search(params.Get("q"), params.Get("channel")))
}

type resolveRequest struct {
	Query  string `json:"query"`
	Search bool   `json:"search"` // разрешить поиск по названию (100 единиц квоты)
}

type candidate struct {
	youtube.Channel
	InLibrary bool `json:"inLibrary"`
}

type resolveResponse struct {
	NeedsSearch bool        `json:"needsSearch,omitempty"`
	Channels    []candidate `json:"channels"`
}

func (server *Server) resolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "empty", "введите @имя, ссылку или название канала")
		return
	}
	if youtube.ParseQuery(req.Query).Kind == "search" && !req.Search {
		writeJSON(w, http.StatusOK, resolveResponse{NeedsSearch: true, Channels: []candidate{}})
		return
	}
	channels, err := server.dataAPI.Resolve(r.Context(), req.Query)
	if err != nil {
		writeYouTubeError(w, err)
		return
	}
	resp := resolveResponse{Channels: make([]candidate, 0, len(channels))}
	for _, channel := range channels {
		_, inLibrary := server.lib.Summary(channel.ID)
		resp.Channels = append(resp.Channels, candidate{Channel: channel, InLibrary: inLibrary})
	}
	writeJSON(w, http.StatusOK, resp)
}

type addRequest struct {
	ID    string `json:"id"`
	Limit int    `json:"limit"`
}

func (server *Server) add(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if !readJSON(w, r, &req) {
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = server.defaultLimit
	}
	limit = min(limit, MaxLimit)
	channel, err := server.dataAPI.Channel(r.Context(), req.ID)
	if err != nil {
		writeYouTubeError(w, err)
		return
	}
	if err := server.lib.Put(channel, limit); err != nil {
		writeError(w, http.StatusInternalServerError, "storage", err.Error())
		return
	}
	server.indexing.Enqueue(channel.ID, limit, false)
	writeJSON(w, http.StatusOK, server.snapshot())
}

type refreshRequest struct {
	Force bool `json:"force"` // снять предохранитель и пробовать сразу
}

func (server *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !readJSON(w, r, &req) {
		return
	}
	summary, ok := server.lib.Summary(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", library.ErrNoChannel.Error())
		return
	}
	server.indexing.Enqueue(summary.ID, summary.Limit, req.Force)
	writeJSON(w, http.StatusOK, server.snapshot())
}

func (server *Server) cancel(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !readJSON(w, r, &req) {
		return
	}
	server.indexing.Cancel(r.PathValue("id"))
	writeJSON(w, http.StatusOK, server.snapshot())
}

func (server *Server) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	server.indexing.Forget(id)
	if err := server.lib.Remove(id); err != nil {
		if errors.Is(err, library.ErrNoChannel) {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "storage", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, server.snapshot())
}

// apiError — ошибка для интерфейса. Code нужен логике, Message читает человек.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"error"`
}

func writeYouTubeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, youtube.ErrNoKey):
		writeError(w, http.StatusServiceUnavailable, "no_key", err.Error())
	case errors.Is(err, youtube.ErrBadKey):
		writeError(w, http.StatusBadGateway, "bad_key", err.Error())
	case errors.Is(err, youtube.ErrQuota):
		writeError(w, http.StatusTooManyRequests, "quota", err.Error())
	case errors.Is(err, youtube.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		log.Printf("YouTube Data API: %v", err)
		writeError(w, http.StatusBadGateway, "youtube", err.Error())
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("ответ API: %v", err)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "не разобрать запрос: "+err.Error())
		return false
	}
	return true
}
