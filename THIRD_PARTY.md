# Чужие материалы и их лицензии

| Что | Где в репозитории | Источник | Лицензия |
|---|---|---|---|
| Шрифты Manrope и JetBrains Mono (кириллица и латиница, woff2) | `web/public/fonts/`, `web/src/fonts.css` | Google Fonts: [Manrope](https://fonts.google.com/specimen/Manrope), [JetBrains Mono](https://fonts.google.com/specimen/JetBrains+Mono) | SIL Open Font License 1.1 |
| Значки Lucide 1.55.0 | `web/src/app/icon.ts` (сгенерирован), `web/public/favicon.svg` (цвет изменён) | [lucide-icons/lucide](https://github.com/lucide-icons/lucide) | ISC |

Полные тексты лицензий раздаёт сам интерфейс: `/licenses.txt` (шрифты и значки, файл `web/public/licenses.txt`) и `/3rdpartylicenses.txt` (код фронтенда — Angular, rxjs, tslib; файл собирает Angular, `tasks.sh` копирует его в сборку). Шрифты пересобираются скриптом `scripts/fetch-fonts.sh`, значки — `scripts/fetch-icons.sh`.

Плеер YouTube загружается с youtube.com в момент первого нажатия на метку времени и в репозиторий не входит.
