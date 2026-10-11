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
func fakeYtDlp(t *testing.T, stdout, stderr string, code int) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.json")
	errf := filepath.Join(dir, "err.txt")
	os.WriteFile(out, []byte(stdout), 0o644)
	os.WriteFile(errf, []byte(stderr), 0o644)
	script := fmt.Sprintf("#!/bin/sh\ncat %q\ncat %q >&2\nexit %d\n", out, errf, code)
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
	// Ручные en и de, автоматические: ru-orig (речь) и машинный перевод en.
	info := fmt.Sprintf(`{"language":"ru",
		"subtitles":{"en":[{"ext":"srv1","url":"%[1]s/en"}],"de":[{"ext":"srv1","url":"%[1]s/de"}]},
		"automatic_captions":{"ru-orig":[{"ext":"json3","url":"x"},{"ext":"srv1","url":"%[1]s/ru"}],"en":[{"ext":"srv1","url":"%[1]s/en-auto"}]}}`, srv.URL)
	y := &YtDlp{Path: fakeYtDlp(t, info, "", 0)}
	tr, err := y.Fetch(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Lang != "ru" || !tr.Auto || len(tr.Lines) != 3 {
		t.Fatalf("got %+v", tr)
	}
}

func TestYtDlpErrors(t *testing.T) {
	cases := map[string]error{
		"ERROR: [youtube] abc: Sign in to confirm you’re not a bot. Use --cookies…": ErrBlocked,
		"ERROR: [youtube] abc: Video unavailable":                                   ErrUnavailable,
		"ERROR: [youtube] abc: Sign in to confirm your age":                         ErrAgeRestricted,
	}
	for stderr, want := range cases {
		y := &YtDlp{Path: fakeYtDlp(t, "", "WARNING: x\n"+stderr+"\n", 1)}
		if _, err := y.Fetch(context.Background(), "abc"); !errors.Is(err, want) {
			t.Errorf("%q: got %v, want %v", stderr, err, want)
		}
	}
	// «webpage» содержит «age», но это не возрастное ограничение.
	y := &YtDlp{Path: fakeYtDlp(t, "", "ERROR: [youtube] abc: Unable to download webpage\n", 1)}
	if _, err := y.Fetch(context.Background(), "abc"); err == nil || isFinal(err) {
		t.Errorf("сбой загрузки страницы стал окончательным: %v", err)
	}
	y = &YtDlp{Path: fakeYtDlp(t, `{"language":"en","subtitles":{},"automatic_captions":{}}`, "", 0)}
	if _, err := y.Fetch(context.Background(), "abc"); !errors.Is(err, ErrNoTranscript) {
		t.Errorf("без дорожек: %v", err)
	}
}

type scripted struct {
	err   error
	calls int
}

func (s *scripted) Fetch(context.Context, string) (*Transcript, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &Transcript{Lang: "ru"}, nil
}

func TestFallback(t *testing.T) {
	cases := []struct {
		name          string
		primary, sec  error
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
	for _, c := range cases {
		p, s := &scripted{err: c.primary}, &scripted{err: c.sec}
		_, err := Fallback{Primary: p, Secondary: s}.Fetch(context.Background(), "abc")
		if (s.calls > 0) != c.wantSecondary {
			t.Errorf("%s: запасной вызван %d раз", c.name, s.calls)
		}
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func TestYtDlpManualOnlyUsesLanguageField(t *testing.T) {
	srv := timedtextServer(t)
	info := fmt.Sprintf(`{"language":"en","subtitles":{"de":[{"ext":"srv1","url":"%[1]s/de"}],"en":[{"ext":"srv1","url":"%[1]s/en"}]},"automatic_captions":{}}`, srv.URL)
	tr, err := (&YtDlp{Path: fakeYtDlp(t, info, "", 0), Langs: []string{"ru"}}).Fetch(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Lang != "en" || tr.Auto {
		t.Fatalf("got %s auto=%v, want en", tr.Lang, tr.Auto)
	}
}
