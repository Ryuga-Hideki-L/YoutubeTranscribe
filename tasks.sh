#!/usr/bin/env bash
# Сборка «Дословно»: Angular (web/) вшивается в Go-сервер (server/).
#   ./tasks.sh build  — собрать интерфейс и бинарник bin/doslovno
#   ./tasks.sh run    — собрать и запустить на 127.0.0.1:3000
#   ./tasks.sh dev    — Go в режиме -dev на :3000 и ng serve на :4200
#                       Порты задают DOSLOVNO_API_PORT и DOSLOVNO_PORT
#   ./tasks.sh test   — сборка, тесты Angular и Go, в том числе проверка настоящей сборки
#   ./tasks.sh clean  — убрать сборки
set -euo pipefail
cd "$(dirname "$0")"

STATIC=server/internal/web/static
export CGO_ENABLED=0 NG_CLI_ANALYTICS=false

web() { (cd web && pnpm install --frozen-lockfile && pnpm ng build); }

# Копируем сборку рядом с go:embed. Файл .gitkeep остаётся, чтобы пакет собирался и без фронтенда.
static() {
  web && rsync -a --delete --exclude .gitkeep web/dist/doslovno/browser/ "$STATIC/"
  # Angular кладёт лицензии кода фронтенда рядом с browser/. Их тоже раздаём.
  cp web/dist/doslovno/3rdpartylicenses.txt "$STATIC/"
}

server() { (cd server && go build -trimpath -o ../bin/doslovno .); }

case "${1:-build}" in
  build) static && server ;;
  run) static && server && exec ./bin/doslovno "${@:2}" ;;
  dev)
    # Сервер собирается в бинарник и запускается напрямую, а не через go run.
    # Если убить go run по PID, скомпилированный сервер остался бы жить на порту.
    api_port=${DOSLOVNO_API_PORT:-3000}
    (cd server && go build -o ../bin/doslovno-dev .)
    ./bin/doslovno-dev -dev -addr "127.0.0.1:$api_port" "${@:2}" &
    server_pid=$!
    trap 'kill "$server_pid" 2>/dev/null' EXIT
    (cd web && DOSLOVNO_API="http://127.0.0.1:$api_port" pnpm ng serve --port "${DOSLOVNO_PORT:-4200}")
    ;;
  test)
    # Сначала ставим зависимости и делаем настоящую сборку. Иначе на чистом
    # клоне нет node_modules, а тест вшитой сборки в Go пропустился бы.
    static
    (cd web && pnpm ng test --watch=false)
    (cd server && go vet ./... && CI=1 go test ./...)
    ;;
  clean) rm -rf bin web/dist && find "$STATIC" -mindepth 1 ! -name .gitkeep -delete ;;
  *) echo "использование: $0 {build|run|dev|test|clean}" >&2; exit 2 ;;
esac
