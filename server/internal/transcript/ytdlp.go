package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// YtDlp — запасной путь через yt-dlp. Он сам добывает PO-токены и быстрее
// успевает за изменениями YouTube, но медленнее: 4–5 запросов и 5–20 с на
// видео против двух запросов и секунды у основного пути. yt-dlp только
// отдаёт список дорожек (-J), саму дорожку качает этот же код.
type YtDlp struct {
	Path    string
	HTTP    *http.Client
	Langs   []string
	Timeout time.Duration
}

// FindYtDlp — путь к yt-dlp в PATH или пустая строка.
func FindYtDlp() string {
	path, err := exec.LookPath("yt-dlp")
	if err != nil {
		return ""
	}
	return path
}

type ytdlpTrack struct {
	Ext string `json:"ext"`
	URL string `json:"url"`
}

type ytdlpInfo struct {
	Language          string                  `json:"language"`
	Subtitles         map[string][]ytdlpTrack `json:"subtitles"`
	AutomaticCaptions map[string][]ytdlpTrack `json:"automatic_captions"`
}

// Fetch скачивает субтитры видео через yt-dlp.
func (y *YtDlp) Fetch(ctx context.Context, videoID string) (*Transcript, error) {
	timeout := y.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, y.Path,
		"--ignore-config", "--no-playlist", "--skip-download", "-J",
		"--extractor-args", "youtube:skip=hls,dash,translated_subs",
		"https://www.youtube.com/watch?v="+videoID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("yt-dlp: %w", ctx.Err())
		}
		return nil, ytdlpError(stderr.String(), err)
	}
	var info ytdlpInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("yt-dlp: ответ не разобран: %w", err)
	}
	track, ok := info.pick(y.Langs)
	if !ok {
		return nil, ErrNoTranscript
	}
	httpClient := y.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, track.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		return nil, transportError(err)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrBlocked
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("YouTube ответил %s на дорожку yt-dlp", resp.Status)
	case len(bytes.TrimSpace(body.Bytes())) == 0:
		return nil, ErrPoToken
	}
	lines, err := Parse(body.Bytes())
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, ErrNoTranscript
	}
	return &Transcript{Lang: track.Lang, Auto: track.Auto, Lines: lines}, nil
}

// pick выбирает дорожку по тем же правилам, что pickTrack. Исходную
// речь yt-dlp помечает в automatic_captions ключом «<язык>-orig»; без него
// язык речи — поле language. Остальные автоматические — машинные переводы.
func (info ytdlpInfo) pick(langs []string) (Track, bool) {
	srv1 := func(tracks []ytdlpTrack) string {
		for _, t := range tracks {
			if t.Ext == "srv1" {
				return t.URL
			}
		}
		return ""
	}
	speech := info.Language
	autoKey := ""
	for key := range info.AutomaticCaptions {
		if lang, ok := strings.CutSuffix(key, "-orig"); ok {
			speech, autoKey = lang, key
			break
		}
	}
	if autoKey == "" && speech != "" {
		if _, ok := info.AutomaticCaptions[speech]; ok {
			autoKey = speech
		}
	}

	var tracks []Track
	for lang, list := range info.Subtitles {
		if u := srv1(list); u != "" && lang != "live_chat" {
			tracks = append(tracks, Track{Lang: lang, URL: u})
		}
	}
	// Порядок ключей map случаен — сортируем, чтобы «первая ручная» была
	// всегда одной и той же.
	slices.SortFunc(tracks, func(a, b Track) int { return strings.Compare(a.Lang, b.Lang) })
	if autoKey != "" {
		if u := srv1(info.AutomaticCaptions[autoKey]); u != "" {
			tracks = append(tracks, Track{Lang: speech, Auto: true, URL: u})
		}
	}
	if len(tracks) == 0 {
		return Track{}, false
	}
	// Без автоматической дорожки язык речи знает только yt-dlp (language) —
	// он важнее желаемых языков.
	if speech != "" {
		langs = append([]string{speech}, langs...)
	}
	return pickTrack(tracks, langs), true
}

// ytdlpError переводит последнюю ошибку yt-dlp в ошибки пакета.
func ytdlpError(stderr string, err error) error {
	last := ""
	for line := range strings.Lines(stderr) {
		if strings.HasPrefix(line, "ERROR:") {
			last = strings.TrimSpace(line)
		}
	}
	lower := strings.ToLower(last)
	switch {
	case strings.Contains(lower, "not a bot"), strings.Contains(lower, "429"), strings.Contains(lower, "too many requests"):
		return ErrBlocked
	case strings.Contains(lower, "confirm your age"), strings.Contains(lower, "age-restricted"),
		strings.Contains(lower, "age restricted"), strings.Contains(lower, "inappropriate"):
		return ErrAgeRestricted
	case strings.Contains(lower, "unavailable"), strings.Contains(lower, "private video"), strings.Contains(lower, "removed"):
		return fmt.Errorf("%w: %s", ErrUnavailable, last)
	case last != "":
		return errors.New("yt-dlp: " + strings.TrimPrefix(last, "ERROR: "))
	}
	return fmt.Errorf("yt-dlp: %w", err)
}

// Fallback — основной загрузчик, а при сбое формата или протокола — запасной.
// Блокировку адреса и отсутствие сети запасной не лечит (тот же адрес и та
// же сеть, только больше запросов), поэтому при ErrBlocked, ErrNetwork и
// окончательных ответах он не зовётся.
type Fallback struct {
	Primary, Secondary interface {
		Fetch(ctx context.Context, videoID string) (*Transcript, error)
	}
}

// Fetch — см. Fallback.
func (f Fallback) Fetch(ctx context.Context, videoID string) (*Transcript, error) {
	t, err := f.Primary.Fetch(ctx, videoID)
	if err == nil || isFinal(err) || errors.Is(err, ErrNetwork) || ctx.Err() != nil {
		return t, err
	}
	t2, err2 := f.Secondary.Fetch(ctx, videoID)
	switch {
	case err2 == nil:
		return t2, nil
	case isFinal(err2):
		// Запасной путь знает точнее: блокировка, нет субтитров, 18+.
		return nil, err2
	}
	return nil, fmt.Errorf("%w (запасной путь: %v)", err, err2)
}
