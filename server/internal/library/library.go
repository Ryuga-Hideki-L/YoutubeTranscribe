// Package library хранит каналы и субтитры их видео на диске и ищет по ним.
//
// Каждый канал лежит в файле channels/<id>.json. Запись атомарная: временный
// файл, fsync, rename. Если запись оборвалась, остаётся прежняя версия.
// Битый файл не затирается, а переименовывается в .corrupt-<время>, и канал
// пропускается. Писать в файлы может только Library.
package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
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
	StatusUnavailable Status = "unavailable"  // недоступно, возможно временно (регион, Restricted Mode)
	StatusPoToken     Status = "pot_required" // нужен PO-токен
	StatusBlocked     Status = "blocked"      // YouTube блокировал запрос
	StatusError       Status = "error"        // прочая ошибка
)

// Final говорит, что повторять бесполезно: результат не изменится. Для
// no_captions у свежих видео есть исключение, см. needsFetch.
func (status Status) Final() bool {
	return status == StatusOK || status == StatusNoCaptions || status == StatusAge
}

// YouTube делает автоматические субтитры не сразу после публикации или конца
// трансляции. Поэтому «нет субтитров» у видео моложе недели ещё не
// окончательный ответ. Такое видео перепроверяется, но не чаще раза в 6 часов.
const (
	freshWindow  = 7 * 24 * time.Hour
	recheckAfter = 6 * time.Hour
)

func needsFetch(video *Video, now time.Time) bool {
	if video.Status == StatusNoCaptions && now.Sub(video.Published) < freshWindow {
		return now.Sub(video.LastTry) >= recheckAfter
	}
	return !video.Status.Final()
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

	mu       sync.Mutex // данные в памяти, держится недолго
	writeMu  sync.Mutex // запись файлов по одной, берётся раньше mu
	channels map[string]*Channel
	dirty    map[string]bool

	snapshot atomic.Pointer[searchSnapshot] // по нему ищем без замка
	now      func() time.Time               // часы, в тестах подменяются
	entries  map[*Video]*searchEntry        // готовые к поиску видео, чтобы не собирать заново
}

// ErrNoChannel — канала нет в библиотеке.
var ErrNoChannel = errors.New("канала нет в библиотеке")

// Open читает каналы из dir/channels.
func Open(dir string) (*Library, error) {
	lib := &Library{
		dir:      filepath.Join(dir, "channels"),
		channels: map[string]*Channel{},
		dirty:    map[string]bool{},
		entries:  map[*Video]*searchEntry{},
		now:      time.Now,
	}
	if err := os.MkdirAll(lib.dir, 0o755); err != nil {
		return nil, err
	}
	files, err := os.ReadDir(lib.dir)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		name := file.Name()
		// Пропускаем временные файлы от прерванной записи и уже отложенные битые.
		if file.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(lib.dir, name)
		channel, err := readChannel(path)
		if err != nil {
			bad := path + ".corrupt-" + time.Now().Format("20060102-150405")
			log.Printf("файл канала %s не читается (%v) — отложен в %s", name, err, filepath.Base(bad))
			if err := os.Rename(path, bad); err != nil {
				return nil, fmt.Errorf("не удалось отложить битый файл: %w", err)
			}
			continue
		}
		lib.channels[channel.Channel.ID] = channel
	}
	lib.reindex()
	return lib, nil
}

func readChannel(path string) (*Channel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var channel Channel
	if err := json.Unmarshal(raw, &channel); err != nil {
		return nil, err
	}
	if channel.Version != Version {
		return nil, fmt.Errorf("версия формата %d, ожидалась %d", channel.Version, Version)
	}
	if channel.Channel.ID == "" {
		return nil, errors.New("нет id канала")
	}
	return &channel, nil
}

// Summaries — все каналы по порядку добавления.
func (lib *Library) Summaries() []Summary {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	out := make([]Summary, 0, len(lib.channels))
	for _, channel := range lib.channels {
		out = append(out, summarize(channel))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AddedAt.Before(out[j].AddedAt) })
	return out
}

