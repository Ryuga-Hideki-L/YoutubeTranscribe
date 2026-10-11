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

// Строка поиска и результаты. Запрос хранится в адресе (?q=&channel=),
// чтобы перезагрузка и ссылка открывали тот же поиск. Поиск запускается
// через 250 мс после последней клавиши, а Enter запускает его сразу.
// Пока идёт новый поиск, старые результаты остаются на месте.
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
          [disabled]="libraryEmpty()"
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
          @for (channel of channels(); track channel.id) {
            <option [value]="channel.id">{{ channel.title }}</option>
          }
        </select>
      }
    </div>

    @if (error(); as message) {
      <div class="notice error" role="alert">
        <svg appIcon name="triangle-alert" [size]="16"></svg>
        <p>{{ message }}</p>
      </div>
    }

    @if (result(); as searchResult) {
      <div class="summary" aria-live="polite">
        @if (searchResult.totalHits) {
          <p>
            Найдено {{ hitsLine() }} в {{ videosLine() }}
            @if (searchResult.totalVideos > searchResult.videos.length) {
              <span class="subtle">· показаны {{ searchResult.videos.length }} видео с наибольшим числом совпадений</span>
            }
          </p>
        } @else {
          <p>Ничего не найдено по «{{ searchResult.query }}»</p>
          @if (scope()) {
            <button class="btn" type="button" (click)="setScope('')">Искать во всех каналах</button>
          }
          @if (progress(); as progressText) {
            <p class="subtle">Индексация ещё идёт — {{ progressText }}</p>
          }
        }
        @if (stale()) {
          <button class="btn" type="button" (click)="runNow()">
            <svg appIcon name="refresh-cw" [size]="14"></svg> Появились новые субтитры — обновить
          </button>
        }
      </div>
      <div class="groups">
        @for (video of searchResult.videos; track video.id; let i = $index) {
          <app-video-group class="appear" [style.--i]="i" [video]="video" />
        }
      </div>
    } @else if (!libraryEmpty() && !query()) {
      <div class="hint">
        <svg appIcon name="captions" [size]="28"></svg>
        <p>Введите фразу — найдём, в каком видео и на какой минуте её сказали.</p>
        <p class="subtle">Регистр и «е»/«ё» не важны. Фраза находится и на стыке строк субтитров.</p>
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
  protected readonly libraryEmpty = computed(() => this.channels().length === 0);
  protected readonly query = signal('');
  protected readonly scope = signal('');
  protected readonly result = signal<SearchResult | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly busy = signal(false);
  // Сколько строк было в библиотеке на момент поиска. Если стало больше, результат устарел.
  private readonly linesAtSearch = signal(0);
  protected readonly stale = computed(
    () => this.result() !== null && this.api.searchableLines() > this.linesAtSearch(),
  );

  protected readonly hitsLine = computed(() =>
    plural(this.result()?.totalHits ?? 0, ['совпадение', 'совпадения', 'совпадений']),
  );
  protected readonly videosLine = computed(() => {
    const count = this.result()?.totalVideos ?? 0;
    return `${count.toLocaleString('ru')} видео`;
  });
  protected readonly progress = computed(() => {
    const running = this.channels().find((channel) => channel.job && ACTIVE_STATES.includes(channel.job.state));
    return running?.job ? `${running.job.done} из ${running.job.total}` : '';
  });

  private typingTimer: ReturnType<typeof setTimeout> | undefined;
  private pendingRequest: AbortController | null = null;

  constructor() {
    const params = new URLSearchParams(location.search);
    this.query.set(params.get('q') ?? '');
    this.scope.set(params.get('channel') ?? '');
    // Поиск из адреса запускаем, когда придёт состояние библиотеки. Иначе
    // счётчик строк на момент поиска был бы нулём, и результат сразу
    // считался бы устаревшим.
    const initialSearch = effect(() => {
      if (!this.api.state()) return;
      initialSearch.destroy();
      if (this.query().trim()) untracked(() => void this.runNow());
    });
    inject(DestroyRef).onDestroy(() => {
      clearTimeout(this.typingTimer);
      this.pendingRequest?.abort();
    });
  }

  protected onInput(value: string) {
    this.query.set(value);
    clearTimeout(this.typingTimer);
    this.typingTimer = setTimeout(() => this.runNow(), 250);
  }

  protected setScope(id: string) {
    this.scope.set(id);
    this.runNow();
  }

  protected onKey(event: KeyboardEvent) {
    const target = event.target as HTMLElement | null;
    const typing = target?.closest('input, textarea, select, [contenteditable]');
    if (event.key === '/' && !typing && !event.ctrlKey && !event.metaKey && !event.altKey) {
      event.preventDefault();
      this.input().nativeElement.focus();
    }
  }

  protected async runNow() {
    clearTimeout(this.typingTimer);
    const phrase = this.query().trim();
    this.syncUrl(phrase);
    this.pendingRequest?.abort();
    if (!phrase) {
      this.result.set(null);
      this.error.set(null);
      this.busy.set(false);
      return;
    }
    const controller = new AbortController();
    this.pendingRequest = controller;
    // Индикатор показываем, только если поиск дольше 300 мс. Обычно он мгновенный.
    const busyTimer = setTimeout(() => this.busy.set(true), 300);
    const linesBefore = this.api.searchableLines();
    try {
      const searchResult = await this.api.search(phrase, this.scope(), controller.signal);
      this.result.set(searchResult);
      this.linesAtSearch.set(linesBefore);
      this.error.set(null);
    } catch (err) {
      if (controller.signal.aborted) return;
      this.error.set(err instanceof ApiError ? err.message : 'Поиск не удался');
    } finally {
      clearTimeout(busyTimer);
      if (this.pendingRequest === controller) {
        this.busy.set(false);
        this.pendingRequest = null;
      }
    }
  }

  private syncUrl(phrase: string) {
    const params = new URLSearchParams();
    if (phrase) params.set('q', phrase);
    if (this.scope()) params.set('channel', this.scope());
    const next = params.size ? `?${params}` : location.pathname;
    history.replaceState(null, '', next);
  }
}
