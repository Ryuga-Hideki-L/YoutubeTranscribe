import { NgTemplateOutlet } from '@angular/common';
import { ChangeDetectionStrategy, Component, computed, effect, inject, signal, untracked } from '@angular/core';
import { AddChannel } from './add-channel';
import { Api } from './api';
import { ChannelRow, Clock } from './channel-row';
import { clock } from './format';
import { Icon, IconName } from './icon';
import { Player, PlayerService } from './player';
import { Search } from './search';
import { ThemePreference, ThemeService } from './theme.service';

const THEME_NEXT: Record<ThemePreference, ThemePreference> = { system: 'light', light: 'dark', dark: 'system' };
const THEME_ICON: Record<ThemePreference, IconName> = { system: 'monitor', light: 'sun', dark: 'moon' };
const THEME_TITLE: Record<ThemePreference, string> = {
  system: 'Тема как в системе',
  light: 'Светлая тема',
  dark: 'Тёмная тема',
};

// Раскладка: слева каналы, в центре поиск, справа плеер. На узком экране
// плеер всплывает карточкой внизу, а каналы сворачиваются.
@Component({
  selector: 'app-root',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [AddChannel, ChannelRow, Icon, NgTemplateOutlet, Player, Search],
  template: `
    @if (!api.connected()) {
      <div class="offline" role="status">
        <svg appIcon name="loader-circle" [size]="14" class="spin"></svg>
        Нет связи с сервером, переподключаюсь…
      </div>
    }

    <header class="top">
      <a class="brand" href="/">
        <svg appIcon name="captions" [size]="22" class="logo"></svg>
        <span class="name">Дословно</span>
        <span class="tagline">поиск по субтитрам каналов YouTube</span>
      </a>
      <button class="btn icon quiet" type="button" (click)="cycleTheme($event)" [title]="themeTitle()">
        <svg appIcon [name]="themeIcon()" [size]="18"></svg>
        <span class="sr-only">{{ themeTitle() }}</span>
      </button>
    </header>

    @if (api.state() === null) {
      <p class="connecting subtle">Подключаюсь к серверу…</p>
    } @else if (api.channels().length === 0) {
      <main class="start">
        <h1>Найдите фразу в видео канала</h1>
        <p class="lead">
          Добавьте канал YouTube — скачаем субтитры его последних {{ api.state()?.defaultLimit }} видео.
          Потом любую фразу можно найти и открыть видео ровно с того места, где её сказали.
        </p>
        @if (!api.state()?.hasKey) {
          <ng-container *ngTemplateOutlet="noKey" />
        }
        @if (api.state()?.restricted) {
          <ng-container *ngTemplateOutlet="restricted" />
        }
        <app-add-channel [large]="true" />
        <p class="subtle small">Индексация идёт в фоне. На каждое видео уходит примерно 2–3 секунды, YouTube не любит спешку.</p>
      </main>
    } @else {
      <div class="layout" [class.playing]="player.current() !== null">
        <aside class="rail" aria-label="Каналы">
          <button class="rail-toggle btn" type="button" (click)="railOpen.set(!railOpen())" [attr.aria-expanded]="railOpen()">
            Каналы · {{ api.channels().length }}
            @if (api.indexing()) {
              <svg appIcon name="loader-circle" [size]="14" class="spin"></svg>
            }
            <svg appIcon name="chevron-down" [size]="16" class="chev" [class.up]="railOpen()"></svg>
          </button>
          <div class="rail-body" [class.open]="railOpen()">
            @if (!api.state()?.hasKey) {
              <ng-container *ngTemplateOutlet="noKey" />
            }
            @if (api.state()?.restricted) {
              <ng-container *ngTemplateOutlet="restricted" />
            }
            @if (adding()) {
              <div class="adding">
                <div class="adding-head">
                  <h2>Новый канал</h2>
                  <button class="btn icon quiet" type="button" (click)="adding.set(false)" title="Закрыть">
                    <svg appIcon name="x" [size]="16"></svg>
                    <span class="sr-only">Закрыть</span>
                  </button>
                </div>
                <app-add-channel />
              </div>
            } @else {
              <button class="btn add" type="button" (click)="adding.set(true)" [disabled]="!api.state()?.hasKey">
                <svg appIcon name="plus" [size]="16"></svg> Добавить канал
              </button>
            }
            @if (globalBlock(); as until) {
              <p class="block-note">
                <svg appIcon name="clock" [size]="14"></svg>
                YouTube притормозил запросы: следующая попытка в {{ until }}.
              </p>
            }
            @for (channel of api.channels(); track channel.id) {
              <app-channel-row [channel]="channel" />
            }
          </div>
        </aside>

        <main class="main">
          <app-search />
        </main>

        <app-player class="player" />
      </div>
    }

    <ng-template #noKey>
      <div class="notice">
        <svg appIcon name="key-round" [size]="16"></svg>
        <p>
          Нет ключа YouTube Data API, поэтому новые каналы не добавить. Поиск по скачанному работает.
          Впишите ключ в <code>YOUTUBE_API_KEY</code> в файле <code>.env</code> рядом с программой и перезапустите её.
        </p>
      </div>
    </ng-template>

    <ng-template #restricted>
      <div class="notice">
        <svg appIcon name="triangle-alert" [size]="16"></svg>
        <p>
          Сеть включает «Безопасный режим» YouTube. Так делают публичные Wi-Fi и родительские фильтры.
          Часть видео будет недоступна, их можно докачать после смены сети или DNS.
        </p>
      </div>
    </ng-template>
  `,
  styles: `
    :host {
      display: block;
      min-height: 100dvh;
      padding: 0 16px 24px;
      max-width: 1680px;
      margin: 0 auto;
    }
    .offline {
      position: sticky;
      top: 0;
      z-index: 10;
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 8px;
      margin: 0 -16px;
      padding: 6px 16px;
      background: var(--warn-block);
      border-bottom: 1px solid var(--warn-block-edge);
      color: var(--warn);
      font-size: 13px;
    }
    .top {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      height: 60px;
    }
    .brand {
      display: flex;
      align-items: baseline;
      gap: 10px;
      min-width: 0;
      text-decoration: none;
    }
    .logo {
      align-self: center;
      color: var(--accent);
    }
    .name {
      font-size: 19px;
      font-weight: 700;
      letter-spacing: -0.01em;
    }
    .tagline {
      font-size: 13px;
      color: var(--text-subtle);
      overflow: hidden;
      white-space: nowrap;
      text-overflow: ellipsis;
    }
    .connecting {
      padding: 64px 0;
      text-align: center;
    }
    .start {
      display: grid;
      gap: 16px;
      max-width: 620px;
      margin: 8vh auto 0;
    }
    .start h1 {
      font-size: 30px;
      line-height: 1.15;
      letter-spacing: -0.02em;
    }
    .lead {
      font-size: 15px;
      color: var(--text-muted);
    }
    .small {
      font-size: 13px;
    }
    .layout {
      display: grid;
      grid-template-columns: var(--rail) minmax(0, 1fr) clamp(360px, 32vw, 520px);
      gap: 24px;
      align-items: start;
    }
    .rail {
      position: sticky;
      top: 12px;
      max-height: calc(100dvh - 24px);
      overflow-y: auto;
      scrollbar-width: thin;
    }
    .rail-toggle {
      display: none;
    }
    .rail-body {
      display: grid;
      gap: 10px;
    }
    .add {
      justify-content: flex-start;
      min-height: 40px;
      border-style: dashed;
      color: var(--text-muted);
    }
    .adding {
      display: grid;
      gap: 10px;
      padding: 12px;
      border: 1px solid var(--line-strong);
      border-radius: var(--radius-m);
      background: var(--surface);
    }
    .adding-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin: -4px -6px -2px 0;
    }
    .adding h2 {
      font-size: 14px;
      font-weight: 600;
    }
    .block-note {
      display: flex;
      align-items: center;
      gap: 6px;
      font-size: 12px;
      color: var(--warn);
    }
    .main {
      min-width: 0;
    }
    .player {
      position: sticky;
      top: 12px;
    }

    /* Правой колонки нет: плеер становится карточкой в правом нижнем углу. */
    @media (max-width: 1279px) {
      .layout {
        grid-template-columns: var(--rail) minmax(0, 1fr);
      }
      .player {
        position: fixed;
        top: auto;
        right: 16px;
        bottom: 16px;
        z-index: 5;
        width: min(420px, calc(100vw - 32px));
        box-shadow: var(--shadow);
        border-radius: var(--radius-m);
        visibility: hidden;
        opacity: 0;
        transform: translateY(12px);
        transition:
          opacity 160ms var(--ease),
          transform 160ms var(--ease),
          visibility 0s 160ms;
      }
      .playing .player {
        visibility: visible;
        opacity: 1;
        transform: none;
        transition:
          opacity 240ms var(--ease),
          transform 240ms var(--ease);
      }
      /* --player-h ставит сам плеер через ResizeObserver. Это высота видео вместе со строкой названия. */
      .playing .main {
        padding-bottom: calc(var(--player-h, 320px) + 16px);
      }
    }

    /* Одна колонка. Каналы сворачиваются над поиском, плеер прижат к низу во всю ширину. */
    @media (max-width: 1023px) {
      .layout {
        grid-template-columns: minmax(0, 1fr);
        gap: 16px;
      }
      .rail {
        position: static;
        max-height: none;
        overflow: visible;
      }
      .rail-toggle {
        display: flex;
        width: 100%;
        justify-content: flex-start;
        min-height: 40px;
      }
      .chev {
        margin-left: auto;
        transition: transform 160ms var(--ease);
      }
      .chev.up {
        transform: rotate(180deg);
      }
      .rail-body {
        display: none;
        margin-top: 10px;
      }
      .rail-body.open {
        display: grid;
      }
      .player {
        right: 0;
        left: 0;
        bottom: 0;
        width: auto;
        max-width: 640px;
        margin: 0 auto;
      }
      .playing .main {
        padding-bottom: var(--player-h, 360px);
      }
    }

    @media (max-width: 560px) {
      .tagline {
        display: none;
      }
      .start {
        margin-top: 4vh;
      }
      .start h1 {
        font-size: 24px;
      }
    }
  `,
})
export class App {
  protected readonly api = inject(Api);
  protected readonly player = inject(PlayerService);
  private readonly theme = inject(ThemeService);
  private readonly now = inject(Clock).now;

  protected readonly adding = signal(false);
  // На узком экране каналы раскрываются, когда начинается индексация.
  // Когда она кончается, список остаётся как есть, иначе он свернулся бы
  // прямо под пальцем.
  protected readonly railOpen = signal(false);

  protected readonly themeIcon = computed(() => THEME_ICON[this.theme.preference()]);
  protected readonly themeTitle = computed(() => THEME_TITLE[this.theme.preference()]);

  // Общая пауза после блокировки. Показываем её, только если ни один канал
  // не показывает свою паузу.
  protected readonly globalBlock = computed(() => {
    const until = this.api.state()?.blockedUntil;
    if (!until || new Date(until).getTime() <= this.now()) return '';
    const shown = this.api.channels().some((channel) => channel.job?.state === 'cooling' || channel.job?.state === 'blocked');
    return shown ? '' : clock(until);
  });

  constructor() {
    let wasIndexing = false;
    effect(() => {
      const indexing = this.api.indexing();
      if (indexing && !wasIndexing) untracked(() => this.railOpen.set(true));
      wasIndexing = indexing;
    });
  }

  protected cycleTheme(event: MouseEvent) {
    this.theme.setPreference(THEME_NEXT[this.theme.preference()], event.currentTarget as HTMLElement);
  }
}
