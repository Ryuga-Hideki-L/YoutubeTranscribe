// Package indexer скачивает субтитры каналов в фоне.
//
// Очередь одна на всё приложение, рабочий тоже один. YouTube блокирует адрес,
// а не канал, поэтому два канала разом только быстрее приводят к капче.
// Пауза после блокировки (предохранитель) тоже общая. Она сохраняется на диск,
// чтобы перезапуск не начинал стучаться заново.
package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/library"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/transcript"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/youtube"
)

// State — состояние задания канала.
type State string

const (
	StateQueued    State = "queued"    // ждёт своей очереди
	StateListing   State = "listing"   // получает список видео
	StateRunning   State = "running"   // скачивает субтитры
	StateCooling   State = "cooling"   // пауза после блокировки
	StateDone      State = "done"      // закончено
	StateBlocked   State = "blocked"   // остановлено: YouTube блокирует
	StateCancelled State = "cancelled" // отменено человеком
	StateFailed    State = "failed"    // не удалось начать (ключ, квота, канал)
)

// Причины паузы задания (Job.Reason).
const (
	ReasonBlocked = "blocked" // YouTube блокирует адрес
	ReasonNetwork = "network" // нет связи с YouTube
)

// Job — задание индексации канала (последнее по времени).
type Job struct {
	ChannelID     string    `json:"channelId"`
	State         State     `json:"state"`
	Total         int       `json:"total"` // видео к скачиванию в этом проходе
	Done          int       `json:"done"`
	OK            int       `json:"ok"`
	NoCaptions    int       `json:"noCaptions"`
	Failed        int       `json:"failed"`
	Current       string    `json:"current,omitempty"` // название текущего видео
	CooldownUntil time.Time `json:"cooldownUntil,omitzero"`
	Reason        string    `json:"reason,omitempty"` // причина паузы или остановки: blocked, network
	Error         string    `json:"error,omitempty"`
	StartedAt     time.Time `json:"startedAt,omitzero"`
	FinishedAt    time.Time `json:"finishedAt,omitzero"`
}

// Fetcher скачивает субтитры видео (transcript.Client).
type Fetcher interface {
	Fetch(ctx context.Context, videoID string) (*transcript.Transcript, error)
}

// Lister получает канал и его видео (youtube.Client).
type Lister interface {
	HasKey() bool
	Channel(ctx context.Context, id string) (youtube.Channel, error)
	Uploads(ctx context.Context, playlist string, limit int) ([]youtube.Video, error)
}

// Config — ритм и предохранитель. Нулевое поле означает значение по умолчанию.
type Config struct {
	Delay       time.Duration // пауза между видео (1 с)
	Jitter      time.Duration // плюс случайно до (1 с)
	Cooldown    time.Duration // первая пауза после блокировки (60 с), дальше вдвое
	MaxBlocks   int           // блокировок подряд до остановки (3)
	BlockPause  time.Duration // пауза после остановки (30 мин)
	FlushEvery  int           // писать на диск каждые N видео (10)
	NetPause    time.Duration // первая пауза без сети (10 с), дальше вдвое
	MaxNetFails int           // сетевых сбоев подряд до остановки (5)
	StatePath   string        // файл предохранителя
	Now         func() time.Time
	Sleep       func(ctx context.Context, duration time.Duration) error
	After       func(duration time.Duration) <-chan time.Time
}

