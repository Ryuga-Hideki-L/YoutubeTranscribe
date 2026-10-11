//go:build live

// Живая проверка на настоящем YouTube. Запуск: go test -tags live ./internal/transcript/
// В обычный прогон и CI она не входит, потому что зависит от сети и настроения YouTube.
package transcript

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

// countingTransport считает запросы, прошедшие через него.
type countingTransport struct{ count atomic.Int32 }

func (transport *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport.count.Add(1)
	return http.DefaultTransport.RoundTrip(req)
}

func TestLive(t *testing.T) {
	cases := []struct {
		videoID, lang string
		auto          bool
	}{
		{"dQw4w9WgXcQ", "en", false}, // ручные субтитры и ещё 5 языков
		{"jNQXAC9IVRw", "en", false}, // первое видео YouTube
	}
	for _, tc := range cases {
		transport := &countingTransport{}
		client := New(Options{HTTP: &http.Client{Transport: transport}})
		result, err := client.Fetch(context.Background(), tc.videoID)
		if err != nil {
			t.Fatalf("%s: %v", tc.videoID, err)
		}
		t.Logf("%s: %s auto=%v, %d строк, запросов %d, первая: %q", tc.videoID, result.Lang, result.Auto, len(result.Lines), transport.count.Load(), result.Lines[0].Text)
		if result.Lang != tc.lang || len(result.Lines) < 3 {
			t.Errorf("%s: язык %s, строк %d", tc.videoID, result.Lang, len(result.Lines))
		}
		if transport.count.Load() != 2 {
			t.Errorf("%s: %d запросов, ожидалось 2", tc.videoID, transport.count.Load())
		}
	}
}

func TestLiveRestrictedMode(t *testing.T) {
	t.Logf("Restricted Mode в DNS этой сети: %v", RestrictedMode(context.Background(), nil))
}

func TestLiveYtDlp(t *testing.T) {
	path := FindYtDlp()
	if path == "" {
		t.Skip("yt-dlp нет в PATH")
	}
	// Как в приложении, где -langs по умолчанию ru,en. У этого видео поле language пустое.
	result, err := (&YtDlp{Path: path, Langs: []string{"ru", "en"}}).Fetch(context.Background(), "jNQXAC9IVRw")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("yt-dlp: %s auto=%v, %d строк, первая: %q", result.Lang, result.Auto, len(result.Lines), result.Lines[0].Text)
	if result.Lang != "en" {
		t.Errorf("язык %s, want en", result.Lang)
	}
}
