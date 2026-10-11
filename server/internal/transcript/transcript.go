// Package transcript скачивает субтитры видео YouTube без ключа API.
//
// Схема та же, что у python-библиотеки youtube-transcript-api (1.2.x):
// запрос к внутреннему API плеера (innertube /youtubei/v1/player) от имени
// клиента Android даёт список дорожек субтитров, а дорожка скачивается по её
// baseUrl в формате XML <transcript><text start dur>…</text>.
//
// Быстрый путь — сразу POST к плееру (две загрузки на видео; ключ innertube
// плееру Android не обязателен, проверено 11.10.2026). Если YouTube ответил
// непонятно, берётся страница видео: из неё ключ и cookies (согласие для ЕС),
// потом тот же POST. Ключ запоминается и дальше отправляется с быстрым путём.
package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Ошибки, по которым индексация решает, что делать с видео.
var (
	// ErrBlocked — YouTube требует капчу или отвечает 429: запросы с этого
	// адреса временно не пускают. Повторять сразу бесполезно.
	ErrBlocked = errors.New("YouTube временно блокирует запросы с этого адреса")
	// ErrNoTranscript — у видео нет ни одной дорожки субтитров.
	ErrNoTranscript = errors.New("у видео нет субтитров")
	// ErrUnavailable — видео удалено, скрыто или недоступно в регионе.
	ErrUnavailable = errors.New("видео недоступно")
	// ErrAgeRestricted — видео 18+, без входа в аккаунт субтитры не отдаются.
	ErrAgeRestricted = errors.New("видео с возрастным ограничением")
	// ErrPoToken — дорожка требует PO-токен, без браузера её не скачать.
	ErrPoToken = errors.New("YouTube требует PO-токен для этих субтитров")
	// ErrNetwork — запрос не дошёл: нет сети, DNS, прокси, таймаут. С самим
	// видео всё может быть в порядке — его надо повторить позже.
	ErrNetwork = errors.New("нет связи с YouTube")
)

// Line — одна строка субтитров: начало и длительность в секундах.
type Line struct {
	Start float64 `json:"s"`
	Dur   float64 `json:"d"`
	Text  string  `json:"t"`
}

// Transcript — скачанные субтитры одного видео.
type Transcript struct {
	Lang  string `json:"lang"`
	Auto  bool   `json:"auto"` // распознанные автоматически (kind=asr)
	Lines []Line `json:"lines"`
}

// Track — дорожка субтитров из ответа плеера.
type Track struct {
	Lang string
	Name string
	Auto bool
	URL  string
}

// Client скачивает субтитры. Нулевое значение не годится — нужен New.
type Client struct {
	http          *http.Client
	base          string // https://www.youtube.com, в тестах — httptest
	clientVersion string
	langs         []string

	mu     sync.Mutex
	apiKey string
}

// Options настраивают клиента; пустые поля — значения по умолчанию.
type Options struct {
	HTTP *http.Client
	// BaseURL — адрес YouTube; в тестах подменяется.
	BaseURL string
	// ClientVersion — версия клиента Android, которой представляемся плееру.
	ClientVersion string
	// Langs — желаемые языки ручных субтитров, если у видео нет
	// автоматической дорожки, по которой видно язык речи.
	Langs []string
}

// DefaultClientVersion — версия из youtube-transcript-api 1.2.4.
const DefaultClientVersion = "20.10.38"

// New создаёт клиента.
func New(o Options) *Client {
	c := &Client{
		http:          o.HTTP,
		base:          strings.TrimSuffix(o.BaseURL, "/"),
		clientVersion: o.ClientVersion,
		langs:         o.Langs,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.base == "" {
		c.base = "https://www.youtube.com"
	}
	if c.clientVersion == "" {
		c.clientVersion = DefaultClientVersion
	}
	return c
}

// Fetch скачивает субтитры видео на языке речи (см. pickTrack).
func (c *Client) Fetch(ctx context.Context, videoID string) (*Transcript, error) {
	tracks, session, err := c.Tracks(ctx, videoID)
	if err != nil {
		return nil, err
	}
	track := pickTrack(tracks, c.langs)
	lines, err := c.download(ctx, session, track)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, ErrNoTranscript
	}
	return &Transcript{Lang: track.Lang, Auto: track.Auto, Lines: lines}, nil
}

// Tracks возвращает дорожки субтитров видео и HTTP-сессию, в которой их
// надо скачивать (с cookies страницы, если она понадобилась).
func (c *Client) Tracks(ctx context.Context, videoID string) ([]Track, *http.Client, error) {
	tracks, err := c.player(ctx, c.http, c.key(), videoID)
	if err == nil || isFinal(err) {
		return tracks, c.http, err
	}
	// Без cookies плеер ответил странно или ключ устарел — повторяем
	// полной схемой через страницу видео.

	session := c.session()
	key, err := c.watchPage(ctx, session, videoID)
	if err != nil {
		return nil, nil, err
	}
	c.setKey(key)
	tracks, err = c.player(ctx, session, key, videoID)
	return tracks, session, err
}

