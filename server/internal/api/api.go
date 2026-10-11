// Package api — HTTP API для интерфейса: каналы, индексация, поиск и поток
// изменений (SSE).
//
// Сервер слушает только loopback, но это не защищает от чужих страниц в
// браузере: они могут слать запросы на 127.0.0.1. Поэтому Guard пускает
// только запросы с локальным Host (против DNS rebinding), отклоняет
// Sec-Fetch-Site: cross-site, а меняющие запросы принимает лишь с JSON —
// такой запрос чужая страница без preflight CORS не отправит.
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
	ix           *indexer.Indexer
	yt           *youtube.Client
	defaultLimit int
	shutdown     chan struct{}
}

// New создаёт API. Shutdown закрывает открытые потоки SSE.
func New(lib *library.Library, ix *indexer.Indexer, yt *youtube.Client, defaultLimit int) *Server {
	return &Server{lib: lib, ix: ix, yt: yt, defaultLimit: defaultLimit, shutdown: make(chan struct{})}
}

// Shutdown завершает потоки SSE — иначе http.Server.Shutdown ждал бы их вечно.
func (s *Server) Shutdown() { close(s.shutdown) }

// Register вешает обработчики на mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("POST /api/resolve", s.resolve)
	mux.HandleFunc("POST /api/channels", s.add)
	mux.HandleFunc("POST /api/channels/{id}/refresh", s.refresh)
	mux.HandleFunc("POST /api/channels/{id}/cancel", s.cancel)
	mux.HandleFunc("DELETE /api/channels/{id}", s.remove)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "нет такого метода API")
	})
}

// Guard — защита от чужих страниц и DNS rebinding (см. описание пакета).
func Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			http.Error(w, "чужой Host", http.StatusMisdirectedRequest)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				writeError(w, http.StatusForbidden, "cross_site", "запрос с чужого сайта")
				return
			}
			if r.Method == http.MethodPost {
				mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if mt != "application/json" {
					writeError(w, http.StatusUnsupportedMediaType, "json_only", "нужен Content-Type: application/json")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

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

// ChannelView — канал в библиотеке и его задание.
type ChannelView struct {
	library.Summary
	Job *indexer.Job `json:"job,omitempty"`
}

// State — всё, что нужно интерфейсу о библиотеке; приходит и потоком SSE.
type State struct {
	HasKey       bool          `json:"hasKey"`
	DefaultLimit int           `json:"defaultLimit"`
	BlockedUntil time.Time     `json:"blockedUntil,omitzero"`
	Restricted   bool          `json:"restricted,omitempty"` // DNS включает Restricted Mode YouTube
	Channels     []ChannelView `json:"channels"`
}

func (s *Server) snapshot() State {
	jobs := s.ix.Jobs()
	st := State{
		HasKey:       s.yt.HasKey(),
		DefaultLimit: s.defaultLimit,
		BlockedUntil: s.ix.BlockedUntil(),
		Restricted:   s.ix.Restricted(),
		Channels:     []ChannelView{},
	}
	for _, sum := range s.lib.Summaries() {
		cv := ChannelView{Summary: sum}
		if j, ok := jobs[sum.ID]; ok {
			cv.Job = &j
		}
		st.Channels = append(st.Channels, cv)
	}
	return st
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshot())
}

// events — поток SSE: полный снимок при подключении (EventSource сам
// переподключается после сна) и после каждого изменения, не чаще 4 раз в
// секунду. Пока ничего не меняется, обработчик просто ждёт — без таймеров.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)

	changed, unsubscribe := s.ix.Subscribe()
	defer unsubscribe()

	send := func() bool {
		raw, err := json.Marshal(s.snapshot())
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
		return rc.Flush() == nil
	}
	if !send() {
		return
	}
	for {
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		case <-s.shutdown:
			return
		}
		if !send() {
			return
		}
		// Сливаем частые изменения: следующий снимок не раньше чем через 250 мс.
		select {
		case <-time.After(250 * time.Millisecond):
		case <-r.Context().Done():
			return
		case <-s.shutdown:
			return
		}
	}
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, s.lib.Search(q.Get("q"), q.Get("channel")))
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

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
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
	chs, err := s.yt.Resolve(r.Context(), req.Query)
	if err != nil {
		writeYouTubeError(w, err)
		return
	}
	resp := resolveResponse{Channels: make([]candidate, 0, len(chs))}
	for _, ch := range chs {
		_, in := s.lib.Summary(ch.ID)
		resp.Channels = append(resp.Channels, candidate{Channel: ch, InLibrary: in})
	}
	writeJSON(w, http.StatusOK, resp)
}

type addRequest struct {
	ID    string `json:"id"`
	Limit int    `json:"limit"`
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if !readJSON(w, r, &req) {
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = s.defaultLimit
	}
	limit = min(limit, MaxLimit)
	ch, err := s.yt.Channel(r.Context(), req.ID)
	if err != nil {
		writeYouTubeError(w, err)
		return
	}
	if err := s.lib.Put(ch, limit); err != nil {
		writeError(w, http.StatusInternalServerError, "storage", err.Error())
		return
	}
	s.ix.Enqueue(ch.ID, limit, false)
	writeJSON(w, http.StatusOK, s.snapshot())
}

type refreshRequest struct {
	Force bool `json:"force"` // снять предохранитель и пробовать сразу
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !readJSON(w, r, &req) {
		return
	}
	sum, ok := s.lib.Summary(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", library.ErrNoChannel.Error())
		return
	}
	s.ix.Enqueue(sum.ID, sum.Limit, req.Force)
	writeJSON(w, http.StatusOK, s.snapshot())
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !readJSON(w, r, &req) {
		return
	}
	s.ix.Cancel(r.PathValue("id"))
	writeJSON(w, http.StatusOK, s.snapshot())
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.ix.Forget(id)
	if err := s.lib.Remove(id); err != nil {
		if errors.Is(err, library.ErrNoChannel) {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "storage", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.snapshot())
}

// apiError — ошибка для интерфейса: код для логики и текст для человека.
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

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Code: code, Message: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("ответ API: %v", err)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "не разобрать запрос: "+err.Error())
		return false
	}
	return true
}
