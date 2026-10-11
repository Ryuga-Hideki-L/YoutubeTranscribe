package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/library"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/transcript"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/youtube"
)

// fakeLister — каналы с n видео каждый; err — ошибка списка.
type fakeLister struct {
	n   int
	err error
}

func (f *fakeLister) HasKey() bool { return true }

func (f *fakeLister) Channel(_ context.Context, id string) (youtube.Channel, error) {
	if f.err != nil {
		return youtube.Channel{}, f.err
	}
	return youtube.Channel{ID: id, Title: "Канал " + id, Uploads: "UU" + id}, nil
}

func (f *fakeLister) Uploads(_ context.Context, playlist string, limit int) ([]youtube.Video, error) {
	var out []youtube.Video
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range min(f.n, limit) {
		out = append(out, youtube.Video{
			ID: fmt.Sprintf("%s-%d", playlist, i), Title: fmt.Sprint("Видео ", i),
			Published: base.Add(-time.Duration(i) * time.Hour), Live: "none",
		})
	}
	return out, nil
}

// fakeFetcher отвечает по сценарию: errs[i] — ответ на i-й вызов (nil — успех).
type fakeFetcher struct {
	mu       sync.Mutex
	errs     []error
	calls    []string
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	hold     chan struct{} // если не nil — каждый вызов ждёт его
}

func (f *fakeFetcher) Fetch(ctx context.Context, id string) (*transcript.Transcript, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	if n > f.maxSeen.Load() {
		f.maxSeen.Store(n)
	}
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	i := len(f.calls)
	f.calls = append(f.calls, id)
	f.mu.Unlock()
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	return &transcript.Transcript{Lang: "ru", Lines: []transcript.Line{{Text: "текст " + id}}}, nil
}

// clock — поддельное время: Sleep сразу сдвигает его вперёд.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
	block  chan struct{} // если не nil — Sleep ждёт отмены
	fire   bool          // After срабатывает сразу
}

