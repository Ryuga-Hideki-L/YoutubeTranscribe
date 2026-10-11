package transcript

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeYtDlp пишет в каталог теста скрипт, который ведёт себя как yt-dlp -J.
func fakeYtDlp(t *testing.T, stdout, stderr string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	stdoutFile := filepath.Join(dir, "out.json")
	stderrFile := filepath.Join(dir, "err.txt")
	os.WriteFile(stdoutFile, []byte(stdout), 0o644)
	os.WriteFile(stderrFile, []byte(stderr), 0o644)
	script := fmt.Sprintf("#!/bin/sh\ncat %q\ncat %q >&2\nexit %d\n", stdoutFile, stderrFile, exitCode)
	path := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func timedtextServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, timedtext)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestYtDlpPicksSpeechTrack(t *testing.T) {
	srv := timedtextServer(t)
	// Ручные en и de. Автоматические: ru-orig (речь) и машинный перевод en.
	info := fmt.Sprintf(`{"language":"ru",
		"subtitles":{"en":[{"ext":"srv1","url":"%[1]s/en"}],"de":[{"ext":"srv1","url":"%[1]s/de"}]},
		"automatic_captions":{"ru-orig":[{"ext":"json3","url":"x"},{"ext":"srv1","url":"%[1]s/ru"}],"en":[{"ext":"srv1","url":"%[1]s/en-auto"}]}}`, srv.URL)
	ytdlp := &YtDlp{Path: fakeYtDlp(t, info, "", 0)}
	result, err := ytdlp.Fetch(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if result.Lang != "ru" || !result.Auto || len(result.Lines) != 3 {
		t.Fatalf("got %+v", result)
	}
}

func TestYtDlpErrors(t *testing.T) {
	cases := map[string]error{
		"ERROR: [youtube] abc: Sign in to confirm you’re not a bot. Use --cookies…": ErrBlocked,
		"ERROR: [youtube] abc: Video unavailable":                                   ErrUnavailable,
		"ERROR: [youtube] abc: Sign in to confirm your age":                         ErrAgeRestricted,
	}
	for stderr, want := range cases {
		ytdlp := &YtDlp{Path: fakeYtDlp(t, "", "WARNING: x\n"+stderr+"\n", 1)}
		if _, err := ytdlp.Fetch(context.Background(), "abc"); !errors.Is(err, want) {
			t.Errorf("%q: got %v, want %v", stderr, err, want)
		}
	}
	// Слово «webpage» содержит «age», но это не возрастное ограничение.
	ytdlp := &YtDlp{Path: fakeYtDlp(t, "", "ERROR: [youtube] abc: Unable to download webpage\n", 1)}
	if _, err := ytdlp.Fetch(context.Background(), "abc"); err == nil || isFinal(err) {
		t.Errorf("сбой загрузки страницы стал окончательным: %v", err)
	}
	ytdlp = &YtDlp{Path: fakeYtDlp(t, `{"language":"en","subtitles":{},"automatic_captions":{}}`, "", 0)}
	if _, err := ytdlp.Fetch(context.Background(), "abc"); !errors.Is(err, ErrNoTranscript) {
		t.Errorf("без дорожек: %v", err)
	}
}

// scriptedFetcher при каждом вызове Fetch отдаёт заранее заданную ошибку и считает вызовы.
type scriptedFetcher struct {
	err   error
	calls int
}

func (fetcher *scriptedFetcher) Fetch(context.Context, string) (*Transcript, error) {
	fetcher.calls++
	if fetcher.err != nil {
		return nil, fetcher.err
	}
	return &Transcript{Lang: "ru"}, nil
}

func TestFallback(t *testing.T) {
	cases := []struct {
		name          string
		primaryErr    error
		secondaryErr  error
		wantSecondary bool
		want          error
	}{
		{"PO-токен — пробуем yt-dlp", ErrPoToken, nil, true, nil},
		{"непонятный ответ — пробуем yt-dlp", errors.New("ответ плеера: unexpected EOF"), nil, true, nil},
		{"блокировка — yt-dlp не трогаем", ErrBlocked, nil, false, ErrBlocked},
		{"нет субтитров — окончательно", ErrNoTranscript, nil, false, ErrNoTranscript},
		{"нет сети — yt-dlp не трогаем", fmt.Errorf("%w: dial", ErrNetwork), nil, false, ErrNetwork},
		{"yt-dlp тоже заблокирован — это блокировка", ErrPoToken, ErrBlocked, true, ErrBlocked},
		{"yt-dlp не смог — остаётся исходная ошибка", ErrPoToken, errors.New("boom"), true, ErrPoToken},
	}
	for _, tc := range cases {
		primary := &scriptedFetcher{err: tc.primaryErr}
		secondary := &scriptedFetcher{err: tc.secondaryErr}
		_, err := Fallback{Primary: primary, Secondary: secondary}.Fetch(context.Background(), "abc")
		if (secondary.calls > 0) != tc.wantSecondary {
			t.Errorf("%s: запасной вызван %d раз", tc.name, secondary.calls)
		}
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestYtDlpManualOnlyUsesLanguageField(t *testing.T) {
	srv := timedtextServer(t)
	info := fmt.Sprintf(`{"language":"en","subtitles":{"de":[{"ext":"srv1","url":"%[1]s/de"}],"en":[{"ext":"srv1","url":"%[1]s/en"}]},"automatic_captions":{}}`, srv.URL)
	result, err := (&YtDlp{Path: fakeYtDlp(t, info, "", 0), Langs: []string{"ru"}}).Fetch(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if result.Lang != "en" || result.Auto {
		t.Fatalf("got %s auto=%v, want en", result.Lang, result.Auto)
	}
}