// Summary — один канал.
func (lib *Library) Summary(id string) (Summary, bool) {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	channel, ok := lib.channels[id]
	if !ok {
		return Summary{}, false
	}
	return summarize(channel), true
}

func summarize(channel *Channel) Summary {
	summary := Summary{Channel: channel.Channel, Limit: channel.Limit, AddedAt: channel.AddedAt, IndexedAt: channel.IndexedAt}
	for _, video := range channel.Videos {
		summary.Counts.Total++
		switch {
		case video.Status == StatusOK:
			summary.Counts.OK++
			summary.Counts.Lines += len(video.Lines)
		case video.Status == StatusNoCaptions || video.Status == StatusAge:
			summary.Counts.NoCaptions++
		case video.Status == StatusPending:
			summary.Counts.Pending++
		default:
			summary.Counts.Failed++
		}
	}
	return summary
}

// Put добавляет канал или обновляет его данные и предел видео.
func (lib *Library) Put(info youtube.Channel, limit int) error {
	lib.writeMu.Lock()
	defer lib.writeMu.Unlock()
	lib.mu.Lock()
	channel, ok := lib.channels[info.ID]
	if !ok {
		channel = &Channel{Version: Version, AddedAt: lib.now()}
		lib.channels[info.ID] = channel
	}
	channel.Channel = info
	channel.Limit = limit
	lib.reindexLocked()
	lib.mu.Unlock()
	return lib.persist(info.ID)
}

// Update обновляет данные и предел видео канала, только если канал ещё есть.
// Индексатор не должен воскрешать канал, который удалили, пока шёл запрос к API.
func (lib *Library) Update(info youtube.Channel, limit int) error {
	lib.writeMu.Lock()
	defer lib.writeMu.Unlock()
	lib.mu.Lock()
	channel, ok := lib.channels[info.ID]
	if !ok {
		lib.mu.Unlock()
		return ErrNoChannel
	}
	channel.Channel = info
	channel.Limit = limit
	lib.reindexLocked()
	lib.mu.Unlock()
	return lib.persist(info.ID)
}

// Remove удаляет канал и его файл. Индексацию канала вызывающий останавливает
// сам до вызова, через indexer.Forget.
func (lib *Library) Remove(id string) error {
	// Берём writeMu, чтобы идущая запись канала закончилась до удаления файла.
	// Иначе она вернула бы файл после os.Remove.
	lib.writeMu.Lock()
	defer lib.writeMu.Unlock()
	lib.mu.Lock()
	defer lib.mu.Unlock()
	if _, ok := lib.channels[id]; !ok {
		return ErrNoChannel
	}
	delete(lib.channels, id)
	delete(lib.dirty, id)
	err := os.Remove(lib.path(id))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	lib.reindexLocked()
	return err
}

// Merge сливает свежий список видео канала с тем, что уже есть. Известные
// видео сохраняют субтитры и статус, новые встают как pending. Видео со
// скачанными субтитрами остаются, даже если выпали из списка, потому что по
// ним можно искать. Выпадают они, когда канал выложил новые видео сверх предела.
func (lib *Library) Merge(id string, videos []youtube.Video) error {
	lib.writeMu.Lock()
	defer lib.writeMu.Unlock()
	lib.mu.Lock()
	channel, ok := lib.channels[id]
	if !ok {
		lib.mu.Unlock()
		return ErrNoChannel
	}
	known := map[string]*Video{}
	for _, video := range channel.Videos {
		known[video.ID] = video
	}
	merged := make([]*Video, 0, len(videos))
	seen := map[string]bool{}
	for _, listed := range videos {
		seen[listed.ID] = true
		// Делаем копию, а не правим на месте. Снимок поиска читает старые
		// записи без блокировки.
		video := Video{ID: listed.ID, Status: StatusPending}
		if old, ok := known[listed.ID]; ok {
			video = *old
		}
		video.Title, video.Published, video.Duration = listed.Title, listed.Published, listed.Duration
		merged = append(merged, &video)
	}
	for _, video := range channel.Videos {
		if !seen[video.ID] && video.Status == StatusOK {
			merged = append(merged, video)
		}
	}
	channel.Videos = merged
	lib.reindexLocked()
	lib.mu.Unlock()
	return lib.persist(id)
}