func (cfg *Config) defaults() {
	if cfg.Delay == 0 {
		cfg.Delay = time.Second
	}
	if cfg.Jitter == 0 {
		cfg.Jitter = time.Second
	}
	if cfg.Cooldown == 0 {
		cfg.Cooldown = time.Minute
	}
	if cfg.MaxBlocks == 0 {
		cfg.MaxBlocks = 3
	}
	if cfg.BlockPause == 0 {
		cfg.BlockPause = 30 * time.Minute
	}
	if cfg.FlushEvery == 0 {
		cfg.FlushEvery = 10
	}
	if cfg.NetPause == 0 {
		cfg.NetPause = 10 * time.Second
	}
	if cfg.MaxNetFails == 0 {
		cfg.MaxNetFails = 5
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
}

func sleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// breaker — общий предохранитель, файл state.json.
type breaker struct {
	Version      int       `json:"version"`
	BlockedUntil time.Time `json:"blockedUntil,omitzero"`
	Blocks       int       `json:"blocks"` // блокировок подряд
}

// Indexer — очередь и рабочий.
type Indexer struct {
	lib     *library.Library
	lister  Lister
	fetcher Fetcher
	cfg     Config

	mu          sync.Mutex
	jobs        map[string]*Job
	queue       []string
	limits      map[string]int
	running     string
	cancel      context.CancelFunc
	breaker     breaker
	subscribers map[chan struct{}]struct{}
	wake        chan struct{}

	restricted   bool                           // DNS сети включает Restricted Mode
	checkNetwork func(ctx context.Context) bool // по умолчанию transcript.RestrictedMode
}

// New создаёт индексатор и читает предохранитель с диска.
func New(lib *library.Library, lister Lister, fetcher Fetcher, cfg Config) *Indexer {
	cfg.defaults()
	indexer := &Indexer{
		lib: lib, lister: lister, fetcher: fetcher, cfg: cfg,
		jobs:        map[string]*Job{},
		limits:      map[string]int{},
		subscribers: map[chan struct{}]struct{}{},
		wake:        make(chan struct{}, 1),
		breaker:     breaker{Version: 1},
		checkNetwork: func(ctx context.Context) bool {
			return transcript.RestrictedMode(ctx, nil)
		},
	}
	if cfg.StatePath != "" {
		if raw, err := os.ReadFile(cfg.StatePath); err == nil {
			if err := json.Unmarshal(raw, &indexer.breaker); err != nil {
				log.Printf("предохранитель %s не читается: %v", filepath.Base(cfg.StatePath), err)
				indexer.breaker = breaker{Version: 1}
			}
		}
	}
	return indexer
}

// Run берёт задания из очереди по одному, пока жив ctx.
func (indexer *Indexer) Run(ctx context.Context) {
	for {
		id, jobCtx, ok := indexer.next(ctx)
		if !ok {
			return
		}
		indexer.run(jobCtx, id)
		indexer.mu.Lock()
		indexer.cancel()
		indexer.running, indexer.cancel = "", nil
		indexer.mu.Unlock()
		indexer.notify()
	}
}

// next ждёт задание. Если ok равно false, ctx закрыт.
func (indexer *Indexer) next(ctx context.Context) (string, context.Context, bool) {
	for {
		indexer.mu.Lock()
		if len(indexer.queue) > 0 {
			id := indexer.queue[0]
			indexer.queue = indexer.queue[1:]
			jobCtx, cancel := context.WithCancel(ctx)
			indexer.running, indexer.cancel = id, cancel
			indexer.mu.Unlock()
			return id, jobCtx, true
		}
		// Задания, остановленные блокировкой, сами встают в очередь, когда
		// кончится пауза. Таймер нужен, только пока такие задания есть.
		var retry <-chan time.Time
		if indexer.hasBlockedLocked() {
			retry = indexer.cfg.After(indexer.breaker.BlockedUntil.Sub(indexer.cfg.Now()))
		}
		indexer.mu.Unlock()
		select {
		case <-indexer.wake:
		case <-retry:
			indexer.requeueBlocked()
		case <-ctx.Done():
			return "", nil, false
		}
	}
}

func (indexer *Indexer) hasBlockedLocked() bool {
	for _, job := range indexer.jobs {
		if job.State == StateBlocked {
			return true
		}
	}
	return false
}

// requeueBlocked ставит в очередь задания, остановленные блокировкой.
func (indexer *Indexer) requeueBlocked() {
	indexer.mu.Lock()
	var ids []string
	for id, job := range indexer.jobs {
		if job.State == StateBlocked {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		indexer.queue = append(indexer.queue, id)
		indexer.jobs[id].State = StateQueued
	}
	indexer.mu.Unlock()
	if len(ids) > 0 {
		log.Printf("пауза после блокировки кончилась — снова в очереди: %v", ids)
		indexer.notify()
	}
}

// Enqueue ставит канал в очередь с пределом видео limit. Канал, который уже
// стоит в очереди или идёт, второй раз не ставится. force снимает
// предохранитель, потому что человек сам решил попробовать снова.
func (indexer *Indexer) Enqueue(id string, limit int, force bool) {
	indexer.mu.Lock()
	if force {
		indexer.breaker.BlockedUntil, indexer.breaker.Blocks = time.Time{}, 0
		indexer.saveBreakerLocked()
	}
	indexer.limits[id] = limit
	if indexer.running == id || slices.Contains(indexer.queue, id) {
		indexer.mu.Unlock()
		return
	}
	indexer.queue = append(indexer.queue, id)
	indexer.jobs[id] = &Job{ChannelID: id, State: StateQueued}
	indexer.mu.Unlock()
	select {
	case indexer.wake <- struct{}{}:
	default:
	}
	indexer.notify()
}

// Cancel снимает канал с очереди или останавливает идущее задание.
func (indexer *Indexer) Cancel(id string) {
	indexer.mu.Lock()
	if i := slices.Index(indexer.queue, id); i >= 0 {
		indexer.queue = slices.Delete(indexer.queue, i, i+1)
		indexer.jobs[id].State = StateCancelled
	}
	// Остановленное блокировкой не встанет в очередь само.
	if job, ok := indexer.jobs[id]; ok && job.State == StateBlocked {
		job.State = StateCancelled
	}
	if indexer.running == id {
		indexer.cancel()
	}
	indexer.mu.Unlock()
	indexer.notify()
}

// Forget убирает задание удалённого канала из списка.
func (indexer *Indexer) Forget(id string) {
	indexer.Cancel(id)
	indexer.mu.Lock()
	delete(indexer.jobs, id)
	delete(indexer.limits, id)
	indexer.mu.Unlock()
	indexer.notify()
}

// Jobs — копии заданий по каналам.
func (indexer *Indexer) Jobs() map[string]Job {
	indexer.mu.Lock()
	defer indexer.mu.Unlock()
	out := make(map[string]Job, len(indexer.jobs))
	for id, job := range indexer.jobs {
		out[id] = *job
	}
	return out
}

// BlockedUntil — до какого времени действует предохранитель.
func (indexer *Indexer) BlockedUntil() time.Time {
	indexer.mu.Lock()
	defer indexer.mu.Unlock()
	if indexer.breaker.BlockedUntil.After(indexer.cfg.Now()) {
		return indexer.breaker.BlockedUntil
	}
	return time.Time{}
}

// Restricted говорит, включает ли DNS сети Restricted Mode YouTube.
// Ответ берётся из последней проверки.
func (indexer *Indexer) Restricted() bool {
	indexer.mu.Lock()
	defer indexer.mu.Unlock()
	return indexer.restricted
}

// CheckNetwork перепроверяет DNS при запуске и перед каждым заданием.
// Сеть могла смениться.
func (indexer *Indexer) CheckNetwork(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	restricted := indexer.checkNetwork(ctx)
	indexer.mu.Lock()
	changed := indexer.restricted != restricted
	indexer.restricted = restricted
	indexer.mu.Unlock()
	if changed {
		if restricted {
			log.Print("DNS сети включает Restricted Mode YouTube: часть видео будет недоступна")
		}
		indexer.notify()
	}
}

// Subscribe отдаёт сигнал «что-то поменялось». Буфер канала равен 1, поэтому
// частые изменения сливаются в один сигнал. Свежий снимок подписчик берёт сам.
func (indexer *Indexer) Subscribe() (<-chan struct{}, func()) {
	signal := make(chan struct{}, 1)
	indexer.mu.Lock()
	indexer.subscribers[signal] = struct{}{}
	indexer.mu.Unlock()
	return signal, func() {
		indexer.mu.Lock()
		delete(indexer.subscribers, signal)
		indexer.mu.Unlock()
	}
}

func (indexer *Indexer) notify() {
	indexer.mu.Lock()
	defer indexer.mu.Unlock()
	for signal := range indexer.subscribers {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
}

// update меняет задание под замком и оповещает подписчиков. Если задания нет,
// канал уже удалили через Forget. Заводить задание заново нельзя, иначе в
// списке останется призрак удалённого канала.
func (indexer *Indexer) update(id string, change func(job *Job)) {
	indexer.mu.Lock()
	job, ok := indexer.jobs[id]
	if ok {
		change(job)
	}
	indexer.mu.Unlock()
	if ok {
		indexer.notify()
	}
}

func (indexer *Indexer) saveBreakerLocked() {
	if indexer.cfg.StatePath == "" {
		return
	}
	raw, _ := json.Marshal(indexer.breaker)
	if err := library.WriteAtomic(indexer.cfg.StatePath, raw); err != nil {
		log.Printf("предохранитель не записан: %v", err)
	}
}

// finish заканчивает задание. Необязательный reason — причина для интерфейса,
// например ReasonNetwork при остановке без сети.
func (indexer *Indexer) finish(id string, state State, errText string, reason ...string) {
	if err := indexer.lib.Flush(); err != nil {
		log.Printf("запись библиотеки: %v", err)
	}
	indexer.update(id, func(job *Job) {
		job.State, job.Error, job.Current = state, errText, ""
		job.CooldownUntil, job.Reason = time.Time{}, ""
		if len(reason) > 0 {
			job.Reason = reason[0]
		}
		job.FinishedAt = indexer.cfg.Now()
	})
}

func (indexer *Indexer) run(ctx context.Context, id string) {
	indexer.mu.Lock()
	limit := indexer.limits[id]
	indexer.mu.Unlock()
	indexer.update(id, func(job *Job) {
		*job = Job{ChannelID: id, State: StateListing, StartedAt: indexer.cfg.Now()}
	})
	indexer.CheckNetwork(ctx)

	if err := indexer.list(ctx, id, limit); err != nil {
		if ctx.Err() != nil || errors.Is(err, library.ErrNoChannel) {
			// Отменили или канал удалили, пока шёл запрос к API.
			indexer.finish(id, StateCancelled, "")
			return
		}
		// Без свежего списка можно доделать уже известные видео. Так бывает,
		// когда нет ключа или кончилась квота.
		if len(indexer.lib.Todo(id)) == 0 {
			indexer.finish(id, StateFailed, err.Error())
			return
		}
		log.Printf("список видео канала %s не обновлён: %v", id, err)
	}

	todo := indexer.lib.Todo(id)
	indexer.update(id, func(job *Job) { job.State, job.Total = StateRunning, len(todo) })

	netFails := 0 // сетевых сбоев подряд
	for i := 0; i < len(todo); {
		video := todo[i]
		if err := indexer.waitBreaker(ctx, id); err != nil {
			indexer.finish(id, StateCancelled, "")
			return
		}
		indexer.update(id, func(job *Job) { job.Current = video.Title })

		fetchCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		fetched, err := indexer.fetcher.Fetch(fetchCtx, video.ID)
		cancel()
		if ctx.Err() != nil {
			indexer.finish(id, StateCancelled, "")
			return
		}
		// Нет сети. С видео при этом всё может быть в порядке, поэтому статус
		// не пишем. Ждём с растущей паузой и повторяем то же видео. После
		// MaxNetFails сбоев подряд задание останавливается, а видео остаются
		// не скачанными.
		if errors.Is(err, transcript.ErrNetwork) {
			netFails++
			if netFails >= indexer.cfg.MaxNetFails {
				indexer.finish(id, StateFailed, err.Error(), ReasonNetwork)
				return
			}
			if err := indexer.pause(ctx, id, ReasonNetwork, indexer.cfg.NetPause<<(netFails-1)); err != nil {
				indexer.finish(id, StateCancelled, "")
				return
			}
			continue
		}
		netFails = 0

		status := classify(err)
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		indexer.lib.SetResult(id, video.ID, status, fetched, errText)

		if status == library.StatusBlocked {
			stop, until := indexer.blocked()
			log.Printf("блокировка на %s (%s), пауза до %s", video.ID, errText, until.Format(time.TimeOnly))
			if stop {
				indexer.finish(id, StateBlocked, errText)
				return
			}
			continue // то же видео после паузы
		}
		indexer.unblocked()

		indexer.update(id, func(job *Job) {
			job.Done++
			switch status {
			case library.StatusOK:
				job.OK++
			case library.StatusNoCaptions, library.StatusAge:
				job.NoCaptions++
			default:
				job.Failed++
			}
		})
		i++
		if i%indexer.cfg.FlushEvery == 0 {
			if err := indexer.lib.Flush(); err != nil {
				log.Printf("запись библиотеки: %v", err)
			}
		}
		if i < len(todo) {
			pause := indexer.cfg.Delay + rand.N(indexer.cfg.Jitter+1)
			if err := indexer.cfg.Sleep(ctx, pause); err != nil {
				indexer.finish(id, StateCancelled, "")
				return
			}
		}
	}
	indexer.lib.MarkIndexed(id)
	indexer.finish(id, StateDone, "")
}

// list обновляет данные канала и список его видео.
func (indexer *Indexer) list(ctx context.Context, id string, limit int) error {
	channel, err := indexer.lister.Channel(ctx, id)
	if err != nil {
		return err
	}
	if err := indexer.lib.Update(channel, limit); err != nil {
		return err
	}
	videos, err := indexer.lister.Uploads(ctx, channel.Uploads, limit)
	if err != nil {
		return err
	}
	return indexer.lib.Merge(id, videos)
}

// waitBreaker ждёт конца общей паузы после блокировки.
func (indexer *Indexer) waitBreaker(ctx context.Context, id string) error {
	until := indexer.BlockedUntil()
	if until.IsZero() {
		return nil
	}
	return indexer.pause(ctx, id, ReasonBlocked, until.Sub(indexer.cfg.Now()))
}

// pause — пауза задания с причиной и обратным отсчётом в интерфейсе.
func (indexer *Indexer) pause(ctx context.Context, id, reason string, duration time.Duration) error {
	until := indexer.cfg.Now().Add(duration)
	indexer.update(id, func(job *Job) { job.State, job.Reason, job.CooldownUntil = StateCooling, reason, until })
	if err := indexer.cfg.Sleep(ctx, duration); err != nil {
		return err
	}
	indexer.update(id, func(job *Job) { job.State, job.Reason, job.CooldownUntil = StateRunning, "", time.Time{} })
	return nil
}

// blocked отмечает блокировку. Пауза растёт вдвое. После MaxBlocks блокировок
// подряд задание останавливается и наступает долгая пауза.
func (indexer *Indexer) blocked() (stop bool, until time.Time) {
	indexer.mu.Lock()
	defer indexer.mu.Unlock()
	indexer.breaker.Blocks++
	pause := indexer.cfg.Cooldown << (indexer.breaker.Blocks - 1)
	stop = indexer.breaker.Blocks >= indexer.cfg.MaxBlocks
	if stop {
		pause = indexer.cfg.BlockPause
	}
	indexer.breaker.BlockedUntil = indexer.cfg.Now().Add(pause)
	indexer.saveBreakerLocked()
	return stop, indexer.breaker.BlockedUntil
}

func (indexer *Indexer) unblocked() {
	indexer.mu.Lock()
	defer indexer.mu.Unlock()
	if indexer.breaker.Blocks != 0 || !indexer.breaker.BlockedUntil.IsZero() {
		indexer.breaker.Blocks, indexer.breaker.BlockedUntil = 0, time.Time{}
		indexer.saveBreakerLocked()
	}
}

func classify(err error) library.Status {
	switch {
	case err == nil:
		return library.StatusOK
	case errors.Is(err, transcript.ErrNoTranscript):
		return library.StatusNoCaptions
	case errors.Is(err, transcript.ErrAgeRestricted):
		return library.StatusAge
	case errors.Is(err, transcript.ErrBlocked):
		return library.StatusBlocked
	case errors.Is(err, transcript.ErrPoToken):
		return library.StatusPoToken
	case errors.Is(err, transcript.ErrUnavailable):
		return library.StatusUnavailable
	}
	return library.StatusError
}

// ResumePending ставит в очередь каналы, индексацию которых прервал
// перезапуск. Это каналы с ещё не скачанными видео и каналы, которые добавили,
// но ни разу не проиндексировали: список их видео не получен.
func (indexer *Indexer) ResumePending() {
	for _, summary := range indexer.lib.Summaries() {
		if summary.Counts.Pending > 0 || summary.IndexedAt.IsZero() {
			indexer.Enqueue(summary.ID, summary.Limit, false)
		}
	}
}
