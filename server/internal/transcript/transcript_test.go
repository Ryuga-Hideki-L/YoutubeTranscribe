package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const timedtext = `<?xml version="1.0" encoding="utf-8" ?><transcript>` +
	`<text start="1.2" dur="2.16">All right, so here we are, in front of the
elephants</text>` +
	`<text start="5.318" dur="2.656">and that&amp;#39;s &lt;font color=&quot;#fff&quot;&gt;cool&lt;/font&gt;</text>` +
	`<text start="9" dur="1">   </text>` +
	`<text start="10" dur="1">&amp;gt;&amp;gt; Так, именно</text>` +
	`</transcript>`

func TestParse(t *testing.T) {
	lines, err := Parse([]byte(timedtext))
	if err != nil {
		t.Fatal(err)
	}
	want := []Line{
		{Start: 1.2, Dur: 2.16, Text: "All right, so here we are, in front of the elephants"},
		{Start: 5.318, Dur: 2.656, Text: "and that's cool"},
		{Start: 10, Dur: 1, Text: "— Так, именно"},
	}
	if fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Fatalf("got %v\nwant %v", lines, want)
	}
}

func TestPickTrack(t *testing.T) {
	en := Track{Lang: "en"}
	de := Track{Lang: "de-DE"}
	enAuto := Track{Lang: "en", Auto: true}
	ruAuto := Track{Lang: "ru", Auto: true}
	ru := Track{Lang: "ru"}
	cases := []struct {
		name   string
		tracks []Track
		langs  []string
		want   Track
	}{
		{"ручная на языке речи важнее перевода", []Track{de, en, enAuto}, nil, en},
		{"автоматическая, если ручных на языке речи нет", []Track{de, ruAuto}, nil, ruAuto},
		{"без автоматической — по желаемым языкам", []Track{en, ru}, []string{"ru"}, ru},
		{"регион не мешает", []Track{en, de}, []string{"de"}, de},
		{"иначе первая", []Track{de, en}, []string{"fr"}, de},
	}
	for _, tc := range cases {
		if got := pickTrack(tc.tracks, tc.langs); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// fakeYouTube — сервер, который ведёт себя как YouTube для одного видео.
type fakeYouTube struct {
	watchHits  atomic.Int32
	playerHits atomic.Int32
	watchPage  func(r *http.Request) string // по умолчанию страница с ключом
	player     func() any                   // ответ плеера
	status     int                          // если не 0, то код ответа плеера
	needKey    bool                         // без ключа плеер отвечает LOGIN_REQUIRED
	emptyText  bool                         // timedtext отдаёт пустой 200
}

func (fake *fakeYouTube) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch":
			fake.watchHits.Add(1)
			page := `<script>ytcfg.set({"INNERTUBE_API_KEY": "KEY123"})</script>`
			if fake.watchPage != nil {
				page = fake.watchPage(r)
			}
			fmt.Fprint(w, page)
		case "/youtubei/v1/player":
			fake.playerHits.Add(1)
			if key := r.URL.Query().Get("key"); key != "" && key != "KEY123" {
				http.Error(w, "bad key", http.StatusBadRequest)
				return
			}
			if fake.needKey && r.URL.Query().Get("key") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"playabilityStatus": map[string]any{"status": "LOGIN_REQUIRED", "reason": "Please sign in"}})
				return
			}
			var body struct {
				Context struct {
					Client struct{ ClientName string } `json:"client"`
				} `json:"context"`
				VideoID string `json:"videoId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Context.Client.ClientName != "ANDROID" {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			if fake.status != 0 {
				w.WriteHeader(fake.status)
				return
			}
			_ = json.NewEncoder(w).Encode(fake.player())
		case "/api/timedtext":
			if !fake.emptyText {
				fmt.Fprint(w, timedtext)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func okPlayer(base string, tracks ...map[string]any) func() any {
	return func() any {
		for _, captionTrack := range tracks {
			captionTrack["baseUrl"] = base + "/api/timedtext?v=x&lang=" + captionTrack["languageCode"].(string) + "&fmt=srv3"
		}
		return map[string]any{
			"playabilityStatus": map[string]any{"status": "OK"},
			"captions": map[string]any{"playerCaptionsTracklistRenderer": map[string]any{
				"captionTracks": tracks,
			}},
		}
	}
}

// playerWithExperiment отвечает как okPlayer, но к адресу дорожки добавляет exp=<experiment>.
func playerWithExperiment(base, experiment string) func() any {
	return func() any {
		player := okPlayer(base, playerTrack("en", "asr"))().(map[string]any)
		tracks := player["captions"].(map[string]any)["playerCaptionsTracklistRenderer"].(map[string]any)["captionTracks"].([]map[string]any)
		tracks[0]["baseUrl"] = tracks[0]["baseUrl"].(string) + "&exp=" + experiment
		return player
	}
}

func playerTrack(lang, kind string) map[string]any {
	return map[string]any{"languageCode": lang, "kind": kind, "name": map[string]any{"runs": []any{map[string]any{"text": lang}}}}
}

func TestFetchFastPath(t *testing.T) {
	fake := &fakeYouTube{}
	srv := fake.server(t)
	fake.player = okPlayer(srv.URL, playerTrack("en", "asr"))
	client := New(Options{BaseURL: srv.URL})

	result, err := client.Fetch(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if result.Lang != "en" || !result.Auto || len(result.Lines) != 3 {
		t.Fatalf("неожиданные субтитры: %+v", result)
	}
	if fake.watchHits.Load() != 0 || fake.playerHits.Load() != 1 {
		t.Errorf("страница %d, плеер %d, ожидалось 0 и 1 (два запроса на видео)", fake.watchHits.Load(), fake.playerHits.Load())
	}
}

func TestFetchFallsBackToWatchPageAndKeepsKey(t *testing.T) {
	fake := &fakeYouTube{needKey: true}
	srv := fake.server(t)
	fake.player = okPlayer(srv.URL, playerTrack("en", "asr"))
	client := New(Options{BaseURL: srv.URL})

	for range 3 {
		if _, err := client.Fetch(context.Background(), "abc"); err != nil {
			t.Fatal(err)
		}
	}
	if got := fake.watchHits.Load(); got != 1 {
		t.Errorf("страница видео загружена %d раз, ожидалась 1, дальше ключ уже известен", got)
	}
}

func TestConsentPage(t *testing.T) {
	fake := &fakeYouTube{needKey: true}
	fake.watchPage = func(r *http.Request) string {
		if cookie, err := r.Cookie("CONSENT"); err == nil && cookie.Value == "YES+cb.20260101" {
			return `"INNERTUBE_API_KEY":"KEY123"`
		}
		return `<form action="https://consent.youtube.com/s"><input name="v" value="cb.20260101"></form>`
	}
	srv := fake.server(t)
	fake.player = okPlayer(srv.URL, playerTrack("en", ""))

	if _, err := New(Options{BaseURL: srv.URL}).Fetch(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if fake.watchHits.Load() != 2 {
		t.Errorf("страница загружена %d раз, ожидалось 2: до и после согласия", fake.watchHits.Load())
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(fake *fakeYouTube, base string)
		want  error
	}{
		{"капча на странице", func(fake *fakeYouTube, _ string) {
			fake.needKey = true
			fake.watchPage = func(*http.Request) string { return `<div class="g-recaptcha"></div>` }
		}, ErrBlocked},
		{"429 от плеера", func(fake *fakeYouTube, _ string) { fake.status = http.StatusTooManyRequests }, ErrBlocked},
		{"плеер просит доказать, что не бот", func(fake *fakeYouTube, _ string) {
			fake.player = func() any {
				return map[string]any{"playabilityStatus": map[string]any{"status": "LOGIN_REQUIRED", "reason": reasonBot}}
			}
		}, ErrBlocked},
		{"возрастное ограничение", func(fake *fakeYouTube, _ string) {
			fake.player = func() any {
				return map[string]any{"playabilityStatus": map[string]any{"status": "LOGIN_REQUIRED", "reason": reasonAge}}
			}
		}, ErrAgeRestricted},
		{"видео недоступно", func(fake *fakeYouTube, _ string) {
			fake.player = func() any {
				return map[string]any{"playabilityStatus": map[string]any{"status": "ERROR", "reason": reasonUnavailable}}
			}
		}, ErrUnavailable},
		{"нет дорожек", func(fake *fakeYouTube, _ string) {
			fake.player = func() any { return map[string]any{"playabilityStatus": map[string]any{"status": "OK"}} }
		}, ErrNoTranscript},
		{"нужен PO-токен", func(fake *fakeYouTube, base string) {
			fake.player = playerWithExperiment(base, "xpe")
		}, ErrPoToken},
		{"xpv в списке экспериментов", func(fake *fakeYouTube, base string) {
			fake.player = playerWithExperiment(base, "abc,xpv")
		}, ErrPoToken},
		{"пустой ответ дорожки", func(fake *fakeYouTube, _ string) { fake.emptyText = true }, ErrPoToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeYouTube{}
			srv := fake.server(t)
			fake.player = okPlayer(srv.URL, playerTrack("en", "asr"))
			tc.setup(fake, srv.URL)
			_, err := New(Options{BaseURL: srv.URL}).Fetch(context.Background(), "abc")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRedactHidesQuery(t *testing.T) {
	client := New(Options{BaseURL: "http://127.0.0.1:1"}) // никто не слушает
	client.setKey("SECRET")
	_, err := client.Fetch(context.Background(), "abc")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("ключ попал в текст ошибки: %v", err)
	}
	if !errors.Is(err, ErrNetwork) {
		t.Errorf("отказ соединения — не ErrNetwork: %v", err)
	}
	// Отмена извне — не «нет сети».
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Fetch(ctx, "abc"); errors.Is(err, ErrNetwork) || !errors.Is(err, context.Canceled) {
		t.Errorf("отмена: %v", err)
	}
}