// Todo — видео канала, которые надо (пере)скачать, от новых к старым.
func (lib *Library) Todo(id string) []Video {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	channel, ok := lib.channels[id]
	if !ok {
		return nil
	}
	var out []Video
	now := lib.now()
	for _, video := range channel.Videos {
		if needsFetch(video, now) {
			out = append(out, Video{ID: video.ID, Title: video.Title, Published: video.Published})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	return out
}

// SetResult записывает исход скачивания. На диск он попадает не сразу, а через
// Flush. Индексатор зовёт Flush раз в несколько видео и в конце.
func (lib *Library) SetResult(channelID, videoID string, status Status, fetched *transcript.Transcript, errText string) {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	channel, ok := lib.channels[channelID]
	if !ok {
		return
	}
	i := slices.IndexFunc(channel.Videos, func(video *Video) bool { return video.ID == videoID })
	if i < 0 {
		return
	}
	// Заменяем запись новой, а не правим на месте. Снимок поиска держит
	// старый указатель и читает его без блокировки.
	video := *channel.Videos[i]
	video.Status, video.Error, video.LastTry = status, errText, lib.now()
	video.Attempts++
	if fetched != nil {
		video.Lang, video.Auto, video.Lines = fetched.Lang, fetched.Auto, fetched.Lines
	}
	channel.Videos[i] = &video
	lib.dirty[channelID] = true
	if status == StatusOK {
		lib.reindexLocked()
	}
}

// MarkIndexed отмечает время законченной индексации.
func (lib *Library) MarkIndexed(id string) {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	if channel, ok := lib.channels[id]; ok {
		channel.IndexedAt = lib.now()
		lib.dirty[id] = true
	}
}

// Flush пишет на диск изменённые каналы.
func (lib *Library) Flush() error {
	lib.writeMu.Lock()
	defer lib.writeMu.Unlock()
	lib.mu.Lock()
	ids := slices.Collect(maps.Keys(lib.dirty))
	lib.mu.Unlock()
	return lib.persist(ids...)
}

func (lib *Library) path(id string) string { return filepath.Join(lib.dir, id+".json") }

// persist пишет каналы на диск. Вызывающий держит writeMu. Под lib.mu берётся
// только снимок канала. Записи видео после создания не меняются (см.
// SetResult), поэтому сериализация и fsync идут без замка. На большом канале
// это десятки мегабайт и сотни миллисекунд. Поиск, состояние и SetResult их
// не ждут.
func (lib *Library) persist(ids ...string) error {
	var errs []error
	for _, id := range ids {
		lib.mu.Lock()
		channel, ok := lib.channels[id]
		if !ok {
			lib.mu.Unlock()
			continue
		}
		snap := *channel
		snap.Videos = slices.Clone(channel.Videos)
		delete(lib.dirty, id)
		lib.mu.Unlock()

		if err := lib.write(id, &snap); err != nil {
			lib.mu.Lock()
			if _, ok := lib.channels[id]; ok {
				lib.dirty[id] = true // повторим при следующей записи
			}
			lib.mu.Unlock()
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (lib *Library) write(id string, channel *Channel) error {
	raw, err := json.Marshal(channel)
	if err != nil {
		return err
	}
	if err := writeFile(lib.path(id), raw); err != nil {
		return fmt.Errorf("запись канала %s: %w", channel.Channel.Title, err)
	}
	return nil
}

// writeFile — запись файла канала; в тестах подменяется.
var writeFile = writeAtomic

// writeAtomic пишет файл целиком или не пишет вовсе.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // после rename файла уже нет, поэтому ошибка не важна
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

// WriteAtomic делает то же для других файлов состояния. Её использует индексатор.
func WriteAtomic(path string, data []byte) error { return writeAtomic(path, data) }
