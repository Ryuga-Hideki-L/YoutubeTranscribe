#!/usr/bin/env bash
# Скачивает шрифты сайта (Manrope, JetBrains Mono — SIL OFL 1.1) в web/public/fonts/
# и пишет web/src/fonts.css с локальными @font-face. Нужен один раз и при
# смене начертаний: сборка и браузер потом в Google Fonts не ходят.
# Берутся только кириллица и латиница — других алфавитов в интерфейсе нет.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=web/public/fonts
CSS=web/src/fonts.css
URL='https://fonts.googleapis.com/css2?family=Manrope:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap'
# С современным User-Agent Google отдаёт woff2 с разбиением по алфавитам.
UA='Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0'

mkdir -p "$OUT"
curl -fsS -A "$UA" "$URL" | python3 -c '
import re, subprocess, sys
out, css_path = sys.argv[1], sys.argv[2]
css = sys.stdin.read()
blocks = re.findall(r"/\* ([\w-]+) \*/\s*(@font-face \{.*?\})", css, re.S)
keep = ("cyrillic", "latin")
# Google отдаёт вариативные шрифты: у разных насыщенностей один и тот же файл.
# Склеиваем их в одно @font-face с диапазоном font-weight.
faces = {}
for subset, block in blocks:
    if subset not in keep:
        continue
    family = re.search(r"font-family: \x27([^\x27]+)\x27", block).group(1)
    weight = int(re.search(r"font-weight: (\d+)", block).group(1))
    src = re.search(r"url\((https://[^)]+\.woff2)\)", block).group(1)
    ranges = re.search(r"unicode-range: ([^;]+);", block).group(1)
    face = faces.setdefault((family, subset, src), {"weights": [], "ranges": ranges})
    face["weights"].append(weight)
result = ["/* Шрифты интерфейса — локальные копии, scripts/fetch-fonts.sh. Manrope и JetBrains Mono: SIL OFL 1.1. */\n"]
for (family, subset, src), face in faces.items():
    low, high = min(face["weights"]), max(face["weights"])
    weight = str(low) if low == high else f"{low} {high}"
    name = "-".join(family.lower().split()) + f"-{subset}" + (f"-{low}" if low == high else "") + ".woff2"
    subprocess.run(["curl", "-fsS", "-o", f"{out}/{name}", src], check=True)
    result.append(f"""/* {family}, {subset} */
@font-face {{
  font-family: "{family}";
  font-style: normal;
  font-weight: {weight};
  font-display: swap;
  src: url("/fonts/{name}") format("woff2");
  unicode-range: {face["ranges"]};
}}
""")
open(css_path, "w").write("\n".join(result))
print(f"{len(faces)} файлов шрифтов")
' "$OUT" "$CSS"

du -sh "$OUT"