// isFinal — ошибка, после которой страница видео ничего не изменит.
func isFinal(err error) bool {
	return errors.Is(err, ErrBlocked) || errors.Is(err, ErrNoTranscript) ||
		errors.Is(err, ErrUnavailable) || errors.Is(err, ErrAgeRestricted)
}

func (c *Client) key() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.apiKey
}

func (c *Client) setKey(key string) {
	c.mu.Lock()
	c.apiKey = key
	c.mu.Unlock()
}

// session — HTTP-клиент со своей банкой cookies: у каждого видео свежая,
// как в youtube-transcript-api, чтобы не таскать за собой «отпечаток».
func (c *Client) session() *http.Client {
	jar, _ := cookiejar.New(nil) // ошибки у cookiejar.New без опций не бывает
	s := *c.http
	s.Jar = jar
	return &s
}

var (
	reInnertubeKey = regexp.MustCompile(`"INNERTUBE_API_KEY":\s*"([a-zA-Z0-9_-]+)"`)
	reConsentValue = regexp.MustCompile(`name="v" value="(.*?)"`)
)

// watchPage открывает страницу видео (при надобности с согласием на cookies)
// и достаёт из неё ключ innertube.
func (c *Client) watchPage(ctx context.Context, session *http.Client, videoID string) (string, error) {
	page, err := c.get(ctx, session, c.base+"/watch?v="+url.QueryEscape(videoID))
	if err != nil {
		return "", err
	}
	if bytes.Contains(page, []byte(`action="https://consent.youtube.com/s"`)) {
		m := reConsentValue.FindSubmatch(page)
		if m == nil {
			return "", errors.New("страница согласия YouTube без значения v")
		}
		u, _ := url.Parse(c.base)
		session.Jar.SetCookies(u, []*http.Cookie{{
			Name: "CONSENT", Value: "YES+" + string(m[1]), Path: "/",
		}})
		if page, err = c.get(ctx, session, c.base+"/watch?v="+url.QueryEscape(videoID)); err != nil {
			return "", err
		}
		if bytes.Contains(page, []byte(`action="https://consent.youtube.com/s"`)) {
			return "", errors.New("YouTube не принял согласие на cookies")
		}
	}
	if bytes.Contains(page, []byte(`class="g-recaptcha"`)) {
		return "", ErrBlocked
	}
	m := reInnertubeKey.FindSubmatch(page)
	if m == nil {
		return "", errors.New("на странице видео нет ключа innertube")
	}
	return string(m[1]), nil
}

// Ответ плеера — только нужные поля.
type playerResponse struct {
	PlayabilityStatus struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
	Captions struct {
		Renderer *struct {
			CaptionTracks []struct {
				BaseURL      string `json:"baseUrl"`
				LanguageCode string `json:"languageCode"`
				Kind         string `json:"kind"`
				Name         struct {
					Runs []struct {
						Text string `json:"text"`
					} `json:"runs"`
				} `json:"name"`
			} `json:"captionTracks"`
		} `json:"playerCaptionsTracklistRenderer"`
	} `json:"captions"`
}

// Причины из playabilityStatus.reason. В «you’re» — типографский апостроф.
const (
	reasonBot         = "Sign in to confirm you’re not a bot"
	reasonAge         = "This video may be inappropriate for some users."
	reasonUnavailable = "This video is unavailable"
)

func (c *Client) player(ctx context.Context, session *http.Client, key, videoID string) ([]Track, error) {
	body, _ := json.Marshal(map[string]any{
		"context": map[string]any{"client": map[string]string{
			"clientName": "ANDROID", "clientVersion": c.clientVersion,
		}},
		"videoId": videoID,
	})
	q := url.Values{"prettyPrint": {"false"}}
	if key != "" {
		q.Set("key", key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/youtubei/v1/player?"+q.Encode(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	raw, err := c.do(session, req)
	if err != nil {
		return nil, err
	}
	var resp playerResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("ответ плеера: %w", err)
	}

	switch ps := resp.PlayabilityStatus; ps.Status {
	case "OK", "":
	case "LOGIN_REQUIRED":
		switch ps.Reason {
		case reasonBot:
			return nil, ErrBlocked
		case reasonAge:
			return nil, ErrAgeRestricted
		}
		return nil, fmt.Errorf("плеер требует вход: %s", ps.Reason)
	case "ERROR":
		if ps.Reason == reasonUnavailable {
			return nil, ErrUnavailable
		}
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, ps.Reason)
	default:
		// UNPLAYABLE, LIVE_STREAM_OFFLINE и прочее: видео есть, но не играет.
		return nil, fmt.Errorf("%w: %s %s", ErrUnavailable, ps.Status, ps.Reason)
	}

	r := resp.Captions.Renderer
	if r == nil || len(r.CaptionTracks) == 0 {
		return nil, ErrNoTranscript
	}
	tracks := make([]Track, 0, len(r.CaptionTracks))
	for _, t := range r.CaptionTracks {
		name := t.LanguageCode
		if len(t.Name.Runs) > 0 {
			name = t.Name.Runs[0].Text
		}
		tracks = append(tracks, Track{
			Lang: t.LanguageCode,
			Name: name,
			Auto: t.Kind == "asr",
			// srv3 — формат с разметкой по словам; без fmt приходит простой
			// <transcript><text>.
			URL: strings.Replace(t.BaseURL, "&fmt=srv3", "", 1),
		})
	}
	return tracks, nil
}

