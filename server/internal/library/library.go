// Package library хранит каналы и субтитры их видео на диске и ищет по ним.
//
// Каждый канал — отдельный файл channels/<id>.json. Запись атомарная:
// временный файл, fsync, rename, так что сбой посреди записи оставляет
// прежнюю версию. Битый файл не затирается: он переименовывается в
// .corrupt-<время>, а канал пропускается. Писать в файлы может только
// Library (один владелец), все изменения идут через её методы.
package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/transcript"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/youtube"
)

// Version — версия формата файла канала.
const Version = 1

// Status — что получилось с субтитрами видео.
type Status string

const (
	StatusPending     Status = "pending"      // ещё не пробовали
	StatusOK          Status = "ok"           // субтитры скачаны
	StatusNoCaptions  Status = "no_captions"  // у видео нет субтитров
	StatusAge         Status = "age"          // 18+, без входа не отдают
	StatusUnavailable Status = "unavailable"  // недоступно (может быть временно: регион, Restricted Mode)
	StatusPoToken     Status = "pot_required" // нужен PO-токен
	StatusBlocked     Status = "blocked"      // YouTube блокировал запрос
	StatusError       Status = "error"        // прочая ошибка
)

// Final — повторять бесполезно: результат не изменится.
func (s Status) Final() bool {
	return s == StatusOK || s == StatusNoCaptions || s == StatusAge
}

// Video — видео канала и его субтитры.
type Video struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Published time.Time         `json:"published"`
	Duration  int               `json:"duration,omitempty"`
	Status    Status            `json:"status"`
	Error     string            `json:"error,omitempty"`
	Attempts  int               `json:"attempts,omitempty"`
	LastTry   time.Time         `json:"lastTry,omitzero"`
	Lang      string            `json:"lang,omitempty"`
	Auto      bool              `json:"auto,omitempty"`
	Lines     []transcript.Line `json:"lines,omitempty"`
}

// Channel — файл канала на диске.
type Channel struct {
	Version   int             `json:"version"`
	Channel   youtube.Channel `json:"channel"`
	Limit     int             `json:"limit"`
	AddedAt   time.Time       `json:"addedAt"`
	IndexedAt time.Time       `json:"indexedAt,omitzero"`
	Videos    []*Video        `json:"videos"`
}

// Counts — сводка по видео канала.
type Counts struct {
	Total      int `json:"total"`
	OK         int `json:"ok"`
	NoCaptions int `json:"noCaptions"`
	Failed     int `json:"failed"`  // не финальные ошибки
	Pending    int `json:"pending"` // ещё не пробовали
	Lines      int `json:"lines"`
}

// Summary — канал без субтитров, для списка каналов.
type Summary struct {
	youtube.Channel
	Limit     int       `json:"limit"`
	AddedAt   time.Time `json:"addedAt"`
	IndexedAt time.Time `json:"indexedAt,omitzero"`
	Counts    Counts    `json:"counts"`
}

// Library — все каналы в памяти и их файлы.
type Library struct {
	dir string

	mu       sync.Mutex
	channels map[string]*Channel
	dirty    map[string]bool

	index atomic.Pointer[index]  // неизменяемый снимок для поиска
	cache map[*Video]*videoIndex // разобранные видео, чтобы не строить заново
}

// ErrNoChannel — канала нет в библиотеке.
var ErrNoChannel = errors.New("канала нет в библиотеке")

// Open читает каналы из dir/channels.
func Open(dir string) (*Library, error) {
	l := &Library{
		dir:      filepath.Join(dir, "channels"),
		channels: map[string]*Channel{},
		dirty:    map[string]bool{},
		cache:    map[*Video]*videoIndex{},
	}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		// Временные файлы от прерванной записи и отложенные битые — мимо.
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(l.dir, name)
		ch, err := readChannel(path)
		if err != nil {
			bad := path + ".corrupt-" + time.Now().Format("20060102-150405")
			log.Printf("файл канала %s не читается (%v) — отложен в %s", name, err, filepath.Base(bad))
			if err := os.Rename(path, bad); err != nil {
				return nil, fmt.Errorf("не удалось отложить битый файл: %w", err)
			}
			continue
		}
		l.channels[ch.Channel.ID] = ch
	}
	l.reindex()
	return l, nil
}

func readChannel(path string) (*Channel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ch Channel
	if err := json.Unmarshal(raw, &ch); err != nil {
		return nil, err
	}
	if ch.Version != Version {
		return nil, fmt.Errorf("версия формата %d, ожидалась %d", ch.Version, Version)
	}
	if ch.Channel.ID == "" {
		return nil, errors.New("нет id канала")
	}
	return &ch, nil
}

// Summaries — все каналы по порядку добавления.
func (l *Library) Summaries() []Summary {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Summary, 0, len(l.channels))
	for _, ch := range l.channels {
		out = append(out, summarize(ch))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AddedAt.Before(out[j].AddedAt) })
	return out
}

// Summary — один канал.
func (l *Library) Summary(id string) (Summary, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.channels[id]
	if !ok {
		return Summary{}, false
	}
	return summarize(ch), true
}

