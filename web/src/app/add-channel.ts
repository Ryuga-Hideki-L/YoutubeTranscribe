import { ChangeDetectionStrategy, Component, computed, inject, input, signal } from '@angular/core';
import { Api, ApiError, Candidate } from './api';
import { plural } from './format';
import { Icon } from './icon';

const LIMIT_OPTIONS = [50, 100, 200, 500];

// Добавление канала: ввод, список кандидатов, кнопка «Добавить».
// Поиск по названию стоит 100 единиц квоты из 10 000 в сутки. Поэтому
// сервер сначала отвечает needsSearch, а сам поиск запускает только
// явная кнопка.
@Component({
  selector: 'app-add-channel',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [Icon],
  template: `
    <form class="row" (submit)="$event.preventDefault(); find(false)">
      <label class="sr-only" for="channel-input">Канал YouTube</label>
      <input
        id="channel-input"
        class="field"
        [class.large]="large()"
        placeholder="@имя, ссылка или название канала"
        autocomplete="off"
        spellcheck="false"
        [value]="query()"
        (input)="onInput($any($event.target).value)"
        [disabled]="!hasKey()"
      />
      <button class="btn primary" type="submit" [disabled]="!hasKey() || !query().trim() || busy()">
        @if (busy()) {
          <svg appIcon name="loader-circle" [size]="16" class="spin"></svg>
        } @else {
          <svg appIcon name="search" [size]="16"></svg>
        }
        Найти
      </button>
    </form>

    @if (needsSearch()) {
      <div class="notice">
        <svg appIcon name="search" [size]="16"></svg>
        <div>
          <p>Похоже на название, а не на @имя или ссылку. Поиск по названию тратит 100 единиц квоты из 10 000 в сутки.</p>
          <button class="btn" type="button" (click)="find(true)" [disabled]="busy()">Искать по названию</button>
        </div>
      </div>
    }

    @if (error(); as message) {
      <div class="notice error" role="alert">
        <svg appIcon name="triangle-alert" [size]="16"></svg>
        <p>{{ message }}</p>
      </div>
    }

    @if (candidates().length) {
      <ul class="candidates">
        @for (candidate of candidates(); track candidate.id; let i = $index) {
          <li class="candidate appear" [style.--i]="i">
            @if (candidate.thumb) {
              <img class="avatar" [src]="candidate.thumb" alt="" width="40" height="40" referrerpolicy="no-referrer" />
            }
            <div class="names">
              <p class="title">{{ candidate.title }}</p>
              <p class="sub">
                @if (candidate.handle) {
                  <span class="mono">{{ candidate.handle }}</span> ·
                }
                {{ videoCount(candidate) }}
              </p>
            </div>
            @if (candidate.inLibrary) {
              <span class="subtle in-lib"><svg appIcon name="check" [size]="15"></svg> уже есть</span>
            } @else {
              <button class="btn primary" type="button" (click)="add(candidate)" [disabled]="busy()">
                <svg appIcon name="plus" [size]="16"></svg> Добавить
              </button>
            }
          </li>
        }
      </ul>
      <label class="limit">
        Брать последние
        <select class="field" [value]="limit()" (change)="limit.set(+$any($event.target).value)">
          @for (option of limitOptions; track option) {
            <option [value]="option">{{ option }}</option>
          }
        </select>
        видео
      </label>
    }
  `,
  styles: `
    :host {
      display: grid;
      gap: 10px;
    }
    .row {
      display: flex;
      gap: 8px;
    }
    .row .field {
      flex: 1;
      min-width: 0;
    }
    .field.large {
      min-height: 46px;
      font-size: 15px;
    }
    .row .btn {
      min-height: 40px;
    }
    :host-context(.start) .row .btn {
      min-height: 46px;
    }
    .notice .btn {
      margin-top: 8px;
    }
    .candidates {
      list-style: none;
      margin: 0;
      padding: 0;
      display: grid;
      gap: 6px;
    }
    .candidate {
      display: flex;
      align-items: center;
      gap: 10px;
      padding: 8px 8px 8px 10px;
      border: 1px solid var(--line);
      border-radius: var(--radius-m);
      background: var(--surface);
    }
    .avatar {
      flex: none;
      width: 40px;
      height: 40px;
      border-radius: 50%;
      object-fit: cover;
      background: var(--chip-bg);
    }
    .names {
      flex: 1;
      min-width: 0;
    }
    .title {
      font-weight: 600;
      overflow: hidden;
      white-space: nowrap;
      text-overflow: ellipsis;
    }
    .sub {
      font-size: 12px;
      color: var(--text-subtle);
    }
    .in-lib {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      font-size: 13px;
      padding-right: 6px;
    }
    .limit {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 13px;
      color: var(--text-muted);
    }
    .limit .field {
      width: auto;
      min-height: 32px;
      padding: 0 8px;
    }
  `,
})
export class AddChannel {
  private readonly api = inject(Api);
  readonly large = input(false);

  protected readonly limitOptions = LIMIT_OPTIONS;
  protected readonly query = signal('');
  protected readonly busy = signal(false);
  protected readonly needsSearch = signal(false);
  protected readonly error = signal<string | null>(null);
  protected readonly candidates = signal<Candidate[]>([]);
  protected readonly limit = signal(200);
  protected readonly hasKey = computed(() => this.api.state()?.hasKey ?? true);

  constructor() {
    const defaultLimit = this.api.state()?.defaultLimit;
    if (defaultLimit) this.limit.set(defaultLimit);
  }

  protected onInput(value: string) {
    this.query.set(value);
    this.needsSearch.set(false);
    this.error.set(null);
  }

  protected async find(search: boolean) {
    const text = this.query().trim();
    if (!text) return;
    this.busy.set(true);
    this.error.set(null);
    this.candidates.set([]);
    try {
      const result = await this.api.resolve(text, search);
      this.needsSearch.set(!!result.needsSearch);
      // Даже если найден ровно один новый канал, добавляем его только
      // кнопкой. Так человек видит, тот ли это канал.
      this.candidates.set(result.channels);
    } catch (err) {
      this.needsSearch.set(false);
      this.error.set(err instanceof ApiError ? err.message : 'Не получилось найти канал');
    } finally {
      this.busy.set(false);
    }
  }

  protected async add(candidate: Candidate) {
    this.busy.set(true);
    this.error.set(null);
    try {
      await this.api.add(candidate.id, this.limit());
      this.candidates.set([]);
      this.query.set('');
    } catch (err) {
      this.error.set(err instanceof ApiError ? err.message : 'Не получилось добавить канал');
    } finally {
      this.busy.set(false);
    }
  }

  protected videoCount(candidate: Candidate) {
    return plural(candidate.videoCount, ['видео', 'видео', 'видео']);
  }
}
