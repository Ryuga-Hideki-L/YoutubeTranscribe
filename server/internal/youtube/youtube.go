// Package youtube — клиент YouTube Data API v3. Он находит канал и
// перечисляет его видео. Субтитры этим API не скачать, потому что
// captions.download работает только для владельца канала. Их берёт пакет
// transcript.
//
// Ключ уходит заголовком X-Goog-Api-Key, а не параметром ?key=. Так он не
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
	// ErrQuota — дневная квота ключа исчерпана. Квота сбрасывается в полночь
	// по Тихоокеанскому времени.
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

// Client обращается к API. С пустым ключом каждый вызов вернёт ErrNoKey.
type Client struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
}

// New создаёт клиента. Пустой baseURL означает настоящий API.
func New(apiKey string, httpClient *http.Client, baseURL string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	if baseURL == "" {
		baseURL = "https://www.googleapis.com/youtube/v3"
	}
	return &Client{apiKey: apiKey, httpClient: httpClient, baseURL: strings.TrimSuffix(baseURL, "/")}
}

// HasKey сообщает, задан ли ключ.
func (client *Client) HasKey() bool { return client.apiKey != "" }

// Query — ввод человека, разобранный по виду.
type Query struct {
	Kind  string // handle, id, username, video, search
	Value string
}

var (
	reChannelID = regexp.MustCompile(`^UC[0-9A-Za-z_-]{22}$`)
	reVideoID   = regexp.MustCompile(`^[0-9A-Za-z_-]{11}$`)
	reHandle    = regexp.MustCompile(`^@[\p{L}\p{N}._·-]{3,30}$`)
)

// ParseQuery понимает @имя, ID канала UC…, ссылки на канал, на видео и вида
// /user/…. Всё остальное считается поиском по названию.
func ParseQuery(input string) Query {
	input = strings.TrimSpace(input)
	switch {
	case reHandle.MatchString(input):
		return Query{"handle", input}
	case reChannelID.MatchString(input):
		return Query{"id", input}
	}
	if pageURL := parseYouTubeURL(input); pageURL != nil {
		segments := strings.Split(strings.Trim(pageURL.Path, "/"), "/")
		switch {
		case pageURL.Host == "youtu.be" && reVideoID.MatchString(segments[0]):
			return Query{"video", segments[0]}
		case segments[0] == "watch" && reVideoID.MatchString(pageURL.Query().Get("v")):
			return Query{"video", pageURL.Query().Get("v")}
		case (segments[0] == "shorts" || segments[0] == "live" || segments[0] == "embed") && len(segments) > 1 && reVideoID.MatchString(segments[1]):
			return Query{"video", segments[1]}
		case strings.HasPrefix(segments[0], "@"):
			if handle, err := url.PathUnescape(segments[0]); err == nil {
				return Query{"handle", handle}
			}
		case segments[0] == "channel" && len(segments) > 1 && reChannelID.MatchString(segments[1]):
			return Query{"id", segments[1]}
		case segments[0] == "user" && len(segments) > 1:
			return Query{"username", segments[1]}
		case segments[0] == "c" && len(segments) > 1:
			// API не разрешает старые адреса /c/имя, их можно найти только поиском.
			name, _ := url.PathUnescape(segments[1])
			return Query{"search", name}
		}
	}
	return Query{"search", input}
}

func parseYouTubeURL(input string) *url.URL {
	if !strings.Contains(input, "://") {
		input = "https://" + input
	}
	parsed, err := url.Parse(input)
	if err != nil {
		return nil
	}
	parsed.Host = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(parsed.Host), "www."), "m.")
	if parsed.Host != "youtube.com" && parsed.Host != "youtu.be" && parsed.Host != "music.youtube.com" {
		return nil
	}
	return parsed
}

// channelRefs — ответ videos или search, из которого нужен только ID канала.
type channelRefs struct {
	Items []struct {
		Snippet struct {
			ChannelID string `json:"channelId"`
		} `json:"snippet"`
	} `json:"items"`
}

// Resolve находит каналы по вводу человека. Для @имени, ссылки или ID это
// один канал, он стоит 1–2 единицы квоты. Для свободного текста это до 5
// кандидатов поиском, он стоит 100 единиц. Поэтому поиск включается только по
// явной просьбе.
func (client *Client) Resolve(ctx context.Context, input string) ([]Channel, error) {
	query := ParseQuery(input)
	params := url.Values{}
	switch query.Kind {
	case "handle":
		params.Set("forHandle", query.Value)
	case "id":
		params.Set("id", query.Value)
	case "username":
		params.Set("forUsername", query.Value)
	case "video":
		var resp channelRefs
		if err := client.get(ctx, "videos", url.Values{"part": {"snippet"}, "id": {query.Value}}, &resp); err != nil {
			return nil, err
		}
		if len(resp.Items) == 0 {
			return nil, ErrNotFound
		}
		params.Set("id", resp.Items[0].Snippet.ChannelID)
	case "search":
		if query.Value == "" {
			return nil, ErrNotFound
		}
		var resp channelRefs
		err := client.get(ctx, "search", url.Values{
			"part": {"snippet"}, "type": {"channel"}, "maxResults": {"5"}, "q": {query.Value},
		}, &resp)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(resp.Items))
		for _, item := range resp.Items {
			ids = append(ids, item.Snippet.ChannelID)
		}
		if len(ids) == 0 {
			return nil, ErrNotFound
		}
		params.Set("id", strings.Join(ids, ","))
	}
	return client.channels(ctx, params)
}

