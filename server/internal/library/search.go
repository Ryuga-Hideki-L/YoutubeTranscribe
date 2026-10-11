package library

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Пределы ответа: сколько видео и совпадений в видео отдаётся за раз.
const (
	MaxVideos       = 60
	MaxHitsPerVideo = 50
)

// Mark — подсвеченный кусок строки: [от, до) в символах (кодовых точках).
type Mark [2]int

// HitLine — строка субтитров в совпадении: с подсветкой или контекст.
type HitLine struct {
	S     float64 `json:"s"`
	T     string  `json:"t"`
	Marks []Mark  `json:"m,omitempty"`
}

// Hit — одно совпадение: строки с фразой и по строке до и после.
type Hit struct {
	S     float64   `json:"s"` // начало первой строки с фразой
	Lines []HitLine `json:"lines"`
	from  int       // номер строки Lines[0] в субтитрах видео
}

// VideoHits — совпадения в одном видео.
type VideoHits struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Published    time.Time `json:"published"`
	Duration     int       `json:"duration,omitempty"`
	ChannelID    string    `json:"channelId"`
	ChannelTitle string    `json:"channelTitle"`
	Total        int       `json:"total"` // всего совпадений в видео
	Hits         []Hit     `json:"hits"`  // не больше MaxHitsPerVideo
}

// Result — ответ поиска.
type Result struct {
	Query       string      `json:"query"`
	TotalHits   int         `json:"totalHits"`
	TotalVideos int         `json:"totalVideos"`
	Videos      []VideoHits `json:"videos"` // не больше MaxVideos
}

// videoIndex — видео, подготовленное к поиску: все строки одной строкой в
// нижнем регистре через пробел и начала строк в ней (в байтах). Так фраза
// находится, даже если субтитры разорвали её между строками.
type videoIndex struct {
	v         *Video
	channelID string
	norm      string
	offsets   []int
}

type index struct {
	videos        []*videoIndex
	channelTitles map[string]string
}

// normalize приводит текст к виду для поиска, не меняя число символов:
// нижний регистр и «ё» как «е». Поэтому позиции в символах у исходной
// строки и у нормализованной совпадают.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		r = unicode.ToLower(r)
		if r == 'ё' {
			r = 'е'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func buildVideoIndex(channelID string, v *Video) *videoIndex {
	vi := &videoIndex{v: v, channelID: channelID, offsets: make([]int, len(v.Lines))}
	var b strings.Builder
	for i, line := range v.Lines {
		if i > 0 {
			b.WriteByte(' ')
		}
		vi.offsets[i] = b.Len()
		b.WriteString(normalize(line.Text))
	}
	vi.norm = b.String()
	return vi
}

func (l *Library) reindex() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reindexLocked()
}

// reindexLocked собирает новый снимок для поиска. Записи видео не меняются
// после создания (SetResult и Merge заменяют их копиями), поэтому готовые
// videoIndex берутся из кэша по указателю на запись.
func (l *Library) reindexLocked() {
	idx := &index{channelTitles: map[string]string{}}
	cache := make(map[*Video]*videoIndex, len(l.cache))
	for id, ch := range l.channels {
		idx.channelTitles[id] = ch.Channel.Title
		for _, v := range ch.Videos {
			if v.Status != StatusOK || len(v.Lines) == 0 {
				continue
			}
			vi, ok := l.cache[v]
			if !ok {
				vi = buildVideoIndex(id, v)
			}
			cache[v] = vi
			idx.videos = append(idx.videos, vi)
		}
	}
	l.cache = cache
	l.index.Store(idx)
}

// Search ищет фразу во всех каналах или в одном (channelID). Без учёта
// регистра и разницы «е»/«ё»; пробелы внутри фразы — любые.
func (l *Library) Search(query, channelID string) Result {
	q := normalize(strings.Join(strings.Fields(query), " "))
	res := Result{Query: query, Videos: []VideoHits{}}
	idx := l.index.Load()
	if q == "" || idx == nil {
		return res
	}
	for _, vi := range idx.videos {
		if channelID != "" && vi.channelID != channelID {
			continue
		}
		total, hits := vi.find(q)
		if total == 0 {
			continue
		}
		res.TotalHits += total
		res.TotalVideos++
		res.Videos = append(res.Videos, VideoHits{
			ID: vi.v.ID, Title: vi.v.Title, Published: vi.v.Published, Duration: vi.v.Duration,
			ChannelID: vi.channelID, ChannelTitle: idx.channelTitles[vi.channelID],
			Total: total, Hits: hits,
		})
	}
	sort.Slice(res.Videos, func(i, j int) bool {
		a, b := res.Videos[i], res.Videos[j]
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.Published.After(b.Published)
	})
	if len(res.Videos) > MaxVideos {
		res.Videos = res.Videos[:MaxVideos]
	}
	return res
}

// find возвращает число совпадений и первые MaxHitsPerVideo из них.
// Несколько вхождений в одной строке — одно совпадение с несколькими
// подсветками.
func (vi *videoIndex) find(q string) (int, []Hit) {
	var hits []Hit
	total := 0
	lastStart, lastKept := -1, false
	for pos := 0; ; {
		i := strings.Index(vi.norm[pos:], q)
		if i < 0 {
			break
		}
		a := pos + i
		b := a + len(q)
		pos = a + 1
		first, last := vi.lineAt(a), vi.lineAt(b-1)
		if first == lastStart {
			if lastKept {
				vi.addMarks(&hits[len(hits)-1], a, b)
			}
			continue
		}
		lastStart = first
		total++
		if lastKept = len(hits) < MaxHitsPerVideo; !lastKept {
			continue
		}
		hit := Hit{S: vi.v.Lines[first].Start, from: max(first-1, 0)}
		for k := hit.from; k <= min(last+1, len(vi.v.Lines)-1); k++ {
			hit.Lines = append(hit.Lines, HitLine{S: vi.v.Lines[k].Start, T: vi.v.Lines[k].Text})
		}
		vi.addMarks(&hit, a, b)
		hits = append(hits, hit)
	}
	return total, hits
}

// lineAt — номер строки, в которую попадает байт pos нормализованного текста.
func (vi *videoIndex) lineAt(pos int) int {
	return sort.Search(len(vi.offsets), func(i int) bool { return vi.offsets[i] > pos }) - 1
}

// lineEnd — конец строки k в нормализованном тексте (без пробела-разделителя).
func (vi *videoIndex) lineEnd(k int) int {
	if k+1 < len(vi.offsets) {
		return vi.offsets[k+1] - 1
	}
	return len(vi.norm)
}

// addMarks подсвечивает байты [a, b) нормализованного текста в строках hit.
func (vi *videoIndex) addMarks(hit *Hit, a, b int) {
	for j := range hit.Lines {
		k := hit.from + j
		start, end := vi.offsets[k], vi.lineEnd(k)
		from, to := max(a, start), min(b, end)
		if from >= to {
			continue
		}
		hit.Lines[j].Marks = append(hit.Lines[j].Marks, Mark{
			utf8.RuneCountInString(vi.norm[start:from]),
			utf8.RuneCountInString(vi.norm[start:to]),
		})
	}
}
