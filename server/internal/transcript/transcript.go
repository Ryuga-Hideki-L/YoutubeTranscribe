// Package transcript скачивает субтитры видео YouTube без ключа API.
//
// Схема та же, что у python-библиотеки youtube-transcript-api 1.2.x.
// Запрос к плееру innertube (/youtubei/v1/player) от имени клиента Android возвращает список дорожек.
// Дорожка скачивается по её baseUrl в формате XML <transcript><text start dur>…</text>.
// Порядок запросов описан у Client.Tracks.
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
	// ErrBlocked — YouTube требует капчу или отвечает 429. С этого адреса
	// запросы временно не пускают, повторять сразу бесполезно.
	ErrBlocked = errors.New("YouTube временно блокирует запросы с этого адреса")
	// ErrNoTranscript — у видео нет ни одной дорожки субтитров.
	ErrNoTranscript = errors.New("у видео нет субтитров")
	// ErrUnavailable — видео удалено, скрыто или недоступно в регионе.
	ErrUnavailable = errors.New("видео недоступно")
	// ErrAgeRestricted — видео 18+. Без входа в аккаунт субтитры не отдаются.
	ErrAgeRestricted = errors.New("видео с возрастным ограничением")
	// ErrPoToken — дорожка требует PO-токен. Без браузера её не скачать.
	ErrPoToken = errors.New("YouTube требует PO-токен для этих субтитров")
	// ErrNetwork — запрос не дошёл из-за сети, DNS, прокси или таймаута.
	// С самим видео всё может быть в порядке, поэтому его надо повторить позже.
	ErrNetwork = errors.New("нет связи с YouTube")
)

// Line — одна строка субтитров. Start и Dur заданы в секундах.
type Line struct {
	Start float64 `json:"s"`
	Dur   float64 `json:"d"`
	Text  string  `json:"t"`
}

// Transcript — скачанные субтитры одного видео.
type Transcript struct {
	Lang  string `json:"lang"`
	Auto  bool   `json:"auto"` // распознаны автоматически (kind=asr)
	Lines []Line `json:"lines"`
}

// Track — дорожка субтитров из ответа плеера.
type Track struct {
	Lang string
	Name string
	Auto bool
	URL  string
}

// Client скачивает субтитры. Создавать его надо через New, нулевое значение не работает.
type Client struct {
	httpClient    *http.Client
	baseURL       string // https://www.youtube.com, в тестах адрес httptest
	clientVersion string
	langs         []string

	mu     sync.Mutex
	apiKey string
}

// Options настраивают клиента. Пустые поля берутся по умолчанию.
type Options struct {
	HTTP *http.Client
	// BaseURL — адрес YouTube. В тестах его подменяют.
	BaseURL string
	// ClientVersion — версия клиента Android, под которую мы выдаём себя плееру.
	ClientVersion string
	// Langs — желаемые языки ручных субтитров.
	// Нужны, если у видео нет автоматической дорожки и по ней не видно язык речи.
	Langs []string
}

// DefaultClientVersion — версия из youtube-transcript-api 1.2.4.
const DefaultClientVersion = "20.10.38"

// New создаёт клиента.
func New(options Options) *Client {
	client := &Client{
		httpClient:    options.HTTP,
		baseURL:       strings.TrimSuffix(options.BaseURL, "/"),
		clientVersion: options.ClientVersion,
		langs:         options.Langs,
	}
	if client.httpClient == nil {
		client.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if client.baseURL == "" {
		client.baseURL = "https://www.youtube.com"
	}
	if client.clientVersion == "" {
		client.clientVersion = DefaultClientVersion
	}
	return client
}

// Fetch скачивает субтитры видео на языке речи (см. pickTrack).
func (client *Client) Fetch(ctx context.Context, videoID string) (*Transcript, error) {
	tracks, session, err := client.Tracks(ctx, videoID)
	if err != nil {
		return nil, err
	}
	track := pickTrack(tracks, client.langs)
	lines, err := client.download(ctx, session, track)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, ErrNoTranscript
	}
	return &Transcript{Lang: track.Lang, Auto: track.Auto, Lines: lines}, nil
}

// Tracks возвращает дорожки субтитров видео и HTTP-сессию, в которой их надо скачивать.
// В сессии лежат cookie страницы видео, если она понадобилась.
//
// Сначала сразу спрашиваем плеер: это два запроса на видео.
// Ключ innertube клиенту Android не нужен, это проверяет TestLive в live_test.go.
// Если плеер ответил странно, открываем страницу видео.
// С неё берём ключ и cookie согласия для ЕС и спрашиваем плеер ещё раз.
// Ключ запоминается и дальше уходит уже в первом запросе.
func (client *Client) Tracks(ctx context.Context, videoID string) ([]Track, *http.Client, error) {
	tracks, err := client.player(ctx, client.httpClient, client.key(), videoID)
	if err == nil || isFinal(err) {
		return tracks, client.httpClient, err
	}
	// Плеер ответил странно или ключ устарел. Повторяем через страницу видео.
	session := client.session()
	key, err := client.watchPage(ctx, session, videoID)
	if err != nil {
		return nil, nil, err
	}
	client.setKey(key)
	tracks, err = client.player(ctx, session, key, videoID)
	return tracks, session, err
}