func summarize(ch *Channel) Summary {
	s := Summary{Channel: ch.Channel, Limit: ch.Limit, AddedAt: ch.AddedAt, IndexedAt: ch.IndexedAt}
	for _, v := range ch.Videos {
		s.Counts.Total++
		switch {
		case v.Status == StatusOK:
			s.Counts.OK++
			s.Counts.Lines += len(v.Lines)
		case v.Status == StatusNoCaptions || v.Status == StatusAge:
			s.Counts.NoCaptions++
		case v.Status == StatusPending:
			s.Counts.Pending++
		default:
			s.Counts.Failed++
		}
	}
	return s
}

// Put добавляет канал или обновляет его данные и предел видео.
func (l *Library) Put(c youtube.Channel, limit int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.channels[c.ID]
	if !ok {
		ch = &Channel{Version: Version, AddedAt: time.Now()}
		l.channels[c.ID] = ch
	}
	ch.Channel = c
	ch.Limit = limit
	l.reindexLocked()
	return l.saveLocked(c.ID)
}

// Remove удаляет канал и его файл. Вызывающий сам останавливает индексацию
// канала до вызова (indexer.Remove).
func (l *Library) Remove(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.channels[id]; !ok {
		return ErrNoChannel
	}
	delete(l.channels, id)
	delete(l.dirty, id)
	err := os.Remove(l.path(id))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	l.reindexLocked()
	return err
}

// Merge сливает свежий список видео канала с тем, что уже есть: известные
// видео сохраняют субтитры и статус, новые встают как pending. Видео,
// выпавшие из списка (канал выложил новые сверх предела), остаются — по
// ним по-прежнему можно искать.
func (l *Library) Merge(id string, videos []youtube.Video) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.channels[id]
	if !ok {
		return ErrNoChannel
	}
	known := map[string]*Video{}
	for _, v := range ch.Videos {
		known[v.ID] = v
	}
	fresh := make([]*Video, 0, len(videos))
	seen := map[string]bool{}
	for _, yv := range videos {
		seen[yv.ID] = true
		// Копия, а не правка на месте: снимок поиска читает старые записи
		// без блокировки.
		v := Video{ID: yv.ID, Status: StatusPending}
		if old, ok := known[yv.ID]; ok {
			v = *old
		}
		v.Title, v.Published, v.Duration = yv.Title, yv.Published, yv.Duration
		fresh = append(fresh, &v)
	}
	for _, v := range ch.Videos {
		if !seen[v.ID] && v.Status == StatusOK {
			fresh = append(fresh, v)
		}
	}
	ch.Videos = fresh
	l.reindexLocked()
	return l.saveLocked(id)
}

// Todo — видео канала, которые надо (пере)скачать, от новых к старым.
func (l *Library) Todo(id string) []Video {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.channels[id]
	if !ok {
		return nil
	}
	var out []Video
	for _, v := range ch.Videos {
		if !v.Status.Final() {
			out = append(out, Video{ID: v.ID, Title: v.Title, Published: v.Published})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	return out
}

// SetResult записывает исход скачивания. На диск — не сразу, а через Flush
// (индексатор зовёт его раз в несколько видео и в конце).
func (l *Library) SetResult(channelID, videoID string, status Status, t *transcript.Transcript, errText string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.channels[channelID]
	if !ok {
		return
	}
	i := slices.IndexFunc(ch.Videos, func(v *Video) bool { return v.ID == videoID })
	if i < 0 {
		return
	}
	// Запись заменяется новой, а не правится на месте: снимок поиска
	// держит старый указатель и читает его без блокировки.
	v := *ch.Videos[i]
	v.Status, v.Error, v.LastTry = status, errText, time.Now()
	v.Attempts++
	if t != nil {
		v.Lang, v.Auto, v.Lines = t.Lang, t.Auto, t.Lines
	}
	ch.Videos[i] = &v
	l.dirty[channelID] = true
	if status == StatusOK {
		l.reindexLocked()
	}
}

// MarkIndexed отмечает время законченной индексации.
func (l *Library) MarkIndexed(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ch, ok := l.channels[id]; ok {
		ch.IndexedAt = time.Now()
		l.dirty[id] = true
	}
}

// Flush пишет на диск изменённые каналы.
func (l *Library) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	for id := range l.dirty {
		if err := l.saveLocked(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (l *Library) path(id string) string { return filepath.Join(l.dir, id+".json") }

func (l *Library) saveLocked(id string) error {
	ch, ok := l.channels[id]
	if !ok {
		return ErrNoChannel
	}
	raw, err := json.Marshal(ch)
	if err != nil {
		return err
	}
	if err := writeAtomic(l.path(id), raw); err != nil {
		return fmt.Errorf("запись канала %s: %w", ch.Channel.Title, err)
	}
	delete(l.dirty, id)
	return nil
}

// writeAtomic пишет файл целиком или не пишет вовсе.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // после rename файла уже нет — ошибка не важна
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteAtomic — то же для других файлов состояния (индексатор).
func WriteAtomic(path string, data []byte) error { return writeAtomic(path, data) }
