// Package youtube — клиент YouTube Data API v3: найти канал и перечислить
// его видео. Субтитры этим API не скачать (captions.download только для
// владельца канала) — их берёт пакет transcript.
//
// Ключ уходит заголовком X-Goog-Api-Key, а не параметром ?key=: так он не
// попадает ни в текст сетевых ошибок, ни в журналы.
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrNoKey — ключ не задан.
	ErrNoKey = errors.New("не задан ключ YouTube Data API (YOUTUBE_API_KEY)")
	// ErrBadKey — Google не принял ключ.
	ErrBadKey = errors.New("ключ YouTube Data API не принят")
	// ErrQuota — дневная квота ключа исчерпана (сброс в полночь по Тихоокеанскому времени).
	ErrQuota = errors.New("квота YouTube Data API на сегодня исчерпана")
	// ErrNotFound — канал не найден.
	ErrNotFound = errors.New("канал не найден")
)

// Channel — канал YouTube.
type Channel struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Handle     string `json:"handle,omitempty"` // @name
	Thumb      string `json:"thumb,omitempty"`
	Uploads    string `json:"uploads"` // плейлист загрузок UU…
	VideoCount int    `json:"videoCount"`
}

// Video — видео из плейлиста загрузок.
type Video struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Published time.Time `json:"published"`
	Duration  int       `json:"duration,omitempty"` // секунды
	Live      string    `json:"-"`                  // none, live, upcoming
}

// Client обращается к API. Пустой ключ — каждый вызов вернёт ErrNoKey.
type Client struct {
	key  string
	http *http.Client
	base string
}

// New создаёт клиента; base пустой — настоящий API.
func New(key string, httpClient *http.Client, base string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	if base == "" {
		base = "https://www.googleapis.com/youtube/v3"
	}
	return &Client{key: key, http: httpClient, base: strings.TrimSuffix(base, "/")}
}

// HasKey — ключ задан.
func (c *Client) HasKey() bool { return c.key != "" }

// Query — что ввёл человек, разобранное по виду.
type Query struct {
	Kind  string // handle, id, username, video, search
	Value string
}

var (
	reChannelID = regexp.MustCompile(`^UC[0-9A-Za-z_-]{22}$`)
	reVideoID   = regexp.MustCompile(`^[0-9A-Za-z_-]{11}$`)
	reHandle    = regexp.MustCompile(`^@[\p{L}\p{N}._·-]{3,30}$`)
)

// ParseQuery понимает @имя, ID канала UC…, ссылки на канал, видео и
// /user/…; всё прочее — поиск по названию.
func ParseQuery(s string) Query {
	s = strings.TrimSpace(s)
	switch {
	case reHandle.MatchString(s):
		return Query{"handle", s}
	case reChannelID.MatchString(s):
		return Query{"id", s}
	}
	if u := parseYouTubeURL(s); u != nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		switch {
		case u.Host == "youtu.be" && reVideoID.MatchString(parts[0]):
			return Query{"video", parts[0]}
		case parts[0] == "watch" && reVideoID.MatchString(u.Query().Get("v")):
			return Query{"video", u.Query().Get("v")}
		case (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed") && len(parts) > 1 && reVideoID.MatchString(parts[1]):
			return Query{"video", parts[1]}
		case strings.HasPrefix(parts[0], "@"):
			if h, err := url.PathUnescape(parts[0]); err == nil {
				return Query{"handle", h}
			}
		case parts[0] == "channel" && len(parts) > 1 && reChannelID.MatchString(parts[1]):
			return Query{"id", parts[1]}
		case parts[0] == "user" && len(parts) > 1:
			return Query{"username", parts[1]}
		case parts[0] == "c" && len(parts) > 1:
			// Старые адреса /c/имя API не разрешает — только поиском.
			name, _ := url.PathUnescape(parts[1])
			return Query{"search", name}
		}
	}
	return Query{"search", s}
}

func parseYouTubeURL(s string) *url.URL {
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil
	}
	u.Host = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(u.Host), "www."), "m.")
	if u.Host != "youtube.com" && u.Host != "youtu.be" && u.Host != "music.youtube.com" {
		return nil
	}
	return u
}