// isFinal проверяет ошибку, после которой страница видео ничего не изменит.
func isFinal(err error) bool {
	return errors.Is(err, ErrBlocked) || errors.Is(err, ErrNoTranscript) ||
		errors.Is(err, ErrUnavailable) || errors.Is(err, ErrAgeRestricted)
}

func (client *Client) key() string {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.apiKey
}

func (client *Client) setKey(key string) {
	client.mu.Lock()
	client.apiKey = key
	client.mu.Unlock()
}

// session возвращает HTTP-клиент с пустым хранилищем cookie.
// У каждого видео оно новое, как в youtube-transcript-api.
// Так за клиентом не тянется «отпечаток».
func (client *Client) session() *http.Client {
	jar, _ := cookiejar.New(nil) // без опций cookiejar.New не возвращает ошибку
	clone := *client.httpClient
	clone.Jar = jar
	return &clone
}

var (
	innertubeKeyPattern = regexp.MustCompile(`"INNERTUBE_API_KEY":\s*"([a-zA-Z0-9_-]+)"`)
	consentValuePattern = regexp.MustCompile(`name="v" value="(.*?)"`)
	consentFormMarker   = []byte(`action="https://consent.youtube.com/s"`)
	recaptchaMarker     = []byte(`class="g-recaptcha"`)
)

// maxResponseBodyBytes — больше этого из ответа не читаем.
const maxResponseBodyBytes = 16 << 20

// watchPage открывает страницу видео и достаёт из неё ключ innertube.
// Если YouTube просит согласие на cookie, сначала даёт его.
func (client *Client) watchPage(ctx context.Context, session *http.Client, videoID string) (string, error) {
	pageURL := client.baseURL + "/watch?v=" + url.QueryEscape(videoID)
	page, err := client.get(ctx, session, pageURL)
	if err != nil {
		return "", err
	}
	if bytes.Contains(page, consentFormMarker) {
		match := consentValuePattern.FindSubmatch(page)
		if match == nil {
			return "", errors.New("страница согласия YouTube без значения v")
		}
		base, _ := url.Parse(client.baseURL)
		session.Jar.SetCookies(base, []*http.Cookie{{
			Name: "CONSENT", Value: "YES+" + string(match[1]), Path: "/",
		}})
		if page, err = client.get(ctx, session, pageURL); err != nil {
			return "", err
		}
		if bytes.Contains(page, consentFormMarker) {
			return "", errors.New("YouTube не принял согласие на cookies")
		}
	}
	if bytes.Contains(page, recaptchaMarker) {
		return "", ErrBlocked
	}
	match := innertubeKeyPattern.FindSubmatch(page)
	if match == nil {
		return "", errors.New("на странице видео нет ключа innertube")
	}
	return string(match[1]), nil
}

// playerResponse — ответ плеера, только нужные поля.
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

// Тексты причин из playabilityStatus.reason.
// В слове «you’re» стоит типографский апостроф.
const (
	reasonBot         = "Sign in to confirm you’re not a bot"
	reasonAge         = "This video may be inappropriate for some users."
	reasonUnavailable = "This video is unavailable"
)

