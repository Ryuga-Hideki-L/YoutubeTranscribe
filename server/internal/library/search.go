package library

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Сколько отдаём за один поиск.
const (
	MaxVideos       = 60
	MaxHitsPerVideo = 50
	MaxHitLines     = 6 // строк в одном совпадении вместе с соседними
)

// Mark — подсвеченный кусок строки. Начало и конец считаются в символах.
type Mark [2]int

// HitLine — строка субтитров в совпадении. Строка с фразой несёт подсветку,
// соседние строки идут без неё.
type HitLine struct {
	Start float64 `json:"s"`
	Text  string  `json:"t"`
	Marks []Mark  `json:"m,omitempty"`
}

// Hit — одно совпадение: строки с фразой и по строке до и после них.
type Hit struct {
	Start float64   `json:"s"` // когда начинается первая строка с фразой
	Lines []HitLine `json:"lines"`
	first int       // номер первой строки совпадения в субтитрах видео
}

// VideoHits — все совпадения в одном видео.
type VideoHits struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Published    time.Time `json:"published"`
	Duration     int       `json:"duration,omitempty"`
	ChannelID    string    `json:"channelId"`
	ChannelTitle string    `json:"channelTitle"`
	Total        int       `json:"total"` // сколько совпадений нашлось
	Hits         []Hit     `json:"hits"`  // отдаём не больше MaxHitsPerVideo
}

// Result — ответ поиска.
type Result struct {
	Query       string      `json:"query"`
	TotalHits   int         `json:"totalHits"`
	TotalVideos int         `json:"totalVideos"`
	Videos      []VideoHits `json:"videos"` // не больше MaxVideos
}

// searchEntry — видео, готовое к поиску. Все его строки склеены в один
// текст через пробел, поэтому находится и фраза, разорванная между строками.
type searchEntry struct {
	video      *Video
	channelID  string
	text       string // строки в нижнем регистре, ё заменена на е
	lineStarts []int  // где в text начинается каждая строка, в байтах
}

// searchSnapshot — всё, по чему ищем. Его не меняют, а заменяют целиком,
// поэтому поиск читает его без замка.
type searchSnapshot struct {
	entries       []*searchEntry
	channelTitles map[string]string
}

// normalize переводит текст в нижний регистр и меняет ё на е. Число символов
// не меняется, поэтому позиции подсветки подходят и к исходной строке.
func normalize(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	for _, char := range text {
		char = unicode.ToLower(char)
		if char == 'ё' {
			char = 'е'
		}
		out.WriteRune(char)
	}
	return out.String()
}

func newSearchEntry(channelID string, video *Video) *searchEntry {
	entry := &searchEntry{video: video, channelID: channelID, lineStarts: make([]int, len(video.Lines))}
	var text strings.Builder
	for i, line := range video.Lines {
		if i > 0 {
			text.WriteByte(' ')
		}
		entry.lineStarts[i] = text.Len()
		text.WriteString(normalize(line.Text))
	}
	entry.text = text.String()
	return entry
}

func (lib *Library) reindex() {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	lib.reindexLocked()
}

// reindexLocked собирает новый снимок для поиска. Готовые записи берутся из
// кэша: запись видео после создания не меняется, её только заменяют копией.
func (lib *Library) reindexLocked() {
	snapshot := &searchSnapshot{channelTitles: map[string]string{}}
	entries := make(map[*Video]*searchEntry, len(lib.entries))
	for id, channel := range lib.channels {
		snapshot.channelTitles[id] = channel.Channel.Title
		for _, video := range channel.Videos {
			if video.Status != StatusOK || len(video.Lines) == 0 {
				continue
			}
			entry, ok := lib.entries[video]
			if !ok {
				entry = newSearchEntry(id, video)
			}
			entries[video] = entry
			snapshot.entries = append(snapshot.entries, entry)
		}
	}
	lib.entries = entries
	lib.snapshot.Store(snapshot)
}

