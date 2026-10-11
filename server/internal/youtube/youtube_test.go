package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseQuery(t *testing.T) {
	cases := map[string]Query{
		"@BBCNews":   {"handle", "@BBCNews"},
		" @bbcnews ": {"handle", "@bbcnews"},
		"https://www.youtube.com/@BBCNews/videos":              {"handle", "@BBCNews"},
		"youtube.com/@%D0%B2%D0%B4%D1%83%D0%B4%D1%8C":          {"handle", "@вдудь"},
		"UC16niRr50-MSBwiO3YDb3RA":                             {"id", "UC16niRr50-MSBwiO3YDb3RA"},
		"https://youtube.com/channel/UC16niRr50-MSBwiO3YDb3RA": {"id", "UC16niRr50-MSBwiO3YDb3RA"},
		"https://www.youtube.com/user/bbcnews":                 {"username", "bbcnews"},
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10":     {"video", "dQw4w9WgXcQ"},
		"https://youtu.be/dQw4w9WgXcQ":                         {"video", "dQw4w9WgXcQ"},
		"https://m.youtube.com/shorts/dQw4w9WgXcQ":             {"video", "dQw4w9WgXcQ"},
		"https://www.youtube.com/c/BBCNews":                    {"search", "BBCNews"},
		"BBC News":                                             {"search", "BBC News"},
		"https://example.com/@BBCNews":                         {"search", "https://example.com/@BBCNews"},
	}
	for in, want := range cases {
		if got := ParseQuery(in); got != want {
			t.Errorf("ParseQuery(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]int{"PT1H2M3S": 3723, "PT45S": 45, "PT10M": 600, "P1DT1S": 86401, "P0D": 0, "bad": 0} {
		if got := parseDuration(in); got != want {
			t.Errorf("parseDuration(%q) = %d, want %d", in, got, want)
		}
	}
}

// fakeAPI отдаёт плейлист из pages страниц по 3 видео. Третье видео на странице — трансляция.
func fakeAPI(t *testing.T, pages int) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != "KEY" || r.URL.Query().Has("key") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":400,"message":"API key not valid.","details":[{"reason":"API_KEY_INVALID"}]}}`)
			return
		}
		query := r.URL.Query()
		switch r.URL.Path {
		case "/playlistItems":
			page := 0
			fmt.Sscan(query.Get("pageToken"), &page)
			var items []any
			for i := range 3 {
				id := fmt.Sprintf("vid%02d%05d", page, i)
				items = append(items, map[string]any{
					"snippet":        map[string]any{"title": "Видео " + id},
					"contentDetails": map[string]any{"videoId": id, "videoPublishedAt": "2026-10-01T10:00:00Z"},
					"status":         map[string]any{"privacyStatus": "public"},
				})
			}
			// Удалённое видео — без даты публикации.
			items = append(items, map[string]any{"contentDetails": map[string]any{"videoId": "deleted0000"}})
			resp := map[string]any{"items": items}
			if page+1 < pages {
				resp["nextPageToken"] = fmt.Sprint(page + 1)
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "/videos":
			var items []any
			for i, id := range strings.Split(query.Get("id"), ",") {
				live := "none"
				if i == 2 {
					live = "upcoming"
				}
				items = append(items, map[string]any{
					"id": id, "snippet": map[string]any{"liveBroadcastContent": live},
					"contentDetails": map[string]any{"duration": "PT1M5S"},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case "/channels":
			if query.Get("forHandle") == "@quota" {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"error":{"code":403,"message":"quota","errors":[{"reason":"quotaExceeded"}]}}`)
				return
			}
			if query.Get("forHandle") != "@bbcnews" {
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{
				"id":             "UC16niRr50-MSBwiO3YDb3RA",
				"snippet":        map[string]any{"title": "BBC News", "customUrl": "@bbcnews", "thumbnails": map[string]any{"medium": map[string]any{"url": "https://yt3/x.jpg"}}},
				"contentDetails": map[string]any{"relatedPlaylists": map[string]any{"uploads": "UU16niRr50-MSBwiO3YDb3RA"}},
				"statistics":     map[string]any{"videoCount": "15000"},
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestUploadsSkipsLiveAndDeletedAndStopsAtLimit(t *testing.T) {
	server := fakeAPI(t, 10)
	videos, err := New("KEY", nil, server.URL).Uploads(context.Background(), "UUx", 5)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, video := range videos {
		ids = append(ids, video.ID)
		if video.Duration != 65 {
			t.Errorf("%s: длительность %d, ожидалось 65", video.ID, video.Duration)
		}
	}
	want := "vid0000000 vid0000001 vid0100000 vid0100001 vid0200000"
	if strings.Join(ids, " ") != want {
		t.Fatalf("got %v, want %s", ids, want)
	}
}

func TestUploadsEndsWithPlaylist(t *testing.T) {
	server := fakeAPI(t, 2)
	videos, err := New("KEY", nil, server.URL).Uploads(context.Background(), "UUx", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 4 {
		t.Fatalf("got %d videos, want 4", len(videos))
	}
}

func TestResolve(t *testing.T) {
	server := fakeAPI(t, 1)
	client := New("KEY", nil, server.URL)
	channels, err := client.Resolve(context.Background(), "https://www.youtube.com/@bbcnews")
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].Uploads != "UU16niRr50-MSBwiO3YDb3RA" || channels[0].VideoCount != 15000 || channels[0].Thumb == "" {
		t.Fatalf("неожиданный канал: %+v", channels)
	}
	if _, err := client.Resolve(context.Background(), "@nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("неизвестный канал: %v", err)
	}
	if _, err := client.Resolve(context.Background(), "@quota"); !errors.Is(err, ErrQuota) {
		t.Errorf("квота: %v", err)
	}
	if _, err := New("WRONG", nil, server.URL).Resolve(context.Background(), "@bbcnews"); !errors.Is(err, ErrBadKey) {
		t.Errorf("плохой ключ: %v", err)
	}
	if _, err := New("", nil, server.URL).Resolve(context.Background(), "@bbcnews"); !errors.Is(err, ErrNoKey) {
		t.Errorf("без ключа: %v", err)
	}
}

func TestKeyNotInErrors(t *testing.T) {
	_, err := New("SECRET123", nil, "http://127.0.0.1:1").Resolve(context.Background(), "@bbcnews")
	if err == nil || strings.Contains(err.Error(), "SECRET123") {
		t.Fatalf("ключ в тексте ошибки: %v", err)
	}
}
