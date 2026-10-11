package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/indexer"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/library"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/transcript"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/youtube"
)

type nopFetcher struct{}

func (nopFetcher) Fetch(context.Context, string) (*transcript.Transcript, error) {
	return &transcript.Transcript{Lines: []transcript.Line{{Text: "привет всем"}}}, nil
}

func newServer(t *testing.T) (*Server, http.Handler, *library.Library) {
	lib, err := library.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataAPI := youtube.New("", nil, "http://127.0.0.1:1") // без ключа
	indexing := indexer.New(lib, dataAPI, nopFetcher{}, indexer.Config{})
	server := New(lib, indexing, dataAPI, 200)
	mux := http.NewServeMux()
	server.Register(mux)
	mux.Handle("/", http.NotFoundHandler())
	return server, Guard(mux), lib
}

func doRequest(handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:3000"
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

var jsonContentType = map[string]string{"Content-Type": "application/json"}

func TestGuard(t *testing.T) {
	_, handler, _ := newServer(t)
	cases := []struct {
		name   string
		host   string
		method string
		header map[string]string
		want   int
	}{
		{"свой Host", "127.0.0.1:3000", "GET", nil, 200},
		{"localhost", "localhost:4200", "GET", nil, 200},
		{"IPv6", "[::1]:3000", "GET", nil, 200},
		{"DNS rebinding", "evil.example:3000", "GET", nil, http.StatusMisdirectedRequest},
		{"чужой сайт", "127.0.0.1:3000", "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"соседний порт localhost", "127.0.0.1:3000", "GET", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"POST без JSON", "127.0.0.1:3000", "POST", map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		target := "/api/state"
		if tc.method == "POST" {
			target = "/api/resolve"
		}
		req := httptest.NewRequest(tc.method, target, strings.NewReader(`{"query":"x"}`))
		req.Host = tc.host
		for name, value := range tc.header {
			req.Header.Set(name, value)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: код %d, ждали %d", tc.name, rec.Code, tc.want)
		}
	}
}

func TestResolveAsksBeforePaidSearch(t *testing.T) {
	_, handler, _ := newServer(t)
	rec := doRequest(handler, "POST", "/api/resolve", `{"query":"BBC News"}`, jsonContentType)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needsSearch":true`) {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
	// Без ключа приходит понятная ошибка с кодом no_key.
	rec = doRequest(handler, "POST", "/api/resolve", `{"query":"@bbcnews"}`, jsonContentType)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"no_key"`) {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	_, handler, _ := newServer(t)
	rec := doRequest(handler, "GET", "/api/nope", "", nil)
	if rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("код %d, тип %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestSearchAndState(t *testing.T) {
	_, handler, lib := newServer(t)
	channel := youtube.Channel{ID: "UCx", Title: "Канал"}
	lib.Put(channel, 10)
	lib.Merge(channel.ID, []youtube.Video{{ID: "v1", Title: "Видео"}})
	lib.SetResult(channel.ID, "v1", library.StatusOK, &transcript.Transcript{Lines: []transcript.Line{{Start: 3, Text: "Привет всем"}}}, "")

	rec := doRequest(handler, "GET", "/api/search?q=%D0%BF%D1%80%D0%B8%D0%B2%D0%B5%D1%82", "", nil)
	var res library.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.TotalHits != 1 || res.Videos[0].ChannelTitle != "Канал" {
		t.Fatalf("поиск: %v %s", err, rec.Body)
	}

	rec = doRequest(handler, "GET", "/api/state", "", nil)
	var state State
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil || state.HasKey || len(state.Channels) != 1 || state.Channels[0].Counts.OK != 1 {
		t.Fatalf("состояние: %v %s", err, rec.Body)
	}
}

func TestEventsSnapshotAndShutdown(t *testing.T) {
	server, handler, _ := newServer(t)
	testServer := httptest.NewUnstartedServer(handler)
	testServer.Config.RegisterOnShutdown(server.Shutdown)
	testServer.Start()
	defer testServer.Close()

	req, _ := http.NewRequest("GET", testServer.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if contentType := resp.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("Content-Type %q", contentType)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "event: state\n" {
		t.Fatalf("первая строка %q: %v", line, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := testServer.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown с открытым SSE: %v (%s)", err, time.Since(start))
	}
}
