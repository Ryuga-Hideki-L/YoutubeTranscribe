// «Дословно» — поиск по субтитрам каналов YouTube. Один бинарник: API,
// фоновая индексация и вшитый интерфейс на Angular.
//
//	./tasks.sh build && ./bin/doslovno
//
// Ключ YouTube Data API берётся из YOUTUBE_API_KEY (окружение или файл .env
// в текущем каталоге). Без ключа поиск по уже скачанному работает, а новые
// каналы не добавить.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/api"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/indexer"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/library"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/transcript"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/web"
	"github.com/Ryuga-Hideki-L/YoutubeTranscribe/server/internal/youtube"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:3000", "адрес, на котором слушать")
	dataDir := flag.String("data", defaultDataDir(), "каталог с каналами и субтитрами")
	limit := flag.Int("limit", 200, "сколько последних видео канала индексировать по умолчанию")
	langs := flag.String("langs", "ru,en", "языки ручных субтитров по порядку, если язык речи не виден")
	clientVersion := flag.String("client-version", transcript.DefaultClientVersion, "версия клиента Android для плеера YouTube")
	ytdlp := flag.String("ytdlp", "auto", "запасной путь через yt-dlp: auto (если есть в PATH), off или путь к программе")
	dev := flag.Bool("dev", false, "стартовать без собранного фронта (для разработки с ng serve)")
	flag.Parse()

	loadDotEnv(".env")

	site, err := web.Load(web.Static(), *dev)
	if err != nil {
		log.Fatal(err)
	}
	// Блокировка — до чтения библиотеки: второй сервер на той же папке
	// затёр бы файлы каналов первого.
	unlock, err := library.LockDir(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	defer unlock()
	lib, err := library.Open(*dataDir)
	if err != nil {
		log.Fatalf("библиотека в %s: %v", *dataDir, err)
	}

	// HTTP-клиент по умолчанию берёт HTTPS_PROXY из окружения — через него
	// можно обойти блокировку адреса YouTube.
	yt := youtube.New(os.Getenv("YOUTUBE_API_KEY"), nil, "")
	var fetcher indexer.Fetcher = transcript.New(transcript.Options{ClientVersion: *clientVersion, Langs: splitList(*langs)})
	if path := ytdlpPath(*ytdlp); path != "" {
		log.Printf("запасной путь субтитров: %s", path)
		fetcher = transcript.Fallback{Primary: fetcher, Secondary: &transcript.YtDlp{Path: path, Langs: splitList(*langs)}}
	}
	ix := indexer.New(lib, yt, fetcher, indexer.Config{StatePath: filepath.Join(*dataDir, "state.json")})
	srvAPI := api.New(lib, ix, yt, *limit)

	mux := http.NewServeMux()
	srvAPI.Register(mux)
	mux.Handle("/", site)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.Guard(mux),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	srv.RegisterOnShutdown(srvAPI.Shutdown)

	// Порт — до запуска индексации: занятый порт должен остановить запуск
	// раньше, чем рабочий сходит в YouTube.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		ix.Run(ctx)
	}()
	go ix.CheckNetwork(ctx)
	ix.ResumePending()

	// Serve возвращается сразу после Shutdown, а сам Shutdown ещё
	// дожидается открытых запросов — main ждёт его по stopped.
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		// Снять перехват сигналов: второй Ctrl+C завершит процесс сразу.
		stop()
		log.Print("останавливаюсь…")
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			log.Printf("остановка: %v", err)
		}
	}()

	if !yt.HasKey() {
		log.Print("нет YOUTUBE_API_KEY: поиск по скачанному работает, новые каналы не добавить")
	}
	log.Printf("Дословно: http://%s (данные в %s)", *addr, *dataDir)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-stopped
	<-workerDone
	if err := lib.Flush(); err != nil {
		log.Printf("запись библиотеки: %v", err)
	}
}

func defaultDataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "doslovno")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "doslovno")
	}
	return "data"
}

// loadDotEnv дописывает в окружение переменные из файла KEY=VALUE.
// Уже заданные (непустые) в окружении не перезаписываются.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}

func ytdlpPath(flagValue string) string {
	switch flagValue {
	case "off", "":
		return ""
	case "auto":
		return transcript.FindYtDlp()
	}
	return flagValue
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