// Resolve находит каналы по вводу человека. Для @имени, ссылки или ID —
// один канал (1–2 единицы квоты); для свободного текста — до 5 кандидатов
// поиском (100 единиц, поэтому поиск — только по явной просьбе).
func (c *Client) Resolve(ctx context.Context, input string) ([]Channel, error) {
	q := ParseQuery(input)
	params := url.Values{}
	switch q.Kind {
	case "handle":
		params.Set("forHandle", q.Value)
	case "id":
		params.Set("id", q.Value)
	case "username":
		params.Set("forUsername", q.Value)
	case "video":
		var resp struct {
			Items []struct {
				Snippet struct {
					ChannelID string `json:"channelId"`
				} `json:"snippet"`
			} `json:"items"`
		}
		if err := c.get(ctx, "videos", url.Values{"part": {"snippet"}, "id": {q.Value}}, &resp); err != nil {
			return nil, err
		}
		if len(resp.Items) == 0 {
			return nil, ErrNotFound
		}
		params.Set("id", resp.Items[0].Snippet.ChannelID)
	case "search":
		if q.Value == "" {
			return nil, ErrNotFound
		}
		var resp struct {
			Items []struct {
				Snippet struct {
					ChannelID string `json:"channelId"`
				} `json:"snippet"`
			} `json:"items"`
		}
		err := c.get(ctx, "search", url.Values{
			"part": {"snippet"}, "type": {"channel"}, "maxResults": {"5"}, "q": {q.Value},
		}, &resp)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(resp.Items))
		for _, it := range resp.Items {
			ids = append(ids, it.Snippet.ChannelID)
		}
		if len(ids) == 0 {
			return nil, ErrNotFound
		}
		params.Set("id", strings.Join(ids, ","))
	}
	return c.channels(ctx, params)
}

// Channel — канал по ID.
func (c *Client) Channel(ctx context.Context, id string) (Channel, error) {
	chs, err := c.channels(ctx, url.Values{"id": {id}})
	if err != nil {
		return Channel{}, err
	}
	return chs[0], nil
}

func (c *Client) channels(ctx context.Context, params url.Values) ([]Channel, error) {
	params.Set("part", "snippet,contentDetails,statistics")
	params.Set("maxResults", "50")
	var resp struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title      string     `json:"title"`
				CustomURL  string     `json:"customUrl"`
				Thumbnails thumbnails `json:"thumbnails"`
			} `json:"snippet"`
			ContentDetails struct {
				RelatedPlaylists struct {
					Uploads string `json:"uploads"`
				} `json:"relatedPlaylists"`
			} `json:"contentDetails"`
			Statistics struct {
				VideoCount string `json:"videoCount"`
			} `json:"statistics"`
		} `json:"items"`
	}
	if err := c.get(ctx, "channels", params, &resp); err != nil {
		return nil, err
	}
	if len(resp.Items) == 0 {
		return nil, ErrNotFound
	}
	out := make([]Channel, 0, len(resp.Items))
	for _, it := range resp.Items {
		n, _ := strconv.Atoi(it.Statistics.VideoCount)
		out = append(out, Channel{
			ID:         it.ID,
			Title:      it.Snippet.Title,
			Handle:     it.Snippet.CustomURL,
			Thumb:      it.Snippet.Thumbnails.best(),
			Uploads:    it.ContentDetails.RelatedPlaylists.Uploads,
			VideoCount: n,
		})
	}
	return out, nil
}

type thumbnails map[string]struct {
	URL string `json:"url"`
}

func (t thumbnails) best() string {
	for _, k := range []string{"medium", "high", "default"} {
		if v, ok := t[k]; ok {
			return v.URL
		}
	}
	return ""
}