// Search ищет фразу во всех каналах или только в channelID. Регистр, разница
// между е и ё и лишние пробелы не важны.
func (lib *Library) Search(query, channelID string) Result {
	phrase := normalize(strings.Join(strings.Fields(query), " "))
	result := Result{Query: query, Videos: []VideoHits{}}
	snapshot := lib.snapshot.Load()
	if phrase == "" || snapshot == nil {
		return result
	}
	for _, entry := range snapshot.entries {
		if channelID != "" && entry.channelID != channelID {
			continue
		}
		total, hits := entry.find(phrase)
		if total == 0 {
			continue
		}
		result.TotalHits += total
		result.TotalVideos++
		video := entry.video
		result.Videos = append(result.Videos, VideoHits{
			ID: video.ID, Title: video.Title, Published: video.Published, Duration: video.Duration,
			ChannelID: entry.channelID, ChannelTitle: snapshot.channelTitles[entry.channelID],
			Total: total, Hits: hits,
		})
	}
	// Сначала видео, где совпадений больше, при равенстве — свежие.
	sort.Slice(result.Videos, func(i, j int) bool {
		left, right := result.Videos[i], result.Videos[j]
		if left.Total != right.Total {
			return left.Total > right.Total
		}
		return left.Published.After(right.Published)
	})
	if len(result.Videos) > MaxVideos {
		result.Videos = result.Videos[:MaxVideos]
	}
	return result
}

// find возвращает число совпадений и первые MaxHitsPerVideo из них.
// Вхождения на соседних строках или через строку склеиваются в одно
// совпадение до MaxHitLines строк. Иначе соседние строки повторялись бы.
func (entry *searchEntry) find(phrase string) (int, []Hit) {
	var hits []Hit
	total := 0
	lastMatchLine := -2 // последняя строка с фразой в текущем совпадении
	lastShown := false  // попало ли текущее совпадение в ответ
	lastLine := len(entry.video.Lines) - 1
	for from := 0; ; {
		found := strings.Index(entry.text[from:], phrase)
		if found < 0 {
			break
		}
		matchStart := from + found
		matchEnd := matchStart + len(phrase)
		from = matchStart + 1
		firstLine, endLine := entry.lineOf(matchStart), entry.lineOf(matchEnd-1)

		if total > 0 && firstLine <= lastMatchLine+2 {
			if !lastShown {
				lastMatchLine = max(lastMatchLine, endLine)
				continue
			}
			hit := &hits[len(hits)-1]
			if upTo := min(endLine+1, lastLine); upTo-hit.first+1 <= MaxHitLines {
				for line := hit.first + len(hit.Lines); line <= upTo; line++ {
					hit.Lines = append(hit.Lines, entry.hitLine(line))
				}
				entry.addMarks(hit, matchStart, matchEnd)
				lastMatchLine = max(lastMatchLine, endLine)
				continue
			}
		}

		lastMatchLine = endLine
		total++
		if lastShown = len(hits) < MaxHitsPerVideo; !lastShown {
			continue
		}
		hit := Hit{Start: entry.video.Lines[firstLine].Start, first: max(firstLine-1, 0)}
		for line := hit.first; line <= min(endLine+1, lastLine); line++ {
			hit.Lines = append(hit.Lines, entry.hitLine(line))
		}
		entry.addMarks(&hit, matchStart, matchEnd)
		hits = append(hits, hit)
	}
	return total, hits
}

func (entry *searchEntry) hitLine(line int) HitLine {
	return HitLine{Start: entry.video.Lines[line].Start, Text: entry.video.Lines[line].Text}
}

// lineOf — номер строки, в которую попадает байт offset склеенного текста.
func (entry *searchEntry) lineOf(offset int) int {
	return sort.Search(len(entry.lineStarts), func(i int) bool { return entry.lineStarts[i] > offset }) - 1
}

// lineEnd — где кончается строка, без пробела после неё.
func (entry *searchEntry) lineEnd(line int) int {
	if line+1 < len(entry.lineStarts) {
		return entry.lineStarts[line+1] - 1
	}
	return len(entry.text)
}

// addMarks подсвечивает найденную фразу во всех строках совпадения, которых
// она касается. matchStart и matchEnd — байты склеенного текста.
func (entry *searchEntry) addMarks(hit *Hit, matchStart, matchEnd int) {
	for i := range hit.Lines {
		line := hit.first + i
		lineStart, lineEnd := entry.lineStarts[line], entry.lineEnd(line)
		from, to := max(matchStart, lineStart), min(matchEnd, lineEnd)
		if from >= to {
			continue
		}
		hit.Lines[i].Marks = append(hit.Lines[i].Marks, Mark{
			utf8.RuneCountInString(entry.text[lineStart:from]),
			utf8.RuneCountInString(entry.text[lineStart:to]),
		})
	}
}