// pickTrack выбирает дорожку на языке речи. Язык речи виден по
// автоматической дорожке (YouTube распознаёт только исходную речь), а ручные
// дорожки бывают и переводами. Поэтому: ручная на языке автоматической →
// сама автоматическая → ручная из langs → первая ручная.
func pickTrack(tracks []Track, langs []string) Track {
	var auto *Track
	for i := range tracks {
		if tracks[i].Auto {
			auto = &tracks[i]
			break
		}
	}
	if auto != nil {
		for _, t := range tracks {
			if !t.Auto && sameLang(t.Lang, auto.Lang) {
				return t
			}
		}
		return *auto
	}
	for _, want := range langs {
		for _, t := range tracks {
			if sameLang(t.Lang, want) {
				return t
			}
		}
	}
	return tracks[0]
}

// sameLang сравнивает коды без региона: «en» и «en-GB» — один язык.
func sameLang(a, b string) bool {
	a, _, _ = strings.Cut(strings.ToLower(a), "-")
	b, _, _ = strings.Cut(strings.ToLower(b), "-")
	return a == b
}

func (c *Client) download(ctx context.Context, session *http.Client, t Track) ([]Line, error) {
	if needsPoToken(t.URL) {
		return nil, ErrPoToken
	}
	raw, err := c.get(ctx, session, t.URL)
	if err != nil {
		return nil, err
	}
	// Пустой ответ 200 — так YouTube отказывает без PO-токена.
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, ErrPoToken
	}
	return Parse(raw)
}

// needsPoToken — в адресе дорожки эксперимент xpe или xpv: без PO-токена
// YouTube отдаст пустой ответ (так же проверяет yt-dlp 2026.08).
func needsPoToken(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	for _, v := range u.Query()["exp"] {
		for _, e := range strings.Split(v, ",") {
			if e == "xpe" || e == "xpv" {
				return true
			}
		}
	}
	return false
}

// Parse разбирает XML дорожки. Текст внутри уже экранирован дважды
// (&amp;#39;): XML снимает один слой, html.UnescapeString — второй.
func Parse(raw []byte) ([]Line, error) {
	var doc struct {
		Texts []struct {
			Start string `xml:"start,attr"`
			Dur   string `xml:"dur,attr"`
			Text  string `xml:",chardata"`
		} `xml:"text"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("XML субтитров: %w", err)
	}
	lines := make([]Line, 0, len(doc.Texts))
	for _, t := range doc.Texts {
		text := cleanText(t.Text)
		if text == "" {
			continue
		}
		start, _ := strconv.ParseFloat(t.Start, 64)
		dur, _ := strconv.ParseFloat(t.Dur, 64)
		lines = append(lines, Line{Start: start, Dur: dur, Text: text})
	}
	return lines, nil
}

var reTag = regexp.MustCompile(`<[^>]*>`)

// cleanText снимает экранирование и теги. «>>» — так автоматические
// субтитры отмечают смену говорящего; в тексте это тире реплики.
func cleanText(s string) string {
	s = html.UnescapeString(s)
	s = reTag.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, ">>", "—")
	return strings.Join(strings.Fields(s), " ")
}

func (c *Client) get(ctx context.Context, session *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// Страница видео без браузерного User-Agent бывает урезанной.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")
	return c.do(session, req)
}

// do выполняет запрос; 429 — блокировка, прочие не-2xx — ошибка с кодом.
func (c *Client) do(session *http.Client, req *http.Request) ([]byte, error) {
	req.Header.Set("Accept-Language", "en-US")
	resp, err := session.Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, transportError(err)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrBlocked
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("YouTube ответил %s на %s", resp.Status, req.URL.Path)
	}
	return body, nil
}

// transportError — запрос не дошёл или оборвался. Отмена извне остаётся
// отменой, остальное (DNS, соединение, таймаут) — ErrNetwork.
func transportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return redact(err)
	}
	return fmt.Errorf("%w: %v", ErrNetwork, redact(err))
}

// redact убирает адрес запроса из сетевой ошибки: в нём ключ и подписи.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %s: %w", ue.Op, strings.SplitN(ue.URL, "?", 2)[0], ue.Err)
	}
	return err
}