func newClock() *clock { return &clock{now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After по умолчанию не срабатывает никогда; fire — сработать сразу,
// сдвинув время.
func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.fire {
		return nil
	}
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	if c.block != nil {
		<-ctx.Done()
		return ctx.Err()
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	return ctx.Err()
}

func setup(t *testing.T, l Lister, f Fetcher, c *clock) (*Indexer, *library.Library, string) {
	dir := t.TempDir()
	lib, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Каналы заводит api.add (lib.Put) до постановки в очередь; индексатор
	// их только обновляет.
	for _, id := range []string{"A", "B"} {
		if err := lib.Put(youtube.Channel{ID: id, Title: "Канал " + id, Uploads: "UU" + id}, 10); err != nil {
			t.Fatal(err)
		}
	}
	statePath := filepath.Join(dir, "state.json")
	ix := New(lib, l, f, Config{StatePath: statePath, Now: c.Now, Sleep: c.Sleep, After: c.After})
	ix.checkNetwork = func(context.Context) bool { return false } // тесты без сети
	return ix, lib, statePath
}

// runUntil запускает рабочего и ждёт, пока все задания придут в конечное состояние.
func runUntil(t *testing.T, ix *Indexer, ids ...string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { ix.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.After(5 * time.Second)
	for {
		finished := 0
		for _, id := range ids {
			switch ix.Jobs()[id].State {
			case StateDone, StateBlocked, StateCancelled, StateFailed:
				finished++
			}
		}
		if finished == len(ids) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("задания не закончились: %+v", ix.Jobs())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestIndexesChannel(t *testing.T) {
	c := newClock()
	f := &fakeFetcher{errs: []error{nil, transcript.ErrNoTranscript, nil, transcript.ErrUnavailable}}
	ix, lib, _ := setup(t, &fakeLister{n: 4}, f, c)
	ix.Enqueue("A", 10, false)
	runUntil(t, ix, "A")

	j := ix.Jobs()["A"]
	if j.State != StateDone || j.Total != 4 || j.Done != 4 || j.OK != 2 || j.NoCaptions != 1 || j.Failed != 1 {
		t.Fatalf("задание: %+v", j)
	}
	// Пауза между видео — три, после последнего не ждём.
	if len(c.sleeps) != 3 {
		t.Errorf("паузы: %v", c.sleeps)
	}
	s, _ := lib.Summary("A")
	if s.Counts.OK != 2 || s.IndexedAt.IsZero() {
		t.Errorf("библиотека: %+v", s)
	}
	// Повтор: недоступное видео снова в работе, остальные — нет.
	f.errs = nil
	f.calls = nil
	ix.Enqueue("A", 10, false)
	runUntil(t, ix, "A")
	if len(f.calls) != 1 || f.calls[0] != "UUA-3" {
		t.Errorf("повторная индексация скачала %v, ожидалось только UUA-3", f.calls)
	}
}

func TestBlockCooldownThenRecover(t *testing.T) {
	c := newClock()
	f := &fakeFetcher{errs: []error{nil, transcript.ErrBlocked, nil}}
	ix, _, _ := setup(t, &fakeLister{n: 2}, f, c)
	ix.Enqueue("A", 10, false)
	runUntil(t, ix, "A")

	j := ix.Jobs()["A"]
	if j.State != StateDone || j.OK != 2 {
		t.Fatalf("задание: %+v", j)
	}
	// То же видео повторено после паузы в минуту.
	if fmt.Sprint(f.calls) != "[UUA-0 UUA-1 UUA-1]" {
		t.Errorf("вызовы: %v", f.calls)
	}
	var cooled bool
	for _, d := range c.sleeps {
		cooled = cooled || d == time.Minute
	}
	if !cooled {
		t.Errorf("не было паузы в минуту: %v", c.sleeps)
	}
	if !ix.BlockedUntil().IsZero() {
		t.Error("предохранитель не сброшен после успеха")
	}
}

func TestStopsAfterRepeatedBlocksAndRemembers(t *testing.T) {
	c := newClock()
	b := transcript.ErrBlocked
	f := &fakeFetcher{errs: []error{b, b, b, b}}
	ix, lib, statePath := setup(t, &fakeLister{n: 3}, f, c)
	ix.Enqueue("A", 10, false)
	runUntil(t, ix, "A")

	if j := ix.Jobs()["A"]; j.State != StateBlocked {
		t.Fatalf("задание: %+v", j)
	}
	if len(f.calls) != 3 {
		t.Errorf("после %d блокировок подряд надо остановиться, было вызовов: %d", 3, len(f.calls))
	}
	// Перезапуск: новый индексатор читает предохранитель с диска.
	again := New(lib, &fakeLister{n: 3}, f, Config{StatePath: statePath, Now: c.Now, Sleep: c.Sleep})
	again.checkNetwork = ix.checkNetwork
	until := again.BlockedUntil()
	if want := c.Now().Add(30 * time.Minute); !until.Equal(want) {
		t.Errorf("предохранитель после перезапуска: %v, want %v", until, want)
	}
	// Повтор «сейчас» снимает его.
	again.Enqueue("A", 10, true)
	if !again.BlockedUntil().IsZero() {
		t.Error("force не снял предохранитель")
	}
}

func TestCancelDuringSleep(t *testing.T) {
	c := newClock()
	c.block = make(chan struct{})
	ix, _, _ := setup(t, &fakeLister{n: 5}, &fakeFetcher{}, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.Run(ctx)
	ix.Enqueue("A", 10, false)
	for ix.Jobs()["A"].Done < 1 {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	ix.Cancel("A")
	for ix.Jobs()["A"].State != StateCancelled {
		if time.Since(start) > 2*time.Second {
			t.Fatalf("отмена не сработала: %+v", ix.Jobs()["A"])
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOneFetchAtATime(t *testing.T) {
	c := newClock()
	f := &fakeFetcher{}
	ix, _, _ := setup(t, &fakeLister{n: 5}, f, c)
	ix.Enqueue("A", 10, false)
	ix.Enqueue("B", 10, false)
	ix.Enqueue("A", 10, false) // повтор не ставится второй раз
	runUntil(t, ix, "A", "B")
	if f.maxSeen.Load() != 1 {
		t.Errorf("одновременных загрузок: %d", f.maxSeen.Load())
	}
	if len(f.calls) != 10 {
		t.Errorf("вызовов %d, ожидалось 10", len(f.calls))
	}
}

func TestListingFailure(t *testing.T) {
	c := newClock()
	ix, _, _ := setup(t, &fakeLister{err: youtube.ErrNoKey}, &fakeFetcher{}, c)
	ix.Enqueue("A", 10, false)
	runUntil(t, ix, "A")
	if j := ix.Jobs()["A"]; j.State != StateFailed || j.Error != youtube.ErrNoKey.Error() {
		t.Fatalf("задание: %+v", j)
	}
}

func TestRestrictedModeIsReported(t *testing.T) {
	c := newClock()
	ix, _, _ := setup(t, &fakeLister{n: 1}, &fakeFetcher{}, c)
	changed, unsubscribe := ix.Subscribe()
	defer unsubscribe()
	ix.checkNetwork = func(context.Context) bool { return true }
	ix.Enqueue("A", 10, false)
	<-changed
	runUntil(t, ix, "A")
	if !ix.Restricted() {
		t.Error("Restricted Mode не замечен перед заданием")
	}
}

func TestBlockedJobRetriesAfterPause(t *testing.T) {
	c := newClock()
	b := transcript.ErrBlocked
	f := &fakeFetcher{errs: []error{b, b, b}}
	ix, lib, _ := setup(t, &fakeLister{n: 2}, f, c)
	ix.Enqueue("A", 10, false)
	runUntil(t, ix, "A")
	if j := ix.Jobs()["A"]; j.State != StateBlocked {
		t.Fatalf("задание: %+v", j)
	}
	// Пауза кончилась — задание встаёт само и доделывает видео.
	c.mu.Lock()
	c.fire = true
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.Run(ctx)
	deadline := time.After(5 * time.Second)
	for ix.Jobs()["A"].State != StateDone {
		select {
		case <-deadline:
			t.Fatalf("задание не возобновилось: %+v", ix.Jobs()["A"])
		case <-time.After(time.Millisecond):
		}
	}
	if s, _ := lib.Summary("A"); s.Counts.OK != 2 {
		t.Errorf("после возобновления: %+v", s.Counts)
	}
}

func TestCancelledBlockedJobStaysCancelled(t *testing.T) {
	c := newClock()
	b := transcript.ErrBlocked
	ix, _, _ := setup(t, &fakeLister{n: 1}, &fakeFetcher{errs: []error{b, b, b}}, c)
	ix.Enqueue("A", 10, false)
	runUntil(t, ix, "A")
	ix.Cancel("A")
	ix.mu.Lock()
	has := ix.hasBlockedLocked()
	ix.mu.Unlock()
	if has || ix.Jobs()["A"].State != StateCancelled {
		t.Fatalf("отменённое заблокированное задание: %+v", ix.Jobs()["A"])
	}
}

func TestResumeChannelNeverListed(t *testing.T) {
	c := newClock()
	f := &fakeFetcher{}
	ix, lib, _ := setup(t, &fakeLister{n: 2}, f, c)
	// Канал добавлен (Put), но список видео не получен — как перед перезапуском.
	lib.Put(youtube.Channel{ID: "B", Title: "B", Uploads: "UUB"}, 10)
	ix.ResumePending()
	runUntil(t, ix, "B")
	if j := ix.Jobs()["B"]; j.State != StateDone || j.OK != 2 {
		t.Fatalf("задание: %+v", j)
	}
}

func TestNetworkOutageKeepsVideosPending(t *testing.T) {
	c := newClock()
	netErr := fmt.Errorf("%w: dial tcp: no route to host", transcript.ErrNetwork)
	errs := make([]error, 10)
	for i := range errs {
		errs[i] = netErr
	}
	ix, lib, _ := setup(t, &fakeLister{n: 5}, &fakeFetcher{errs: errs}, c)
	ix.Enqueue("A", 10, false)
	runUntil(t, ix, "A")

	j := ix.Jobs()["A"]
	if j.State != StateFailed || j.Reason != ReasonNetwork || j.Failed != 0 || j.Done != 0 {
		t.Fatalf("задание: %+v", j)
	}
	s, _ := lib.Summary("A")
	if s.Counts.Pending != 5 || !s.IndexedAt.IsZero() {
		t.Errorf("видео испорчены сбоем сети: %+v, indexedAt %v", s.Counts, s.IndexedAt)
	}
	// Паузы растут: 10, 20, 40, 80 с, после пятого сбоя — остановка.
	if fmt.Sprint(c.sleeps) != "[10s 20s 40s 1m20s]" {
		t.Errorf("паузы: %v", c.sleeps)
	}
}

func TestNetworkBlipRecovers(t *testing.T) {
	c := newClock()
	netErr := fmt.Errorf("%w: timeout", transcript.ErrNetwork)
	f := &fakeFetcher{errs: []error{nil, netErr, nil}}
	ix, lib, _ := setup(t, &fakeLister{n: 2}, f, c)
	ix.Enqueue("A", 10, false)
	runUntil(t, ix, "A")
	if j := ix.Jobs()["A"]; j.State != StateDone || j.OK != 2 || j.Failed != 0 {
		t.Fatalf("задание: %+v", j)
	}
	if s, _ := lib.Summary("A"); s.Counts.OK != 2 {
		t.Errorf("сводка: %+v", s.Counts)
	}
}

// gatedLister ждёт release перед ответом Channel — окно, в которое
// успевает прийти удаление канала.
type gatedLister struct {
	fakeLister
	entered, release chan struct{}
}

func (g *gatedLister) Channel(ctx context.Context, id string) (youtube.Channel, error) {
	close(g.entered)
	<-g.release
	return g.fakeLister.Channel(ctx, id)
}

func TestDeleteDuringListingDoesNotResurrect(t *testing.T) {
	c := newClock()
	g := &gatedLister{fakeLister: fakeLister{n: 2}, entered: make(chan struct{}), release: make(chan struct{})}
	ix, lib, _ := setup(t, g, &fakeFetcher{}, c)
	lib.Put(youtube.Channel{ID: "A", Title: "A", Uploads: "UUA"}, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { ix.Run(ctx); close(done) }()
	ix.Enqueue("A", 10, false)
	<-g.entered
	// Удаление, как в api.remove: забыть задание, убрать канал.
	ix.Forget("A")
	if err := lib.Remove("A"); err != nil {
		t.Fatal(err)
	}
	close(g.release)
	// Рабочий доделывает задание — ждём, пока он снова свободен.
	for {
		ix.mu.Lock()
		idle := ix.running == ""
		ix.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := lib.Summary("A"); ok {
		t.Error("удалённый канал вернулся в библиотеку")
	}
	if j, ok := ix.Jobs()["A"]; ok {
		t.Errorf("задание-призрак: %+v", j)
	}
	cancel()
	<-done
}
