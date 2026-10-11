#!/usr/bin/env bash
# Собирает web/src/app/icon.ts из значков Lucide (лицензия ISC).
# Запускать нужно один раз и при смене набора. Список значков лежит в ICONS ниже,
# версия в LUCIDE. Значки вшиваются в шаблон компонента, поэтому сборка
# и браузер за ними в сеть не ходят.
set -euo pipefail
cd "$(dirname "$0")/.."

LUCIDE=1.55.0
ICONS=(
  search plus x refresh-cw square trash external-link copy check play
  sun moon monitor chevron-down triangle-alert loader-circle clock captions
  key-round link list-video
)

python3 - "$LUCIDE" "${ICONS[@]}" <<'PY'
import re, sys, urllib.request

version, names = sys.argv[1], sys.argv[2:]
cases = []
for name in names:
    url = f"https://raw.githubusercontent.com/lucide-icons/lucide/{version}/icons/{name}.svg"
    svg = urllib.request.urlopen(url).read().decode()
    body = re.search(r"<svg[^>]*>(.*)</svg>", svg, re.S).group(1)
    # Внутри <svg> компонента Angular нужны элементы с префиксом svg:.
    elements = re.findall(r"<(\w+)([^>]*?)\s*/>", body)
    inner = "\n        ".join(f"<svg:{tag}{attrs} />" for tag, attrs in elements)
    cases.append(f"      @case ('{name}') {{\n        {inner}\n      }}")

union = "\n".join(f"  | '{n}'" for n in names)
out = f"""import {{ ChangeDetectionStrategy, Component, input }} from '@angular/core';

// Сгенерировано scripts/fetch-icons.sh из Lucide {version} (ISC) — не править руками.

export type IconName =
{union};

// Значок интерфейса: <svg appIcon name="search" [size]="18"></svg>.
@Component({{
  selector: 'svg[appIcon]',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {{
    'aria-hidden': 'true',
    fill: 'none',
    viewBox: '0 0 24 24',
    stroke: 'currentColor',
    'stroke-linecap': 'round',
    'stroke-linejoin': 'round',
    '[attr.stroke-width]': 'stroke()',
    '[attr.width]': 'size()',
    '[attr.height]': 'size()',
  }},
  template: `
    @switch (name()) {{
{chr(10).join(cases)}
    }}
  `,
}})
export class Icon {{
  readonly name = input.required<IconName>();
  readonly size = input(18);
  readonly stroke = input(1.8);
}}
"""
open("web/src/app/icon.ts", "w").write(out)
print(f"{len(names)} значков")
PY
