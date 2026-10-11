package library

import (
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

func lines(texts ...string) *transcript.Transcript {
	t := &transcript.Transcript{Lang: "ru", Auto: true}
	for i, s := range texts {
		t.Lines = append(t.Lines, transcript.Line{Start: float64(i * 2), Dur: 2, Text: s})
	}
	return t
}

func newLib(t *testing.T) (*Library, string) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Put(bbc, 200); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	err = l.Merge(bbc.ID, []youtube.Video{
		{ID: "v1", Title: "Первое", Published: day},
		{ID: "v2", Title: "Второе", Published: day.Add(24 * time.Hour)},
		{ID: "v3", Title: "Третье", Published: day.Add(48 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return l, dir
}

func TestRoundTripAndTodo(t *testing.T) {
	l, dir := newLib(t)
	l.SetResult(bbc.ID, "v1", StatusOK, lines("Привет, мир"), "")
	l.SetResult(bbc.ID, "v2", StatusBlocked, nil, "капча")
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := again.Summary(bbc.ID)
	if !ok {
		t.Fatal("канал пропал после перезапуска")
	}
	if s.Counts != (Counts{Total: 3, OK: 1, Failed: 1, Pending: 1, Lines: 1}) {
		t.Errorf("сводка: %+v", s.Counts)
	}
	var todo []string
	for _, v := range again.Todo(bbc.ID) {
		todo = append(todo, v.ID)
	}
	// Блокировка не финальна: видео снова в очереди; порядок — от новых.
	if strings.Join(todo, " ") != "v3 v2" {
		t.Errorf("todo = %v, want [v3 v2]", todo)
	}
	if r := again.Search("мир", ""); r.TotalHits != 1 {
		t.Errorf("поиск после перезапуска: %+v", r)
	}
}

func TestMergeKeepsTranscriptsAndOldVideos(t *testing.T) {
	l, _ := newLib(t)
	l.SetResult(bbc.ID, "v1", StatusOK, lines("старое видео"), "")
	l.SetResult(bbc.ID, "v2", StatusOK, lines("второе видео"), "")
	// Новый список: v2 переименовано, v1 выпало (сверх предела), появилось v4.
	if err := l.Merge(bbc.ID, []youtube.Video{{ID: "v4"}, {ID: "v2", Title: "Новое имя"}}); err != nil {
		t.Fatal(err)
	}
	r := l.Search("видео", "")
	got := map[string]string{}
	for _, v := range r.Videos {
		got[v.ID] = v.Title
	}
	if len(got) != 2 || got["v2"] != "Новое имя" || got["v1"] != "Первое" {
		t.Errorf("после слияния: %v", got)
	}
}

func TestCorruptFileIsKeptAside(t *testing.T) {
	dir := t.TempDir()
	chDir := filepath.Join(dir, "channels")
	os.MkdirAll(chDir, 0o755)
	os.WriteFile(filepath.Join(chDir, "UCbad.json"), []byte(`{"version":1,"chan`), 0o644)
	os.WriteFile(filepath.Join(chDir, "UCold.json.tmp-123"), []byte(`мусор`), 0o644)

	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Summaries()) != 0 {
		t.Error("битый канал загружен")
	}
	entries, _ := os.ReadDir(chDir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "UCbad.json.corrupt-") || strings.Contains(joined, "UCbad.json ") {
		t.Errorf("битый файл не отложен: %v", names)
	}
}

func TestRemove(t *testing.T) {
	l, dir := newLib(t)
	l.SetResult(bbc.ID, "v1", StatusOK, lines("текст"), "")
	if err := l.Remove(bbc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "channels", bbc.ID+".json")); !os.IsNotExist(err) {
		t.Error("файл канала остался")
	}
	if r := l.Search("текст", ""); r.TotalHits != 0 {
		t.Error("удалённый канал всё ещё ищется")
	}
}

func TestSearch(t *testing.T) {
	l, _ := newLib(t)
	l.SetResult(bbc.ID, "v1", StatusOK, lines(
		"Ёлка стоит в углу",
		"а потом мы пошли",
		"в магазин за хлебом",
		"и снова ёлка, ёлка",
	), "")
	l.SetResult(bbc.ID, "v2", StatusOK, lines("здесь только ЕЛКА"), "")

	t.Run("регистр и ё", func(t *testing.T) {
		r := l.Search("елка", "")
		if r.TotalHits != 3 || r.TotalVideos != 2 {
			t.Fatalf("hits %d videos %d", r.TotalHits, r.TotalVideos)
		}
		// Сначала видео с большим числом совпадений.
		v := r.Videos[0]
		if v.ID != "v1" || v.Total != 2 {
			t.Fatalf("первое видео %s (%d)", v.ID, v.Total)
		}
		// Два вхождения в одной строке — одно совпадение с двумя подсветками.
		last := v.Hits[1]
		line := last.Lines[len(last.Lines)-1]
		if line.T != "и снова ёлка, ёлка" || fmt.Sprint(line.Marks) != "[[8 12] [14 18]]" {
			t.Errorf("подсветка: %+v", line)
		}
	})

	t.Run("фраза через границу строк", func(t *testing.T) {
		r := l.Search("мы  пошли в магазин", "")
		if r.TotalHits != 1 {
			t.Fatalf("hits %d", r.TotalHits)
		}
		h := r.Videos[0].Hits[0]
		if h.S != 2 {
			t.Errorf("время совпадения %v, want 2", h.S)
		}
		// Контекст: строка до, две строки с фразой, строка после.
		var got []string
		for _, ln := range h.Lines {
			got = append(got, fmt.Sprintf("%s%v", ln.T, ln.Marks))
		}
		want := "Ёлка стоит в углу[] а потом мы пошли[[8 16]] в магазин за хлебом[[0 9]] и снова ёлка, ёлка[]"
		if strings.Join(got, " ") != want {
			t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
		}
	})

	t.Run("фильтр по каналу и пустой запрос", func(t *testing.T) {
		if r := l.Search("елка", "UCother"); r.TotalHits != 0 {
			t.Error("фильтр по каналу не сработал")
		}
		if r := l.Search("   ", ""); r.TotalHits != 0 || r.Videos == nil {
			t.Errorf("пустой запрос: %+v", r)
		}
	})
}

func TestSearchCapsHits(t *testing.T) {
	l, _ := newLib(t)
	var texts []string
	// Между вхождениями по две пустые по смыслу строки — чтобы они не склеились.
	for i := range MaxHitsPerVideo + 10 {
		texts = append(texts, fmt.Sprintf("слово %d слово", i), "пауза", "пауза")
	}
	l.SetResult(bbc.ID, "v1", StatusOK, lines(texts...), "")
	r := l.Search("слово", "")
	v := r.Videos[0]
	if v.Total != MaxHitsPerVideo+10 || len(v.Hits) != MaxHitsPerVideo {
		t.Fatalf("total %d hits %d", v.Total, len(v.Hits))
	}
	for _, h := range v.Hits {
		for _, ln := range h.Lines {
			if len(ln.Marks) > 2 {
				t.Fatalf("лишние подсветки: %+v", ln)
			}
		}
	}
}

func BenchmarkSearch(b *testing.B) {
	l, err := Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	l.Put(bbc, 200)
	words := strings.Fields("сегодня мы поговорим о том как устроен мир новостей и почему это важно для каждого")
	var videos []youtube.Video
	for i := range 200 {
		videos = append(videos, youtube.Video{ID: fmt.Sprint("v", i)})
	}
	l.Merge(bbc.ID, videos)
	for i := range 200 {
		var texts []string
		for j := range 1000 {
			texts = append(texts, strings.Join(words[(i+j)%5:(i+j)%5+8], " "))
		}
		l.SetResult(bbc.ID, fmt.Sprint("v", i), StatusOK, lines(texts...), "")
	}
	b.ResetTimer()
	for b.Loop() {
		l.Search("почему это важно", "")
	}
}

func TestAdjacentMatchesMerge(t *testing.T) {
	l, _ := newLib(t)
	l.SetResult(bbc.ID, "v1", StatusOK, lines(
		"начало",
		"Голливуд раз",    // 1
		"Голливуд два",    // 2 — соседняя: в то же совпадение
		"середина",        //
		"Голливуд три",    // 4 — через строку: тоже склеивается
		"тишина",          //
		"тишина",          //
		"тишина",          //
		"Голливуд четыре", // 8 — далеко: новое совпадение
		"конец",
	), "")
	v := l.Search("голливуд", "").Videos[0]
	if v.Total != 2 || len(v.Hits) != 2 {
		t.Fatalf("total %d hits %d, want 2 и 2", v.Total, len(v.Hits))
	}
	first := v.Hits[0]
	var marked []string
	for _, ln := range first.Lines {
		if len(ln.Marks) > 0 {
			marked = append(marked, ln.T)
		}
	}
	if len(first.Lines) != 6 || strings.Join(marked, "|") != "Голливуд раз|Голливуд два|Голливуд три" {
		t.Errorf("первое совпадение: %d строк, подсвечены %v", len(first.Lines), marked)
	}
	if v.Hits[1].S != 16 {
		t.Errorf("второе совпадение с %v, want 16", v.Hits[1].S)
	}
}

func TestLongRunOfMatchesIsSplit(t *testing.T) {
	l, _ := newLib(t)
	var texts []string
	for range 20 {
		texts = append(texts, "да да")
	}
	l.SetResult(bbc.ID, "v1", StatusOK, lines(texts...), "")
	v := l.Search("да", "").Videos[0]
	for _, h := range v.Hits {
		if len(h.Lines) > MaxHitLines {
			t.Fatalf("совпадение из %d строк", len(h.Lines))
		}
	}
	if v.Total < 3 {
		t.Errorf("20 строк подряд склеились в %d совпадений", v.Total)
	}
}
