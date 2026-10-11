// Package indexer скачивает субтитры каналов в фоне.
//
// Очередь одна на всё приложение и один рабочий: YouTube блокирует адрес,
// а не канал, поэтому скачивать два канала разом — только быстрее нарваться
// на капчу. По той же причине пауза после блокировки (предохранитель) общая
// и сохраняется на диск: перезапуск не начинает стучаться заново.
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

// Config — ритм и предохранитель. Нулевые поля — значения по умолчанию.
type Config struct {
	Delay      time.Duration // пауза между видео (1 с)
	Jitter     time.Duration // плюс случайно до (1 с)
	Cooldown   time.Duration // первая пауза после блокировки (60 с), дальше вдвое
	MaxBlocks  int           // блокировок подряд до остановки (3)
	BlockPause time.Duration // пауза после остановки (30 мин)
	FlushEvery int           // писать на диск каждые N видео (10)
	StatePath  string        // файл предохранителя
	Now        func() time.Time
	Sleep      func(ctx context.Context, d time.Duration) error
	After      func(d time.Duration) <-chan time.Time
}

func (c *Config) defaults() {
	if c.Delay == 0 {
		c.Delay = time.Second
	}
	if c.Jitter == 0 {
		c.Jitter = time.Second
	}
	if c.Cooldown == 0 {
		c.Cooldown = time.Minute
	}
	if c.MaxBlocks == 0 {
		c.MaxBlocks = 3
	}
	if c.BlockPause == 0 {
		c.BlockPause = 30 * time.Minute
	}
	if c.FlushEvery == 0 {
		c.FlushEvery = 10
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Sleep == nil {
		c.Sleep = sleep
	}
	if c.After == nil {
		c.After = time.After
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
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
	lib *library.Library
	yt  Lister
	tr  Fetcher
	cfg Config

	mu      sync.Mutex
	jobs    map[string]*Job
	queue   []string
	limits  map[string]int
	running string
	cancel  context.CancelFunc
	br      breaker
	subs    map[chan struct{}]struct{}
	wake    chan struct{}

	restricted   bool                           // DNS сети включает Restricted Mode
	checkNetwork func(ctx context.Context) bool // по умолчанию transcript.RestrictedMode
}

// New создаёт индексатор и читает предохранитель с диска.
func New(lib *library.Library, yt Lister, tr Fetcher, cfg Config) *Indexer {
	cfg.defaults()
	ix := &Indexer{
		lib: lib, yt: yt, tr: tr, cfg: cfg,
		jobs:   map[string]*Job{},
		limits: map[string]int{},
		subs:   map[chan struct{}]struct{}{},
		wake:   make(chan struct{}, 1),
		br:     breaker{Version: 1},
		checkNetwork: func(ctx context.Context) bool {
			return transcript.RestrictedMode(ctx, nil)
		},
	}
	if cfg.StatePath != "" {
		if raw, err := os.ReadFile(cfg.StatePath); err == nil {
			if err := json.Unmarshal(raw, &ix.br); err != nil {
				log.Printf("предохранитель %s не читается: %v", filepath.Base(cfg.StatePath), err)
				ix.br = breaker{Version: 1}
			}
		}
	}
	return ix
}

// Run — рабочий: берёт задания из очереди по одному, пока жив ctx.
func (ix *Indexer) Run(ctx context.Context) {
	for {
		id, jobCtx, ok := ix.next(ctx)
		if !ok {
			return
		}
		ix.run(jobCtx, id)
		ix.mu.Lock()
		ix.cancel()
		ix.running, ix.cancel = "", nil
		ix.mu.Unlock()
		ix.notify()
	}
}

// next ждёт задание; ok=false — ctx закрыт.
func (ix *Indexer) next(ctx context.Context) (string, context.Context, bool) {
	for {
		ix.mu.Lock()
		if len(ix.queue) > 0 {
			id := ix.queue[0]
			ix.queue = ix.queue[1:]
			jobCtx, cancel := context.WithCancel(ctx)
			ix.running, ix.cancel = id, cancel
			ix.mu.Unlock()
			return id, jobCtx, true
		}
		// Задания, остановленные блокировкой, встают в очередь сами, когда
		// кончится пауза. Таймер — только пока такие есть.
		var retry <-chan time.Time
		if ix.hasBlockedLocked() {
			retry = ix.cfg.After(ix.br.BlockedUntil.Sub(ix.cfg.Now()))
		}
		ix.mu.Unlock()
		select {
		case <-ix.wake:
		case <-retry:
			ix.requeueBlocked()
		case <-ctx.Done():
			return "", nil, false
		}
	}
}

func (ix *Indexer) hasBlockedLocked() bool {
	for _, j := range ix.jobs {
		if j.State == StateBlocked {
			return true
		}
	}
	return false
}

// requeueBlocked ставит в очередь задания, остановленные блокировкой.
func (ix *Indexer) requeueBlocked() {
	ix.mu.Lock()
	var ids []string
	for id, j := range ix.jobs {
		if j.State == StateBlocked {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		ix.queue = append(ix.queue, id)
		ix.jobs[id].State = StateQueued
	}
	ix.mu.Unlock()
	if len(ids) > 0 {
		log.Printf("пауза после блокировки кончилась — снова в очереди: %v", ids)
		ix.notify()
	}
}

// Enqueue ставит канал в очередь с пределом видео limit. Уже стоящий или
// идущий канал второй раз не ставится. force снимает предохранитель:
// человек сам решил попробовать снова.
func (ix *Indexer) Enqueue(id string, limit int, force bool) {
	ix.mu.Lock()
	if force {
		ix.br.BlockedUntil, ix.br.Blocks = time.Time{}, 0
		ix.saveBreakerLocked()
	}
	ix.limits[id] = limit
	if ix.running == id || slices.Contains(ix.queue, id) {
		ix.mu.Unlock()
		return
	}
	ix.queue = append(ix.queue, id)
	ix.jobs[id] = &Job{ChannelID: id, State: StateQueued}
	ix.mu.Unlock()
	select {
	case ix.wake <- struct{}{}:
	default:
	}
	ix.notify()
}

// Cancel снимает канал с очереди или останавливает идущее задание.
func (ix *Indexer) Cancel(id string) {
	ix.mu.Lock()
	if i := slices.Index(ix.queue, id); i >= 0 {
		ix.queue = slices.Delete(ix.queue, i, i+1)
		ix.jobs[id].State = StateCancelled
	}
	// Остановленное блокировкой не встанет в очередь само.
	if j, ok := ix.jobs[id]; ok && j.State == StateBlocked {
		j.State = StateCancelled
	}
	if ix.running == id {
		ix.cancel()
	}
	ix.mu.Unlock()
	ix.notify()
}

// Forget убирает задание удалённого канала из списка.
func (ix *Indexer) Forget(id string) {
	ix.Cancel(id)
	ix.mu.Lock()
	delete(ix.jobs, id)
	delete(ix.limits, id)
	ix.mu.Unlock()
	ix.notify()
}

// Jobs — копии заданий по каналам.
func (ix *Indexer) Jobs() map[string]Job {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	out := make(map[string]Job, len(ix.jobs))
	for id, j := range ix.jobs {
		out[id] = *j
	}
	return out
}

// BlockedUntil — до какого времени действует предохранитель.
func (ix *Indexer) BlockedUntil() time.Time {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.br.BlockedUntil.After(ix.cfg.Now()) {
		return ix.br.BlockedUntil
	}
	return time.Time{}
}

// Restricted — DNS сети подменяет YouTube на Restricted Mode (по последней проверке).
func (ix *Indexer) Restricted() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.restricted
}

// CheckNetwork перепроверяет DNS: при запуске и перед каждым заданием —
// сеть могла смениться.
func (ix *Indexer) CheckNetwork(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	restricted := ix.checkNetwork(ctx)
	ix.mu.Lock()
	changed := ix.restricted != restricted
	ix.restricted = restricted
	ix.mu.Unlock()
	if changed {
		if restricted {
			log.Print("DNS сети включает Restricted Mode YouTube: часть видео будет недоступна")
		}
		ix.notify()
	}
}

// Subscribe — сигнал «что-то поменялось». Канал с буфером 1: частые
// изменения сливаются, подписчик берёт свежий снимок сам.
func (ix *Indexer) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	ix.mu.Lock()
	ix.subs[ch] = struct{}{}
	ix.mu.Unlock()
	return ch, func() {
		ix.mu.Lock()
		delete(ix.subs, ch)
		ix.mu.Unlock()
	}
}

func (ix *Indexer) notify() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for ch := range ix.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// update меняет задание под замком и оповещает подписчиков.
func (ix *Indexer) update(id string, f func(j *Job)) {
	ix.mu.Lock()
	j, ok := ix.jobs[id]
	if !ok {
		j = &Job{ChannelID: id}
		ix.jobs[id] = j
	}
	f(j)
	ix.mu.Unlock()
	ix.notify()
}

func (ix *Indexer) saveBreakerLocked() {
	if ix.cfg.StatePath == "" {
		return
	}
	raw, _ := json.Marshal(ix.br)
	if err := library.WriteAtomic(ix.cfg.StatePath, raw); err != nil {
		log.Printf("предохранитель не записан: %v", err)
	}
}

func (ix *Indexer) finish(id string, state State, errText string) {
	if err := ix.lib.Flush(); err != nil {
		log.Printf("запись библиотеки: %v", err)
	}
	ix.update(id, func(j *Job) {
		j.State, j.Error, j.Current = state, errText, ""
		j.CooldownUntil = time.Time{}
		j.FinishedAt = ix.cfg.Now()
	})
}

func (ix *Indexer) run(ctx context.Context, id string) {
	ix.mu.Lock()
	limit := ix.limits[id]
	ix.mu.Unlock()
	ix.update(id, func(j *Job) {
		*j = Job{ChannelID: id, State: StateListing, StartedAt: ix.cfg.Now()}
	})
	ix.CheckNetwork(ctx)

	if err := ix.list(ctx, id, limit); err != nil {
		if ctx.Err() != nil {
			ix.finish(id, StateCancelled, "")
			return
		}
		// Без списка можно доделать то, что уже известно (нет ключа, квота).
		if len(ix.lib.Todo(id)) == 0 {
			ix.finish(id, StateFailed, err.Error())
			return
		}
		log.Printf("список видео канала %s не обновлён: %v", id, err)
	}

	todo := ix.lib.Todo(id)
	ix.update(id, func(j *Job) { j.State, j.Total = StateRunning, len(todo) })

	for i := 0; i < len(todo); {
		v := todo[i]
		if err := ix.waitBreaker(ctx, id); err != nil {
			ix.finish(id, StateCancelled, "")
			return
		}
		ix.update(id, func(j *Job) { j.Current = v.Title })

		fetchCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		t, err := ix.tr.Fetch(fetchCtx, v.ID)
		cancel()
		if ctx.Err() != nil {
			ix.finish(id, StateCancelled, "")
			return
		}
		status := classify(err)
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		ix.lib.SetResult(id, v.ID, status, t, errText)

		if status == library.StatusBlocked {
			stop, until := ix.blocked()
			log.Printf("блокировка на %s (%s), пауза до %s", v.ID, errText, until.Format(time.TimeOnly))
			if stop {
				ix.finish(id, StateBlocked, errText)
				return
			}
			continue // то же видео после паузы
		}
		ix.unblocked()

		ix.update(id, func(j *Job) {
			j.Done++
			switch status {
			case library.StatusOK:
				j.OK++
			case library.StatusNoCaptions, library.StatusAge:
				j.NoCaptions++
			default:
				j.Failed++
			}
		})
		i++
		if i%ix.cfg.FlushEvery == 0 {
			if err := ix.lib.Flush(); err != nil {
				log.Printf("запись библиотеки: %v", err)
			}
		}
		if i < len(todo) {
			pause := ix.cfg.Delay + rand.N(ix.cfg.Jitter+1)
			if err := ix.cfg.Sleep(ctx, pause); err != nil {
				ix.finish(id, StateCancelled, "")
				return
			}
		}
	}
	ix.lib.MarkIndexed(id)
	ix.finish(id, StateDone, "")
}

// list обновляет данные канала и список его видео.
func (ix *Indexer) list(ctx context.Context, id string, limit int) error {
	ch, err := ix.yt.Channel(ctx, id)
	if err != nil {
		return err
	}
	if err := ix.lib.Put(ch, limit); err != nil {
		return err
	}
	videos, err := ix.yt.Uploads(ctx, ch.Uploads, limit)
	if err != nil {
		return err
	}
	return ix.lib.Merge(id, videos)
}

// waitBreaker ждёт конца общей паузы, показывая её в задании.
func (ix *Indexer) waitBreaker(ctx context.Context, id string) error {
	until := ix.BlockedUntil()
	if until.IsZero() {
		return nil
	}
	ix.update(id, func(j *Job) { j.State, j.CooldownUntil = StateCooling, until })
	if err := ix.cfg.Sleep(ctx, until.Sub(ix.cfg.Now())); err != nil {
		return err
	}
	ix.update(id, func(j *Job) { j.State, j.CooldownUntil = StateRunning, time.Time{} })
	return nil
}

// blocked отмечает блокировку: пауза растёт вдвое, после MaxBlocks подряд —
// остановка задания и долгая пауза.
func (ix *Indexer) blocked() (stop bool, until time.Time) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.br.Blocks++
	pause := ix.cfg.Cooldown << (ix.br.Blocks - 1)
	stop = ix.br.Blocks >= ix.cfg.MaxBlocks
	if stop {
		pause = ix.cfg.BlockPause
	}
	ix.br.BlockedUntil = ix.cfg.Now().Add(pause)
	ix.saveBreakerLocked()
	return stop, ix.br.BlockedUntil
}

func (ix *Indexer) unblocked() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.br.Blocks != 0 || !ix.br.BlockedUntil.IsZero() {
		ix.br.Blocks, ix.br.BlockedUntil = 0, time.Time{}
		ix.saveBreakerLocked()
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
// перезапуск: остались ни разу не скачанные видео или канал добавлен, но
// ещё ни разу не проиндексирован (список видео не получен).
func (ix *Indexer) ResumePending() {
	for _, s := range ix.lib.Summaries() {
		if s.Counts.Pending > 0 || s.IndexedAt.IsZero() {
			ix.Enqueue(s.ID, s.Limit, false)
		}
	}
}