// Uploads возвращает до limit последних видео плейлиста загрузок, уже без
// трансляций и премьер, которые ещё не прошли (у них нет субтитров).
// Квота: 1 единица на 50 видео плейлиста + 1 на 50 видео подробностей.
func (c *Client) Uploads(ctx context.Context, playlist string, limit int) ([]Video, error) {
	var videos []Video
	page := ""
	for len(videos) < limit {
		var resp struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				Snippet struct {
					Title string `json:"title"`
				} `json:"snippet"`
				ContentDetails struct {
					VideoID          string `json:"videoId"`
					VideoPublishedAt string `json:"videoPublishedAt"`
				} `json:"contentDetails"`
				Status struct {
					PrivacyStatus string `json:"privacyStatus"`
				} `json:"status"`
			} `json:"items"`
		}
		params := url.Values{
			"part": {"snippet,contentDetails,status"}, "playlistId": {playlist}, "maxResults": {"50"},
		}
		if page != "" {
			params.Set("pageToken", page)
		}
		if err := c.get(ctx, "playlistItems", params, &resp); err != nil {
			return nil, err
		}
		batch := make([]Video, 0, len(resp.Items))
		for _, it := range resp.Items {
			// Приватные и удалённые видео остаются в плейлисте без даты.
			if it.ContentDetails.VideoPublishedAt == "" || it.Status.PrivacyStatus == "private" {
				continue
			}
			published, _ := time.Parse(time.RFC3339, it.ContentDetails.VideoPublishedAt)
			batch = append(batch, Video{ID: it.ContentDetails.VideoID, Title: it.Snippet.Title, Published: published})
		}
		if err := c.details(ctx, batch); err != nil {
			return nil, err
		}
		for _, v := range batch {
			if v.Live == "none" && len(videos) < limit {
				videos = append(videos, v)
			}
		}
		page = resp.NextPageToken
		if page == "" {
			break
		}
	}
	return videos, nil
}

// details дописывает длительность и признак трансляции (до 50 видео).
func (c *Client) details(ctx context.Context, videos []Video) error {
	if len(videos) == 0 {
		return nil
	}
	ids := make([]string, len(videos))
	for i, v := range videos {
		ids[i] = v.ID
	}
	var resp struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				LiveBroadcastContent string `json:"liveBroadcastContent"`
			} `json:"snippet"`
			ContentDetails struct {
				Duration string `json:"duration"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	err := c.get(ctx, "videos", url.Values{"part": {"snippet,contentDetails"}, "id": {strings.Join(ids, ",")}}, &resp)
	if err != nil {
		return err
	}
	byID := map[string]int{}
	for i, v := range videos {
		byID[v.ID] = i
	}
	for _, it := range resp.Items {
		if i, ok := byID[it.ID]; ok {
			videos[i].Live = it.Snippet.LiveBroadcastContent
			videos[i].Duration = parseDuration(it.ContentDetails.Duration)
		}
	}
	return nil
}

var reDuration = regexp.MustCompile(`^P(?:(\d+)D)?T?(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?$`)

// parseDuration переводит ISO 8601 (PT1H2M3S) в секунды.
func parseDuration(s string) int {
	m := reDuration.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	total := 0
	for i, mul := range []int{86400, 3600, 60, 1} {
		n, _ := strconv.Atoi(m[i+1])
		total += n * mul
	}
	return total
}

type apiError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Details []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

func (c *Client) get(ctx context.Context, method string, params url.Values, out any) error {
	if c.key == "" {
		return ErrNoKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/"+method+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Goog-Api-Key", c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("YouTube Data API: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("YouTube Data API: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e apiError
		_ = json.Unmarshal(body, &e)
		for _, d := range e.Error.Details {
			if d.Reason == "API_KEY_INVALID" {
				return ErrBadKey
			}
		}
		for _, r := range e.Error.Errors {
			switch r.Reason {
			case "quotaExceeded", "dailyLimitExceeded", "rateLimitExceeded":
				return ErrQuota
			case "keyInvalid", "accessNotConfigured", "keyExpired":
				return fmt.Errorf("%w: %s", ErrBadKey, e.Error.Message)
			case "channelNotFound", "playlistNotFound":
				return ErrNotFound
			}
		}
		if e.Error.Message != "" {
			return fmt.Errorf("YouTube Data API %s: %s", method, e.Error.Message)
		}
		return fmt.Errorf("YouTube Data API %s: %s", method, resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("YouTube Data API %s: %w", method, err)
	}
	return nil
}