// player спрашивает плеер и возвращает дорожки субтитров видео.
func (client *Client) player(ctx context.Context, session *http.Client, key, videoID string) ([]Track, error) {
	requestBody, _ := json.Marshal(map[string]any{
		"context": map[string]any{"client": map[string]string{
			"clientName": "ANDROID", "clientVersion": client.clientVersion,
		}},
		"videoId": videoID,
	})
	query := url.Values{"prettyPrint": {"false"}}
	if key != "" {
		query.Set("key", key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.baseURL+"/youtubei/v1/player?"+query.Encode(), bytes.NewReader(requestBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	raw, err := client.do(session, req)
	if err != nil {
		return nil, err
	}
	var response playerResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("ответ плеера: %w", err)
	}

	switch playability := response.PlayabilityStatus; playability.Status {
	case "OK", "":
	case "LOGIN_REQUIRED":
		switch playability.Reason {
		case reasonBot:
			return nil, ErrBlocked
		case reasonAge:
			return nil, ErrAgeRestricted
		}
		return nil, fmt.Errorf("плеер требует вход: %s", playability.Reason)
	case "ERROR":
		if playability.Reason == reasonUnavailable {
			return nil, ErrUnavailable
		}
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, playability.Reason)
	default:
		// UNPLAYABLE, LIVE_STREAM_OFFLINE и другие статусы. Видео есть, но не играет.
		return nil, fmt.Errorf("%w: %s %s", ErrUnavailable, playability.Status, playability.Reason)
	}

	renderer := response.Captions.Renderer
	if renderer == nil || len(renderer.CaptionTracks) == 0 {
		return nil, ErrNoTranscript
	}
	tracks := make([]Track, 0, len(renderer.CaptionTracks))
	for _, captionTrack := range renderer.CaptionTracks {
		name := captionTrack.LanguageCode
		if len(captionTrack.Name.Runs) > 0 {
			name = captionTrack.Name.Runs[0].Text
		}
		tracks = append(tracks, Track{
			Lang: captionTrack.LanguageCode,
			Name: name,
			Auto: captionTrack.Kind == "asr",
			// Формат srv3 размечен по словам. Без параметра fmt приходит
			// простой <transcript><text>.
			URL: strings.Replace(captionTrack.BaseURL, "&fmt=srv3", "", 1),
		})
	}
	return tracks, nil
}

// pickTrack выбирает дорожку на языке речи.
// Язык речи виден по автоматической дорожке, потому что YouTube распознаёт только исходную речь.
// Ручные дорожки бывают и переводами, по ним язык речи не определить.
// Порядок выбора такой:
//  1. ручная дорожка на языке автоматической;
//  2. сама автоматическая;
//  3. ручная дорожка из langs;
//  4. первая ручная.
func pickTrack(tracks []Track, langs []string) Track {
	var autoTrack *Track
	for i := range tracks {
		if tracks[i].Auto {
			autoTrack = &tracks[i]
			break
		}
	}
	if autoTrack != nil {
		for _, track := range tracks {
			if !track.Auto && sameLang(track.Lang, autoTrack.Lang) {
				return track
			}
		}
		return *autoTrack
	}
	for _, wantLang := range langs {
		for _, track := range tracks {
			if sameLang(track.Lang, wantLang) {
				return track
			}
		}
	}
	return tracks[0]
}

// sameLang сравнивает коды без региона. «en» и «en-GB» — один язык.
func sameLang(lang, other string) bool {
	lang, _, _ = strings.Cut(strings.ToLower(lang), "-")
	other, _, _ = strings.Cut(strings.ToLower(other), "-")
	return lang == other
}

func (client *Client) download(ctx context.Context, session *http.Client, track Track) ([]Line, error) {
	if needsPoToken(track.URL) {
		return nil, ErrPoToken
	}
	raw, err := client.get(ctx, session, track.URL)
	if err != nil {
		return nil, err
	}
	// Пустой ответ с кодом 200 — так YouTube отказывает без PO-токена.
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, ErrPoToken
	}
	return Parse(raw)
}

// needsPoToken смотрит, есть ли в адресе дорожки эксперимент xpe или xpv.
// Тогда без PO-токена YouTube отдаст пустой ответ.
// Так же проверяет yt-dlp 2026.08.
func needsPoToken(trackURL string) bool {
	parsed, err := url.Parse(trackURL)
	if err != nil {
		return false
	}
	for _, value := range parsed.Query()["exp"] {
		for _, experiment := range strings.Split(value, ",") {
			if experiment == "xpe" || experiment == "xpv" {
				return true
			}
		}
	}
	return false
}

// Parse разбирает XML дорожки.
// Текст в нём экранирован дважды, например &amp;#39;.
// XML снимает один слой, html.UnescapeString второй.
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
	for _, node := range doc.Texts {
		text := cleanText(node.Text)
		if text == "" {
			continue
		}
		start, _ := strconv.ParseFloat(node.Start, 64)
		dur, _ := strconv.ParseFloat(node.Dur, 64)
		lines = append(lines, Line{Start: start, Dur: dur, Text: text})
	}
	return lines, nil
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

// cleanText снимает экранирование и теги.
// Автоматические субтитры отмечают смену говорящего знаком «>>».
// В тексте он становится тире реплики.
func cleanText(text string) string {
	text = html.UnescapeString(text)
	text = tagPattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, ">>", "—")
	return strings.Join(strings.Fields(text), " ")
}

func (client *Client) get(ctx context.Context, session *http.Client, requestURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	// Страница видео без браузерного User-Agent бывает урезанной.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")
	return client.do(session, req)
}

// do выполняет запрос.
// Код 429 значит блокировку, другие не-2xx дают ошибку с кодом.
func (client *Client) do(session *http.Client, req *http.Request) ([]byte, error) {
	req.Header.Set("Accept-Language", "en-US")
	resp, err := session.Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
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

// transportError оборачивает ошибку, когда запрос не дошёл или оборвался.
// Отмену извне оставляем отменой.
// Остальное, то есть DNS, соединение и таймаут, становится ErrNetwork.
func transportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return redact(err)
	}
	return fmt.Errorf("%w: %v", ErrNetwork, redact(err))
}

// redact убирает из адреса в сетевой ошибке всё после «?».
// Там ключ и подписи.
func redact(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s %s: %w", urlErr.Op, strings.SplitN(urlErr.URL, "?", 2)[0], urlErr.Err)
	}
	return err
}