// Channel возвращает канал по ID.
func (client *Client) Channel(ctx context.Context, id string) (Channel, error) {
	channels, err := client.channels(ctx, url.Values{"id": {id}})
	if err != nil {
		return Channel{}, err
	}
	return channels[0], nil
}

func (client *Client) channels(ctx context.Context, params url.Values) ([]Channel, error) {
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
	if err := client.get(ctx, "channels", params, &resp); err != nil {
		return nil, err
	}
	if len(resp.Items) == 0 {
		return nil, ErrNotFound
	}
	result := make([]Channel, 0, len(resp.Items))
	for _, item := range resp.Items {
		videoCount, _ := strconv.Atoi(item.Statistics.VideoCount)
		result = append(result, Channel{
			ID:         item.ID,
			Title:      item.Snippet.Title,
			Handle:     item.Snippet.CustomURL,
			Thumb:      item.Snippet.Thumbnails.best(),
			Uploads:    item.ContentDetails.RelatedPlaylists.Uploads,
			VideoCount: videoCount,
		})
	}
	return result, nil
}

type thumbnails map[string]struct {
	URL string `json:"url"`
}

// best возвращает адрес первой найденной картинки в порядке medium, high, default.
func (thumbs thumbnails) best() string {
	for _, size := range []string{"medium", "high", "default"} {
		if thumb, ok := thumbs[size]; ok {
			return thumb.URL
		}
	}
	return ""
}

// Uploads возвращает до limit последних видео плейлиста загрузок. Трансляции
// и премьеры, которые ещё не прошли, пропускаются, потому что у них нет
// субтитров. Квота: 1 единица на 50 видео плейлиста и ещё 1 на 50 видео для
// подробностей.
func (client *Client) Uploads(ctx context.Context, playlist string, limit int) ([]Video, error) {
	var videos []Video
	pageToken := ""
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
		if pageToken != "" {
			params.Set("pageToken", pageToken)
		}
		if err := client.get(ctx, "playlistItems", params, &resp); err != nil {
			return nil, err
		}
		batch := make([]Video, 0, len(resp.Items))
		for _, item := range resp.Items {
			// Приватные и удалённые видео остаются в плейлисте без даты.
			if item.ContentDetails.VideoPublishedAt == "" || item.Status.PrivacyStatus == "private" {
				continue
			}
			published, _ := time.Parse(time.RFC3339, item.ContentDetails.VideoPublishedAt)
			batch = append(batch, Video{ID: item.ContentDetails.VideoID, Title: item.Snippet.Title, Published: published})
		}
		if err := client.details(ctx, batch); err != nil {
			return nil, err
		}
		for _, video := range batch {
			if video.Live == "none" && len(videos) < limit {
				videos = append(videos, video)
			}
		}
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}
	return videos, nil
}

// details дописывает длительность и признак трансляции. За один вызов
// принимает не больше 50 видео.
func (client *Client) details(ctx context.Context, videos []Video) error {
	if len(videos) == 0 {
		return nil
	}
	ids := make([]string, len(videos))
	for i, video := range videos {
		ids[i] = video.ID
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
	err := client.get(ctx, "videos", url.Values{"part": {"snippet,contentDetails"}, "id": {strings.Join(ids, ",")}}, &resp)
	if err != nil {
		return err
	}
	indexByID := map[string]int{}
	for i, video := range videos {
		indexByID[video.ID] = i
	}
	for _, item := range resp.Items {
		if i, ok := indexByID[item.ID]; ok {
			videos[i].Live = item.Snippet.LiveBroadcastContent
			videos[i].Duration = parseDuration(item.ContentDetails.Duration)
		}
	}
	return nil
}

var reDuration = regexp.MustCompile(`^P(?:(\d+)D)?T?(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?$`)

// parseDuration переводит длительность ISO 8601 (PT1H2M3S) в секунды.
func parseDuration(iso string) int {
	match := reDuration.FindStringSubmatch(iso)
	if match == nil {
		return 0
	}
	total := 0
	for i, multiplier := range []int{86400, 3600, 60, 1} {
		amount, _ := strconv.Atoi(match[i+1])
		total += amount * multiplier
	}
	return total
}

// apiError — тело ошибки, которое присылает Google.
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

func (client *Client) get(ctx context.Context, endpoint string, params url.Values, result any) error {
	if client.apiKey == "" {
		return ErrNoKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+"/"+endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Goog-Api-Key", client.apiKey)
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("YouTube Data API: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("YouTube Data API: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var apiErr apiError
		_ = json.Unmarshal(body, &apiErr)
		for _, detail := range apiErr.Error.Details {
			if detail.Reason == "API_KEY_INVALID" {
				return ErrBadKey
			}
		}
		for _, entry := range apiErr.Error.Errors {
			switch entry.Reason {
			case "quotaExceeded", "dailyLimitExceeded", "rateLimitExceeded":
				return ErrQuota
			case "keyInvalid", "accessNotConfigured", "keyExpired":
				return fmt.Errorf("%w: %s", ErrBadKey, apiErr.Error.Message)
			case "channelNotFound", "playlistNotFound":
				return ErrNotFound
			}
		}
		if apiErr.Error.Message != "" {
			return fmt.Errorf("YouTube Data API %s: %s", endpoint, apiErr.Error.Message)
		}
		return fmt.Errorf("YouTube Data API %s: %s", endpoint, resp.Status)
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("YouTube Data API %s: %w", endpoint, err)
	}
	return nil
}
