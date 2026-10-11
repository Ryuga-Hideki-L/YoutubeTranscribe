package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/transcript"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/youtube"
)

var bbc = youtube.Channel{ID: "UCbbc", Title: "BBC News", Uploads: "UUbbc"}

func transcriptOf(texts ...string) *transcript.Transcript {
	captions := &transcript.Transcript{Lang: "ru", Auto: true}
	for i, text := range texts {
		captions.Lines = append(captions.Lines, transcript.Line{Start: float64(i * 2), Dur: 2, Text: text})
	}
	return captions
}

func newLib(t *testing.T) (*Library, string) {
	dir := t.TempDir()
	lib, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := lib.Put(bbc, 200); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	err = lib.Merge(bbc.ID, []youtube.Video{
		{ID: "v1", Title: "Первое", Published: day},
		{ID: "v2", Title: "Второе", Published: day.Add(24 * time.Hour)},
		{ID: "v3", Title: "Третье", Published: day.Add(48 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return lib, dir
}

func TestRoundTripAndTodo(t *testing.T) {
	lib, dir := newLib(t)
	lib.SetResult(bbc.ID, "v1", StatusOK, transcriptOf("Привет, мир"), "")
	lib.SetResult(bbc.ID, "v2", StatusBlocked, nil, "капча")
	if err := lib.Flush(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	summary, ok := again.Summary(bbc.ID)
	if !ok {
		t.Fatal("канал пропал после перезапуска")
	}
	if summary.Counts != (Counts{Total: 3, OK: 1, Failed: 1, Pending: 1, Lines: 1}) {
		t.Errorf("сводка: %+v", summary.Counts)
	}
	var todo []string
	for _, video := range again.Todo(bbc.ID) {
		todo = append(todo, video.ID)
	}
	// Блокировка не финальна, поэтому видео снова в очереди. Порядок от новых к старым.
	if strings.Join(todo, " ") != "v3 v2" {
		t.Errorf("todo = %v, want [v3 v2]", todo)
	}
	if result := again.Search("мир", ""); result.TotalHits != 1 {
		t.Errorf("поиск после перезапуска: %+v", result)
	}
}

func TestMergeKeepsTranscriptsAndOldVideos(t *testing.T) {
	lib, _ := newLib(t)
	lib.SetResult(bbc.ID, "v1", StatusOK, transcriptOf("старое видео"), "")
	lib.SetResult(bbc.ID, "v2", StatusOK, transcriptOf("второе видео"), "")
	// Новый список: v2 переименовано, v1 выпало (сверх предела), появилось v4.
	if err := lib.Merge(bbc.ID, []youtube.Video{{ID: "v4"}, {ID: "v2", Title: "Новое имя"}}); err != nil {
		t.Fatal(err)
	}
	result := lib.Search("видео", "")
	got := map[string]string{}
	for _, videoHits := range result.Videos {
		got[videoHits.ID] = videoHits.Title
	}
	if len(got) != 2 || got["v2"] != "Новое имя" || got["v1"] != "Первое" {
		t.Errorf("после слияния: %v", got)
	}
}

func TestCorruptFileIsKeptAside(t *testing.T) {
	dir := t.TempDir()
	channelsDir := filepath.Join(dir, "channels")
	os.MkdirAll(channelsDir, 0o755)
	os.WriteFile(filepath.Join(channelsDir, "UCbad.json"), []byte(`{"version":1,"chan`), 0o644)
	os.WriteFile(filepath.Join(channelsDir, "UCold.json.tmp-123"), []byte(`мусор`), 0o644)

	lib, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Summaries()) != 0 {
		t.Error("битый канал загружен")
	}
	entries, _ := os.ReadDir(channelsDir)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "UCbad.json.corrupt-") || strings.Contains(joined, "UCbad.json ") {
		t.Errorf("битый файл не отложен: %v", names)
	}
}

func TestRemove(t *testing.T) {
	lib, dir := newLib(t)
	lib.SetResult(bbc.ID, "v1", StatusOK, transcriptOf("текст"), "")
	if err := lib.Remove(bbc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "channels", bbc.ID+".json")); !os.IsNotExist(err) {
		t.Error("файл канала остался")
	}
	if result := lib.Search("текст", ""); result.TotalHits != 0 {
		t.Error("удалённый канал всё ещё ищется")
	}
}

func TestSearch(t *testing.T) {
	lib, _ := newLib(t)
	lib.SetResult(bbc.ID, "v1", StatusOK, transcriptOf(
		"Ёлка стоит в углу",
		"а потом мы пошли",
		"в магазин за хлебом",
		"и снова ёлка, ёлка",
	), "")
	lib.SetResult(bbc.ID, "v2", StatusOK, transcriptOf("здесь только ЕЛКА"), "")

	t.Run("регистр и ё", func(t *testing.T) {
		result := lib.Search("елка", "")
		if result.TotalHits != 3 || result.TotalVideos != 2 {
			t.Fatalf("hits %d videos %d", result.TotalHits, result.TotalVideos)
		}
		// Сначала видео с большим числом совпадений.
		videoHits := result.Videos[0]
		if videoHits.ID != "v1" || videoHits.Total != 2 {
			t.Fatalf("первое видео %s (%d)", videoHits.ID, videoHits.Total)
		}
		// Два вхождения в одной строке — одно совпадение с двумя подсветками.
		last := videoHits.Hits[1]
		line := last.Lines[len(last.Lines)-1]
		if line.Text != "и снова ёлка, ёлка" || fmt.Sprint(line.Marks) != "[[8 12] [14 18]]" {
			t.Errorf("подсветка: %+v", line)
		}
	})

	t.Run("фраза через границу строк", func(t *testing.T) {
		result := lib.Search("мы  пошли в магазин", "")
		if result.TotalHits != 1 {
			t.Fatalf("hits %d", result.TotalHits)
		}
		hit := result.Videos[0].Hits[0]
		if hit.Start != 2 {
			t.Errorf("время совпадения %v, want 2", hit.Start)
		}
		// Контекст: строка до, две строки с фразой, строка после.
		var got []string
		for _, line := range hit.Lines {
			got = append(got, fmt.Sprintf("%s%v", line.Text, line.Marks))
		}
		want := "Ёлка стоит в углу[] а потом мы пошли[[8 16]] в магазин за хлебом[[0 9]] и снова ёлка, ёлка[]"
		if strings.Join(got, " ") != want {
			t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
		}
	})

	t.Run("фильтр по каналу и пустой запрос", func(t *testing.T) {
		if result := lib.Search("елка", "UCother"); result.TotalHits != 0 {
			t.Error("фильтр по каналу не сработал")
		}
		if result := lib.Search("   ", ""); result.TotalHits != 0 || result.Videos == nil {
			t.Errorf("пустой запрос: %+v", result)
		}
	})
}

func TestSearchCapsHits(t *testing.T) {
	lib, _ := newLib(t)
	var texts []string
	// Между вхождениями две пустые по смыслу строки, чтобы совпадения не склеились.
	for i := range MaxHitsPerVideo + 10 {
		texts = append(texts, fmt.Sprintf("слово %d слово", i), "пауза", "пауза")
	}
	lib.SetResult(bbc.ID, "v1", StatusOK, transcriptOf(texts...), "")
	result := lib.Search("слово", "")
	videoHits := result.Videos[0]
	if videoHits.Total != MaxHitsPerVideo+10 || len(videoHits.Hits) != MaxHitsPerVideo {
		t.Fatalf("total %d hits %d", videoHits.Total, len(videoHits.Hits))
	}
	for _, hit := range videoHits.Hits {
		for _, line := range hit.Lines {
			if len(line.Marks) > 2 {
				t.Fatalf("лишние подсветки: %+v", line)
			}
		}
	}
}

func BenchmarkSearch(b *testing.B) {
	lib, err := Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	lib.Put(bbc, 200)
	words := strings.Fields("сегодня мы поговорим о том как устроен мир новостей и почему это важно для каждого")
	var videos []youtube.Video
	for i := range 200 {
		videos = append(videos, youtube.Video{ID: fmt.Sprint("v", i)})
	}
	lib.Merge(bbc.ID, videos)
	for i := range 200 {
		var texts []string
		for j := range 1000 {
			texts = append(texts, strings.Join(words[(i+j)%5:(i+j)%5+8], " "))
		}
		lib.SetResult(bbc.ID, fmt.Sprint("v", i), StatusOK, transcriptOf(texts...), "")
	}
	b.ResetTimer()
	for b.Loop() {
		lib.Search("почему это важно", "")
	}
}

func TestAdjacentMatchesMerge(t *testing.T) {
	lib, _ := newLib(t)
	lib.SetResult(bbc.ID, "v1", StatusOK, transcriptOf(
		"начало",
		"Голливуд раз",    // 1
		"Голливуд два",    // 2, соседняя: в то же совпадение
		"середина",        //
		"Голливуд три",    // 4, через строку: тоже склеивается
		"тишина",          //
		"тишина",          //
		"тишина",          //
		"Голливуд четыре", // 8, далеко: новое совпадение
		"конец",
	), "")
	videoHits := lib.Search("голливуд", "").Videos[0]
	if videoHits.Total != 2 || len(videoHits.Hits) != 2 {
		t.Fatalf("total %d hits %d, want 2 и 2", videoHits.Total, len(videoHits.Hits))
	}
	first := videoHits.Hits[0]
	var marked []string
	for _, line := range first.Lines {
		if len(line.Marks) > 0 {
			marked = append(marked, line.Text)
		}
	}
	if len(first.Lines) != 6 || strings.Join(marked, "|") != "Голливуд раз|Голливуд два|Голливуд три" {
		t.Errorf("первое совпадение: %d строк, подсвечены %v", len(first.Lines), marked)
	}
	if videoHits.Hits[1].Start != 16 {
		t.Errorf("второе совпадение с %v, want 16", videoHits.Hits[1].Start)
	}
}

func TestLongRunOfMatchesIsSplit(t *testing.T) {
	lib, _ := newLib(t)
	var texts []string
	for range 20 {
		texts = append(texts, "да да")
	}
	lib.SetResult(bbc.ID, "v1", StatusOK, transcriptOf(texts...), "")
	videoHits := lib.Search("да", "").Videos[0]
	for _, hit := range videoHits.Hits {
		if len(hit.Lines) > MaxHitLines {
			t.Fatalf("совпадение из %d строк", len(hit.Lines))
		}
	}
	if videoHits.Total < 3 {
		t.Errorf("20 строк подряд склеились в %d совпадений", videoHits.Total)
	}
}

func TestFreshVideoWithoutCaptionsIsRechecked(t *testing.T) {
	lib, _ := newLib(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	lib.now = func() time.Time { return now }
	// v3 опубликовано 3 октября (6,5 дня назад), v1 — 1 октября (8,5 дня).
	lib.SetResult(bbc.ID, "v3", StatusNoCaptions, nil, "")
	lib.SetResult(bbc.ID, "v1", StatusNoCaptions, nil, "")
	lib.SetResult(bbc.ID, "v2", StatusOK, transcriptOf("текст"), "")

	todo := func() string {
		var ids []string
		for _, video := range lib.Todo(bbc.ID) {
			ids = append(ids, video.ID)
		}
		return strings.Join(ids, " ")
	}
	if got := todo(); got != "" {
		t.Errorf("сразу после проверки: %q, ждали пусто", got)
	}
	now = now.Add(7 * time.Hour)
	if got := todo(); got != "v3" {
		t.Errorf("через 7 часов: %q, ждали v3 (свежее), без v1 (старое)", got)
	}
	now = now.Add(7 * 24 * time.Hour)
	if got := todo(); got != "" {
		t.Errorf("через неделю: %q — свежесть прошла, ждали пусто", got)
	}
}

func TestLockDir(t *testing.T) {
	dir := t.TempDir()
	release, err := LockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// flock действует на открытый файл, поэтому второй open в том же
	// процессе ведёт себя как второй сервер.
	if _, err := LockDir(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("второй захват: %v, ждали ErrLocked", err)
	}
	release()
	again, err := LockDir(dir)
	if err != nil {
		t.Fatalf("после release: %v", err)
	}
	again()
}

func TestFlushDoesNotHoldLockWhileWriting(t *testing.T) {
	lib, dir := newLib(t)
	lib.SetResult(bbc.ID, "v1", StatusOK, transcriptOf("текст"), "")

	writing, release := make(chan struct{}), make(chan struct{})
	writeFile = func(path string, data []byte) error {
		close(writing)
		<-release
		return writeAtomic(path, data)
	}
	defer func() { writeFile = writeAtomic }()

	flushed := make(chan error)
	go func() { flushed <- lib.Flush() }()
	<-writing

	// Пока файл пишется, чтение и SetResult не ждут записи.
	done := make(chan struct{})
	go func() {
		lib.Summaries()
		lib.Search("текст", "")
		lib.SetResult(bbc.ID, "v2", StatusOK, transcriptOf("второе"), "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("библиотека заблокирована на время записи")
	}

	// Удаление ждёт конца записи и не даёт ей вернуть файл.
	removed := make(chan error)
	go func() { removed <- lib.Remove(bbc.ID) }()
	select {
	case <-removed:
		t.Fatal("удаление не дождалось идущей записи")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "channels", bbc.ID+".json")); !os.IsNotExist(err) {
		t.Error("файл удалённого канала вернулся")
	}
}

func TestFailedWriteStaysDirty(t *testing.T) {
	lib, dir := newLib(t)
	lib.SetResult(bbc.ID, "v1", StatusOK, transcriptOf("текст"), "")
	writeFile = func(string, []byte) error { return errors.New("диск полон") }
	if err := lib.Flush(); err == nil {
		t.Fatal("ошибка записи потерялась")
	}
	writeFile = writeAtomic
	if err := lib.Flush(); err != nil {
		t.Fatal(err)
	}
	again, _ := Open(dir)
	if summary, _ := again.Summary(bbc.ID); summary.Counts.OK != 1 {
		t.Errorf("после повторной записи: %+v", summary.Counts)
	}
}
