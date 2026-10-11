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

// YtDlp — запасной путь через yt-dlp.
// Он сам добывает PO-токены и быстрее подстраивается под изменения YouTube.
// Зато он медленнее: 4–5 запросов и 5–20 с на видео, а у основного пути два запроса и около секунды.
// yt-dlp только отдаёт список дорожек (-J).
// Саму дорожку качает наш код.
type YtDlp struct {
	Path    string
	HTTP    *http.Client
	Langs   []string
	Timeout time.Duration
}

// FindYtDlp возвращает путь к yt-dlp из PATH или пустую строку.
func FindYtDlp() string {
	path, err := exec.LookPath("yt-dlp")
	if err != nil {
		return ""
	}
	return path
}

// ytdlpFormat — один вариант дорожки в выводе yt-dlp.
type ytdlpFormat struct {
	Ext string `json:"ext"`
	URL string `json:"url"`
}

// ytdlpInfo — нужные поля вывода yt-dlp -J.
type ytdlpInfo struct {
	Language          string                   `json:"language"`
	Subtitles         map[string][]ytdlpFormat `json:"subtitles"`
	AutomaticCaptions map[string][]ytdlpFormat `json:"automatic_captions"`
}

// Fetch скачивает субтитры видео через yt-dlp.
func (ytdlp *YtDlp) Fetch(ctx context.Context, videoID string) (*Transcript, error) {
	timeout := ytdlp.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ytdlp.Path,
		"--ignore-config", "--no-playlist", "--skip-download", "-J",
		"--extractor-args", "youtube:skip=hls,dash,translated_subs",
		"https://www.youtube.com/watch?v="+videoID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("yt-dlp: %w", ctx.Err())
		}
		return nil, ytdlpError(stderr.String(), err)
	}
	var info ytdlpInfo
	if err := json.Unmarshal(output, &info); err != nil {
		return nil, fmt.Errorf("yt-dlp: ответ не разобран: %w", err)
	}
	track, ok := info.pick(ytdlp.Langs)
	if !ok {
		return nil, ErrNoTranscript
	}
	httpClient := ytdlp.HTTP
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

// pick выбирает дорожку по тем же правилам, что pickTrack.
// Исходную речь yt-dlp помечает в automatic_captions ключом «<язык>-orig».
// Если такого ключа нет, язык речи берётся из поля language.
// Остальные автоматические дорожки — машинные переводы.
func (info ytdlpInfo) pick(langs []string) (Track, bool) {
	srv1URL := func(formats []ytdlpFormat) string {
		for _, format := range formats {
			if format.Ext == "srv1" {
				return format.URL
			}
		}
		return ""
	}
	speechLang := info.Language
	autoKey := ""
	for captionKey := range info.AutomaticCaptions {
		if lang, ok := strings.CutSuffix(captionKey, "-orig"); ok {
			speechLang, autoKey = lang, captionKey
			break
		}
	}
	if autoKey == "" && speechLang != "" {
		if _, ok := info.AutomaticCaptions[speechLang]; ok {
			autoKey = speechLang
		}
	}

	var tracks []Track
	for lang, formats := range info.Subtitles {
		if trackURL := srv1URL(formats); trackURL != "" && lang != "live_chat" {
			tracks = append(tracks, Track{Lang: lang, URL: trackURL})
		}
	}
	// Порядок ключей map случаен. Сортируем, чтобы «первая ручная» была
	// всегда одной и той же.
	slices.SortFunc(tracks, func(left, right Track) int { return strings.Compare(left.Lang, right.Lang) })
	if autoKey != "" {
		if trackURL := srv1URL(info.AutomaticCaptions[autoKey]); trackURL != "" {
			tracks = append(tracks, Track{Lang: speechLang, Auto: true, URL: trackURL})
		}
	}
	if len(tracks) == 0 {
		return Track{}, false
	}
	// Без автоматической дорожки язык речи знает только yt-dlp, это поле language.
	// Оно важнее желаемых языков.
	if speechLang != "" {
		langs = append([]string{speechLang}, langs...)
	}
	return pickTrack(tracks, langs), true
}

// ytdlpError переводит последнюю ошибку yt-dlp в ошибки пакета.
func ytdlpError(stderr string, err error) error {
	lastErrorLine := ""
	for line := range strings.Lines(stderr) {
		if strings.HasPrefix(line, "ERROR:") {
			lastErrorLine = strings.TrimSpace(line)
		}
	}
	lowered := strings.ToLower(lastErrorLine)
	switch {
	case strings.Contains(lowered, "not a bot"), strings.Contains(lowered, "429"), strings.Contains(lowered, "too many requests"):
		return ErrBlocked
	case strings.Contains(lowered, "confirm your age"), strings.Contains(lowered, "age-restricted"),
		strings.Contains(lowered, "age restricted"), strings.Contains(lowered, "inappropriate"):
		return ErrAgeRestricted
	case strings.Contains(lowered, "unavailable"), strings.Contains(lowered, "private video"), strings.Contains(lowered, "removed"):
		return fmt.Errorf("%w: %s", ErrUnavailable, lastErrorLine)
	case lastErrorLine != "":
		return errors.New("yt-dlp: " + strings.TrimPrefix(lastErrorLine, "ERROR: "))
	}
	return fmt.Errorf("yt-dlp: %w", err)
}

// Fallback пробует основной загрузчик, а при сбое формата или протокола берёт запасной.
// Блокировку адреса и отсутствие сети запасной не лечит.
// Адрес и сеть те же, запросов только больше.
// Поэтому при ErrBlocked, ErrNetwork и окончательных ответах запасной не вызывается.
type Fallback struct {
	Primary, Secondary interface {
		Fetch(ctx context.Context, videoID string) (*Transcript, error)
	}
}

// Fetch работает по правилам, описанным у Fallback.
func (fallback Fallback) Fetch(ctx context.Context, videoID string) (*Transcript, error) {
	primaryTranscript, primaryErr := fallback.Primary.Fetch(ctx, videoID)
	if primaryErr == nil || isFinal(primaryErr) || errors.Is(primaryErr, ErrNetwork) || ctx.Err() != nil {
		return primaryTranscript, primaryErr
	}
	secondaryTranscript, secondaryErr := fallback.Secondary.Fetch(ctx, videoID)
	switch {
	case secondaryErr == nil:
		return secondaryTranscript, nil
	case isFinal(secondaryErr):
		// Запасной путь знает точнее, например про блокировку, отсутствие субтитров или 18+.
		return nil, secondaryErr
	}
	return nil, fmt.Errorf("%w (запасной путь: %v)", primaryErr, secondaryErr)
}
