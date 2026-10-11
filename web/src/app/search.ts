import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  ElementRef,
  computed,
  effect,
  inject,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { ACTIVE_STATES, Api, ApiError, SearchResult } from './api';
import { plural } from './format';
import { Icon } from './icon';
import { VideoGroup } from './video-group';

// Строка поиска и результаты. Запрос живёт в адресе (?q=&channel=), чтобы
// перезагрузка и ссылка открывали тот же поиск. Ввод ищет через 250 мс
// после последней клавиши, Enter — сразу. Пока идёт новый поиск, старые
// результаты остаются на месте.
@Component({
  selector: 'app-search',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [Icon, VideoGroup],
  host: { '(document:keydown)': 'onKey($event)' },
  template: `
    <div class="bar">
      <form class="box" (submit)="$event.preventDefault(); runNow()">
        <svg appIcon name="search" [size]="18" class="lead"></svg>
        <label class="sr-only" for="search-input">Фраза для поиска</label>
        <input
          #input
          id="search-input"
          type="search"
          placeholder="Фраза, которую сказали в видео"
          autocomplete="off"
          [value]="query()"
          (input)="onInput($any($event.target).value)"
          [disabled]="empty()"
        />
        @if (busy()) {
          <svg appIcon name="loader-circle" [size]="16" class="spin subtle"></svg>
        } @else if (!query()) {
          <kbd class="mono" aria-hidden="true">/</kbd>
        }
      </form>
      @if (channels().length > 1) {
        <label class="sr-only" for="scope">Где искать</label>
        <select id="scope" class="scope" [value]="scope()" (change)="setScope($any($event.target).value)">
          <option value="">Все каналы</option>
          @for (c of channels(); track c.id) {
            <option [value]="c.id">{{ c.title }}</option>
          }
        </select>
      }
    </div>

    @if (error(); as e) {
      <div class="notice error" role="alert">
        <svg appIcon name="triangle-alert" [size]="16"></svg>
        <p>{{ e }}</p>
      </div>
    }

    @if (result(); as r) {
      <div class="summary" aria-live="polite">
        @if (r.totalHits) {
          <p>
            Найдено {{ hitsLine() }} в {{ videosLine() }}
            @if (r.totalVideos > r.videos.length) {
              <span class="subtle">· показаны {{ r.videos.length }} видео с наибольшим числом совпадений</span>
            }
          </p>
        } @else {
          <p>Ничего не найдено по «{{ r.query }}»</p>
          @if (scope()) {
            <button class="btn" type="button" (click)="setScope('')">Искать во всех каналах</button>
          }
          @if (progress(); as p) {
            <p class="subtle">Индексация ещё идёт — {{ p }}</p>
          }
        }
        @if (stale()) {
          <button class="btn" type="button" (click)="runNow()">
            <svg appIcon name="refresh-cw" [size]="14"></svg> Появились новые субтитры — обновить
          </button>
        }
      </div>
      <div class="groups">
        @for (v of r.videos; track v.id; let i = $index) {
          <app-video-group class="appear" [style.--i]="i" [video]="v" />
        }
      </div>
    } @else if (!empty() && !query()) {
      <div class="hint">
        <svg appIcon name="captions" [size]="28"></svg>
        <p>Введите фразу — найдём, в каком видео и на какой минуте её сказали.</p>
        <p class="subtle">Регистр и «е»/«ё» не важны, фраза находится и на стыке строк субтитров.</p>
      </div>
    }
  `,
  styles: `
    :host {
      display: grid;
      gap: 16px;
      align-content: start;
    }
    .bar {
      position: sticky;
      top: 0;
      z-index: 2;
      display: flex;
      gap: 8px;
      padding: 12px 0;
      margin: -12px 0 -4px;
      background: var(--bg);
    }
    .box {
      flex: 1;
      min-width: 0;
      display: flex;
      align-items: center;
      gap: 10px;
      min-height: 48px;
      padding: 0 14px;
      border: 1px solid var(--line-strong);
      border-radius: var(--radius-m);
      background: var(--surface);
      transition: border-color 160ms var(--ease);
    }
    .box:focus-within {
      border-color: var(--text-muted);
    }
    .lead {
      flex: none;
      color: var(--text-subtle);
    }
    input {
      flex: 1;
      min-width: 0;
      border: 0;
      outline: none;
      background: transparent;
      font-size: 16px;
    }
    input::placeholder {
      color: var(--text-subtle);
    }
    input::-webkit-search-cancel-button {
      cursor: pointer;
    }
    kbd {
      padding: 1px 7px;
      border: 1px solid var(--chip-edge);
      border-radius: 6px;
      background: var(--chip-bg);
      color: var(--text-subtle);
      font-size: 12px;
    }
    .scope {
      max-width: 200px;
      min-height: 48px;
      padding: 0 10px;
      border: 1px solid var(--line-strong);
      border-radius: var(--radius-m);
      background: var(--surface);
    }
    .summary {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 8px 16px;
      color: var(--text-muted);
    }
    .groups {
      display: grid;
      gap: 12px;
    }
    .hint {
      display: grid;
      justify-items: center;
      gap: 8px;
      padding: 48px 16px;
      text-align: center;
      color: var(--text-muted);
    }
    .hint svg {
      color: var(--text-subtle);
      margin-bottom: 4px;
    }
    @media (max-width: 640px) {
      .bar {
        flex-direction: column;
      }
      .scope {
        max-width: none;
        min-height: 40px;
      }
    }
  `,
})
export class Search {
  private readonly api = inject(Api);
  private readonly input = viewChild.required<ElementRef<HTMLInputElement>>('input');

