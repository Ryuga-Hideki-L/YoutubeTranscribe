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

// fakeLister отдаёт каналы по n видео. Поле err — ошибка получения списка.
type fakeLister struct {
	n   int
	err error
}

func (lister *fakeLister) HasKey() bool { return true }

func (lister *fakeLister) Channel(_ context.Context, id string) (youtube.Channel, error) {
	if lister.err != nil {
		return youtube.Channel{}, lister.err
	}
	return youtube.Channel{ID: id, Title: "Канал " + id, Uploads: "UU" + id}, nil
}

func (lister *fakeLister) Uploads(_ context.Context, playlist string, limit int) ([]youtube.Video, error) {
	var out []youtube.Video
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range min(lister.n, limit) {
		out = append(out, youtube.Video{
			ID: fmt.Sprintf("%s-%d", playlist, i), Title: fmt.Sprint("Видео ", i),
			Published: base.Add(-time.Duration(i) * time.Hour), Live: "none",
		})
	}
	return out, nil
}

// fakeFetcher отвечает по сценарию. errs[i] — ответ на i-й вызов, nil означает успех.
type fakeFetcher struct {
	mu       sync.Mutex
	errs     []error
	calls    []string
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	hold     chan struct{} // если не nil, каждый вызов ждёт его
}

func (fetcher *fakeFetcher) Fetch(ctx context.Context, id string) (*transcript.Transcript, error) {
	running := fetcher.inFlight.Add(1)
	defer fetcher.inFlight.Add(-1)
	if running > fetcher.maxSeen.Load() {
		fetcher.maxSeen.Store(running)
	}
	if fetcher.hold != nil {
		select {
		case <-fetcher.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	fetcher.mu.Lock()
	i := len(fetcher.calls)
	fetcher.calls = append(fetcher.calls, id)
	fetcher.mu.Unlock()
	if i < len(fetcher.errs) && fetcher.errs[i] != nil {
		return nil, fetcher.errs[i]
	}
	return &transcript.Transcript{Lang: "ru", Lines: []transcript.Line{{Text: "текст " + id}}}, nil
}

// fakeClock — поддельное время. Sleep сразу сдвигает его вперёд.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
	block  chan struct{} // если не nil, Sleep ждёт отмены
	fire   bool          // After срабатывает сразу
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

// After по умолчанию не срабатывает никогда. Если включён fire, срабатывает
// сразу и сдвигает время.
func (clock *fakeClock) After(duration time.Duration) <-chan time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	if !clock.fire {
		return nil
	}
	clock.now = clock.now.Add(duration)
	fired := make(chan time.Time, 1)
	fired <- clock.now
	return fired
}

func (clock *fakeClock) Sleep(ctx context.Context, duration time.Duration) error {
	if clock.block != nil {
		<-ctx.Done()
		return ctx.Err()
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.sleeps = append(clock.sleeps, duration)
	clock.mu.Unlock()
	return ctx.Err()
}

func setup(t *testing.T, lister Lister, fetcher Fetcher, clock *fakeClock) (*Indexer, *library.Library, string) {
	dir := t.TempDir()
	lib, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Каналы заводит api.add через lib.Put до постановки в очередь.
	// Индексатор их только обновляет.
	for _, id := range []string{"A", "B"} {
		if err := lib.Put(youtube.Channel{ID: id, Title: "Канал " + id, Uploads: "UU" + id}, 10); err != nil {
			t.Fatal(err)
		}
	}
	statePath := filepath.Join(dir, "state.json")
	indexer := New(lib, lister, fetcher, Config{StatePath: statePath, Now: clock.Now, Sleep: clock.Sleep, After: clock.After})
	indexer.checkNetwork = func(context.Context) bool { return false } // тесты без сети
	return indexer, lib, statePath
}

// runUntil запускает рабочего и ждёт, пока все задания придут в конечное состояние.
func runUntil(t *testing.T, indexer *Indexer, ids ...string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { indexer.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.After(5 * time.Second)
	for {
		finished := 0
		for _, id := range ids {
			switch indexer.Jobs()[id].State {
			case StateDone, StateBlocked, StateCancelled, StateFailed:
				finished++
			}
		}
		if finished == len(ids) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("задания не закончились: %+v", indexer.Jobs())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestIndexesChannel(t *testing.T) {
	clock := newFakeClock()
	fetcher := &fakeFetcher{errs: []error{nil, transcript.ErrNoTranscript, nil, transcript.ErrUnavailable}}
	indexer, lib, _ := setup(t, &fakeLister{n: 4}, fetcher, clock)
	indexer.Enqueue("A", 10, false)
	runUntil(t, indexer, "A")

	job := indexer.Jobs()["A"]
	if job.State != StateDone || job.Total != 4 || job.Done != 4 || job.OK != 2 || job.NoCaptions != 1 || job.Failed != 1 {
		t.Fatalf("задание: %+v", job)
	}
	// Пауз между видео три, после последнего не ждём.
	if len(clock.sleeps) != 3 {
		t.Errorf("паузы: %v", clock.sleeps)
	}
	summary, _ := lib.Summary("A")
	if summary.Counts.OK != 2 || summary.IndexedAt.IsZero() {
		t.Errorf("библиотека: %+v", summary)
	}
	// Повтор: недоступное видео снова в работе, остальные нет.
	fetcher.errs = nil
	fetcher.calls = nil
	indexer.Enqueue("A", 10, false)
	runUntil(t, indexer, "A")
	if len(fetcher.calls) != 1 || fetcher.calls[0] != "UUA-3" {
		t.Errorf("повторная индексация скачала %v, ожидалось только UUA-3", fetcher.calls)
	}
}

func TestBlockCooldownThenRecover(t *testing.T) {
	clock := newFakeClock()
	fetcher := &fakeFetcher{errs: []error{nil, transcript.ErrBlocked, nil}}
	indexer, _, _ := setup(t, &fakeLister{n: 2}, fetcher, clock)
	indexer.Enqueue("A", 10, false)
	runUntil(t, indexer, "A")

	job := indexer.Jobs()["A"]
	if job.State != StateDone || job.OK != 2 {
		t.Fatalf("задание: %+v", job)
	}
	// То же видео повторено после паузы в минуту.
	if fmt.Sprint(fetcher.calls) != "[UUA-0 UUA-1 UUA-1]" {
		t.Errorf("вызовы: %v", fetcher.calls)
	}
	var cooled bool
	for _, pause := range clock.sleeps {
		cooled = cooled || pause == time.Minute
	}
	if !cooled {
		t.Errorf("не было паузы в минуту: %v", clock.sleeps)
	}
	if !indexer.BlockedUntil().IsZero() {
		t.Error("предохранитель не сброшен после успеха")
	}
}

func TestStopsAfterRepeatedBlocksAndRemembers(t *testing.T) {
	clock := newFakeClock()
	errBlocked := transcript.ErrBlocked
	fetcher := &fakeFetcher{errs: []error{errBlocked, errBlocked, errBlocked, errBlocked}}
	indexer, lib, statePath := setup(t, &fakeLister{n: 3}, fetcher, clock)
	indexer.Enqueue("A", 10, false)
	runUntil(t, indexer, "A")

	if job := indexer.Jobs()["A"]; job.State != StateBlocked {
		t.Fatalf("задание: %+v", job)
	}
	if len(fetcher.calls) != 3 {
		t.Errorf("после %d блокировок подряд надо остановиться, было вызовов: %d", 3, len(fetcher.calls))
	}
	// Перезапуск: новый индексатор читает предохранитель с диска.
	again := New(lib, &fakeLister{n: 3}, fetcher, Config{StatePath: statePath, Now: clock.Now, Sleep: clock.Sleep})
	again.checkNetwork = indexer.checkNetwork
	until := again.BlockedUntil()
	if want := clock.Now().Add(30 * time.Minute); !until.Equal(want) {
		t.Errorf("предохранитель после перезапуска: %v, want %v", until, want)
	}
	// Повтор «сейчас» снимает его.
	again.Enqueue("A", 10, true)
	if !again.BlockedUntil().IsZero() {
		t.Error("force не снял предохранитель")
	}
}

func TestCancelDuringSleep(t *testing.T) {
	clock := newFakeClock()
	clock.block = make(chan struct{})
	indexer, _, _ := setup(t, &fakeLister{n: 5}, &fakeFetcher{}, clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go indexer.Run(ctx)
	indexer.Enqueue("A", 10, false)
	for indexer.Jobs()["A"].Done < 1 {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	indexer.Cancel("A")
	for indexer.Jobs()["A"].State != StateCancelled {
		if time.Since(start) > 2*time.Second {
			t.Fatalf("отмена не сработала: %+v", indexer.Jobs()["A"])
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOneFetchAtATime(t *testing.T) {
	clock := newFakeClock()
	fetcher := &fakeFetcher{}
	indexer, _, _ := setup(t, &fakeLister{n: 5}, fetcher, clock)
	indexer.Enqueue("A", 10, false)
	indexer.Enqueue("B", 10, false)
	indexer.Enqueue("A", 10, false) // повтор не ставится второй раз
	runUntil(t, indexer, "A", "B")
	if fetcher.maxSeen.Load() != 1 {
		t.Errorf("одновременных загрузок: %d", fetcher.maxSeen.Load())
	}
	if len(fetcher.calls) != 10 {
		t.Errorf("вызовов %d, ожидалось 10", len(fetcher.calls))
	}
}

func TestListingFailure(t *testing.T) {
	clock := newFakeClock()
	indexer, _, _ := setup(t, &fakeLister{err: youtube.ErrNoKey}, &fakeFetcher{}, clock)
	indexer.Enqueue("A", 10, false)
	runUntil(t, indexer, "A")
	if job := indexer.Jobs()["A"]; job.State != StateFailed || job.Error != youtube.ErrNoKey.Error() {
		t.Fatalf("задание: %+v", job)
	}
}

func TestRestrictedModeIsReported(t *testing.T) {
	clock := newFakeClock()
	indexer, _, _ := setup(t, &fakeLister{n: 1}, &fakeFetcher{}, clock)
	changed, unsubscribe := indexer.Subscribe()
	defer unsubscribe()
	indexer.checkNetwork = func(context.Context) bool { return true }
	indexer.Enqueue("A", 10, false)
	<-changed
	runUntil(t, indexer, "A")
	if !indexer.Restricted() {
		t.Error("Restricted Mode не замечен перед заданием")
	}
}

func TestBlockedJobRetriesAfterPause(t *testing.T) {
	clock := newFakeClock()
	errBlocked := transcript.ErrBlocked
	fetcher := &fakeFetcher{errs: []error{errBlocked, errBlocked, errBlocked}}
	indexer, lib, _ := setup(t, &fakeLister{n: 2}, fetcher, clock)
	indexer.Enqueue("A", 10, false)
	runUntil(t, indexer, "A")
	if job := indexer.Jobs()["A"]; job.State != StateBlocked {
		t.Fatalf("задание: %+v", job)
	}
	// Пауза кончилась. Задание встаёт само и доделывает видео.
	clock.mu.Lock()
	clock.fire = true
	clock.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go indexer.Run(ctx)
	deadline := time.After(5 * time.Second)
	for indexer.Jobs()["A"].State != StateDone {
		select {
		case <-deadline:
			t.Fatalf("задание не возобновилось: %+v", indexer.Jobs()["A"])
		case <-time.After(time.Millisecond):
		}
	}
	if summary, _ := lib.Summary("A"); summary.Counts.OK != 2 {
		t.Errorf("после возобновления: %+v", summary.Counts)
	}
}

func TestCancelledBlockedJobStaysCancelled(t *testing.T) {
	clock := newFakeClock()
	errBlocked := transcript.ErrBlocked
	indexer, _, _ := setup(t, &fakeLister{n: 1}, &fakeFetcher{errs: []error{errBlocked, errBlocked, errBlocked}}, clock)
	indexer.Enqueue("A", 10, false)
	runUntil(t, indexer, "A")
	indexer.Cancel("A")
	indexer.mu.Lock()
	hasBlocked := indexer.hasBlockedLocked()
	indexer.mu.Unlock()
	if hasBlocked || indexer.Jobs()["A"].State != StateCancelled {
		t.Fatalf("отменённое заблокированное задание: %+v", indexer.Jobs()["A"])
	}
}

func TestResumeChannelNeverListed(t *testing.T) {
	clock := newFakeClock()
	fetcher := &fakeFetcher{}
	indexer, lib, _ := setup(t, &fakeLister{n: 2}, fetcher, clock)
	// Канал добавлен через Put, но список видео не получен. Так бывает перед перезапуском.
	lib.Put(youtube.Channel{ID: "B", Title: "B", Uploads: "UUB"}, 10)
	indexer.ResumePending()
	runUntil(t, indexer, "B")
	if job := indexer.Jobs()["B"]; job.State != StateDone || job.OK != 2 {
		t.Fatalf("задание: %+v", job)
	}
}

func TestNetworkOutageKeepsVideosPending(t *testing.T) {
	clock := newFakeClock()
	netErr := fmt.Errorf("%w: dial tcp: no route to host", transcript.ErrNetwork)
	errs := make([]error, 10)
	for i := range errs {
		errs[i] = netErr
	}
	indexer, lib, _ := setup(t, &fakeLister{n: 5}, &fakeFetcher{errs: errs}, clock)
	indexer.Enqueue("A", 10, false)
	runUntil(t, indexer, "A")

	job := indexer.Jobs()["A"]
	if job.State != StateFailed || job.Reason != ReasonNetwork || job.Failed != 0 || job.Done != 0 {
		t.Fatalf("задание: %+v", job)
	}
	summary, _ := lib.Summary("A")
	if summary.Counts.Pending != 5 || !summary.IndexedAt.IsZero() {
		t.Errorf("видео испорчены сбоем сети: %+v, indexedAt %v", summary.Counts, summary.IndexedAt)
	}
	// Паузы растут: 10, 20, 40, 80 с. После пятого сбоя задание останавливается.
	if fmt.Sprint(clock.sleeps) != "[10s 20s 40s 1m20s]" {
		t.Errorf("паузы: %v", clock.sleeps)
	}
}

func TestNetworkBlipRecovers(t *testing.T) {
	clock := newFakeClock()
	netErr := fmt.Errorf("%w: timeout", transcript.ErrNetwork)
	fetcher := &fakeFetcher{errs: []error{nil, netErr, nil}}
	indexer, lib, _ := setup(t, &fakeLister{n: 2}, fetcher, clock)
	indexer.Enqueue("A", 10, false)
	runUntil(t, indexer, "A")
	if job := indexer.Jobs()["A"]; job.State != StateDone || job.OK != 2 || job.Failed != 0 {
		t.Fatalf("задание: %+v", job)
	}
	if summary, _ := lib.Summary("A"); summary.Counts.OK != 2 {
		t.Errorf("сводка: %+v", summary.Counts)
	}
}

// gatedLister ждёт release перед ответом Channel. В это окно успевает прийти
// удаление канала.
type gatedLister struct {
	fakeLister
	entered, release chan struct{}
}

func (gated *gatedLister) Channel(ctx context.Context, id string) (youtube.Channel, error) {
	close(gated.entered)
	<-gated.release
	return gated.fakeLister.Channel(ctx, id)
}

func TestDeleteDuringListingDoesNotResurrect(t *testing.T) {
	clock := newFakeClock()
	gated := &gatedLister{fakeLister: fakeLister{n: 2}, entered: make(chan struct{}), release: make(chan struct{})}
	indexer, lib, _ := setup(t, gated, &fakeFetcher{}, clock)
	lib.Put(youtube.Channel{ID: "A", Title: "A", Uploads: "UUA"}, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { indexer.Run(ctx); close(done) }()
	indexer.Enqueue("A", 10, false)
	<-gated.entered
	// Удаление, как в api.remove: забыть задание, убрать канал.
	indexer.Forget("A")
	if err := lib.Remove("A"); err != nil {
		t.Fatal(err)
	}
	close(gated.release)
	// Рабочий доделывает задание. Ждём, пока он снова свободен.
	for {
		indexer.mu.Lock()
		idle := indexer.running == ""
		indexer.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := lib.Summary("A"); ok {
		t.Error("удалённый канал вернулся в библиотеку")
	}
	if job, ok := indexer.Jobs()["A"]; ok {
		t.Errorf("задание-призрак: %+v", job)
	}
	cancel()
	<-done
}