  protected readonly channels = this.api.channels;
  protected readonly empty = computed(() => this.channels().length === 0);
  protected readonly query = signal('');
  protected readonly scope = signal('');
  protected readonly result = signal<SearchResult | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly busy = signal(false);
  // Сколько строк было в библиотеке на момент поиска: выросло — результат устарел.
  private readonly linesAtSearch = signal(0);
  protected readonly stale = computed(
    () => this.result() !== null && this.api.searchableLines() > this.linesAtSearch(),
  );

  protected readonly hitsLine = computed(() =>
    plural(this.result()?.totalHits ?? 0, ['совпадение', 'совпадения', 'совпадений']),
  );
  protected readonly videosLine = computed(() => {
    const n = this.result()?.totalVideos ?? 0;
    return `${n.toLocaleString('ru')} видео`;
  });
  protected readonly progress = computed(() => {
    const running = this.channels().find((c) => c.job && ACTIVE_STATES.includes(c.job.state));
    return running?.job ? `${running.job.done} из ${running.job.total}` : '';
  });

  private timer: ReturnType<typeof setTimeout> | undefined;
  private inflight: AbortController | null = null;

  constructor() {
    const params = new URLSearchParams(location.search);
    this.query.set(params.get('q') ?? '');
    this.scope.set(params.get('channel') ?? '');
    // Поиск из адреса — когда придёт состояние библиотеки: иначе счётчик
    // строк на момент поиска был бы нулём и результат сразу «устарел».
    const initial = effect(() => {
      if (!this.api.state()) return;
      initial.destroy();
      if (this.query().trim()) untracked(() => void this.runNow());
    });
    inject(DestroyRef).onDestroy(() => {
      clearTimeout(this.timer);
      this.inflight?.abort();
    });
  }

  protected onInput(value: string) {
    this.query.set(value);
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.runNow(), 250);
  }

  protected setScope(id: string) {
    this.scope.set(id);
    this.runNow();
  }

  protected onKey(e: KeyboardEvent) {
    const target = e.target as HTMLElement | null;
    const typing = target?.closest('input, textarea, select, [contenteditable]');
    if (e.key === '/' && !typing && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      this.input().nativeElement.focus();
    }
  }

  protected async runNow() {
    clearTimeout(this.timer);
    const q = this.query().trim();
    this.syncUrl(q);
    this.inflight?.abort();
    if (!q) {
      this.result.set(null);
      this.error.set(null);
      this.busy.set(false);
      return;
    }
    const ctrl = new AbortController();
    this.inflight = ctrl;
    // Индикатор — только если поиск дольше 300 мс: обычно он мгновенный.
    const slow = setTimeout(() => this.busy.set(true), 300);
    const lines = this.api.searchableLines();
    try {
      const res = await this.api.search(q, this.scope(), ctrl.signal);
      this.result.set(res);
      this.linesAtSearch.set(lines);
      this.error.set(null);
    } catch (err) {
      if (ctrl.signal.aborted) return;
      this.error.set(err instanceof ApiError ? err.message : 'Поиск не удался');
    } finally {
      clearTimeout(slow);
      if (this.inflight === ctrl) {
        this.busy.set(false);
        this.inflight = null;
      }
    }
  }

  private syncUrl(q: string) {
    const params = new URLSearchParams();
    if (q) params.set('q', q);
    if (this.scope()) params.set('channel', this.scope());
    const next = params.size ? `?${params}` : location.pathname;
    history.replaceState(null, '', next);
  }
}
