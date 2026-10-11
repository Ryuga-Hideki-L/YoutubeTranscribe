import { ChangeDetectionStrategy, Component, computed, inject, input, signal } from '@angular/core';
import { Hit, VideoHits } from './api';
import { date, highlight, plural, thumbUrl, timecode, watchUrl } from './format';
import { Icon } from './icon';
import { PlayerService } from './player';

const SHOWN = 3;

// Совпадения в одном видео: шапка с превью и строки с метками времени.
@Component({
  selector: 'app-video-group',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [Icon],
  template: `
    @let v = video();
    <header class="head">
      <button class="thumb" type="button" (click)="play(v.hits[0])" [attr.aria-label]="'Смотреть: ' + v.title">
        <img [src]="thumb()" alt="" width="128" height="72" loading="lazy" referrerpolicy="no-referrer" />
        @if (v.duration) {
          <span class="duration mono">{{ timecode(v.duration) }}</span>
        }
      </button>
      <div class="info">
        <h3 class="title" [title]="v.title">{{ v.title }}</h3>
        <p class="meta">
          {{ v.channelTitle }}
          @if (published()) {
            · {{ published() }}
          }
          · {{ hitsLine() }}
        </p>
      </div>
    </header>

    <ol class="hits">
      @for (h of shown(); track h.s; let i = $index) {
        <li class="hit" [class.playing]="isPlaying(h)" [class.appear]="i >= appearFrom()" [style.--i]="i - appearFrom()">
          <button class="time mono" type="button" (click)="play(h)" [attr.aria-label]="'Смотреть с ' + timecode(h.s)">
            {{ timecode(h.s) }}
          </button>
          <div class="lines">
            @for (line of h.lines; track $index) {
              <p class="line" [class.context]="!line.m">
                @for (p of pieces(line.t, line.m); track $index) {
                  @if (p.mark) {
                    <mark>{{ p.text }}</mark>
                  } @else {
                    {{ p.text }}
                  }
                }
              </p>
            }
          </div>
          <div class="row-actions">
            <a class="btn icon quiet" [href]="watch(h)" target="_blank" rel="noopener" title="Открыть на YouTube с этого места">
              <svg appIcon name="external-link" [size]="15"></svg>
              <span class="sr-only">Открыть на YouTube</span>
            </a>
            <button class="btn icon quiet" type="button" (click)="copy(h)" [title]="copied() === h.s ? 'Скопировано' : 'Скопировать ссылку с меткой времени'">
              <svg appIcon [name]="copied() === h.s ? 'check' : 'link'" [size]="15"></svg>
              <span class="sr-only">Скопировать ссылку</span>
            </button>
          </div>
        </li>
      }
    </ol>

    @if (hidden() > 0) {
      <button class="btn quiet more" type="button" (click)="expand()">
        <svg appIcon name="chevron-down" [size]="16"></svg>
        Ещё {{ hidden() }}
      </button>
    } @else if (v.total > v.hits.length) {
      <p class="cut subtle">Показаны первые {{ v.hits.length }} из {{ v.total }} — уточните запрос.</p>
    }
  `,
  styles: `
    :host {
      display: block;
      border: 1px solid var(--line);
      border-radius: var(--radius-m);
      background: var(--surface);
      overflow: hidden;
    }
    .head {
      display: flex;
      gap: 12px;
      padding: 12px;
    }
    .thumb {
      position: relative;
      flex: none;
      width: 128px;
      padding: 0;
      border: 0;
      border-radius: var(--radius-s);
      overflow: hidden;
      background: var(--chip-bg);
      aspect-ratio: 16 / 9;
    }
    .thumb img {
      width: 100%;
      height: 100%;
      object-fit: cover;
    }
    .duration {
      position: absolute;
      right: 4px;
      bottom: 4px;
      padding: 0 4px;
      border-radius: 4px;
      background: rgba(0, 0, 0, 0.75);
      color: #fff;
      font-size: 11px;
    }
    .info {
      min-width: 0;
    }
    .title {
      font-size: 15px;
      font-weight: 600;
      line-height: 1.35;
      display: -webkit-box;
      -webkit-line-clamp: 2;
      -webkit-box-orient: vertical;
      overflow: hidden;
    }
    .meta {
      margin-top: 4px;
      font-size: 13px;
      color: var(--text-subtle);
    }
    .hits {
      list-style: none;
      margin: 0;
      padding: 0;
      border-top: 1px solid var(--line);
    }
    .hit {
      position: relative;
      display: grid;
      grid-template-columns: 64px minmax(0, 1fr) auto;
      gap: 8px;
      padding: 10px 8px 10px 12px;
    }
    .hit + .hit {
      border-top: 1px solid var(--line);
    }
    .hit.playing::before {
      content: '';
      position: absolute;
      left: 0;
      top: 8px;
      bottom: 8px;
      width: 2px;
      border-radius: 2px;
      background: var(--accent);
    }
    .time {
      align-self: start;
      min-height: 32px;
      padding: 0 6px;
      border-radius: var(--radius-s);
      border: 1px solid var(--chip-edge);
      background: var(--chip-bg);
      font-size: 12px;
      transition: background-color 160ms var(--ease);
    }
    .time:hover,
    .playing .time {
      background: var(--accent-block);
      border-color: var(--accent-block-edge);
    }
    .lines {
      padding-top: 5px;
    }
    .line {
      font-size: 14px;
    }
    .line.context {
      font-size: 13px;
      color: var(--text-muted);
    }
    .row-actions {
      display: flex;
      align-items: start;
      gap: 2px;
      opacity: 0;
      transition: opacity 160ms var(--ease);
    }
    .row-actions .btn.icon {
      width: 32px;
      min-height: 32px;
    }
    .hit:hover .row-actions,
    .hit:focus-within .row-actions {
      opacity: 1;
    }
    @media (hover: none) {
      .row-actions {
        opacity: 1;
      }
    }
    .more {
      width: 100%;
      border-top: 1px solid var(--line);
      border-radius: 0;
      min-height: 40px;
    }
    .cut {
      padding: 10px 12px;
      border-top: 1px solid var(--line);
      font-size: 13px;
    }
    @media (max-width: 480px) {
      .thumb {
        width: 104px;
      }
      .hit {
        grid-template-columns: 56px minmax(0, 1fr);
      }
      .row-actions {
        grid-column: 2;
        margin-top: -4px;
      }
    }
  `,
})
export class VideoGroup {
  readonly video = input.required<VideoHits>();
  private readonly player = inject(PlayerService);
  protected readonly timecode = timecode;
  protected readonly pieces = highlight;
  protected readonly expanded = signal(false);
  protected readonly copied = signal<number | null>(null);
  // С какого совпадения начинается проявление: при «Ещё» — только новые.
  protected readonly appearFrom = signal(Infinity);

  protected readonly shown = computed(() =>
    this.expanded() ? this.video().hits : this.video().hits.slice(0, SHOWN),
  );
  protected readonly hidden = computed(() => this.video().hits.length - this.shown().length);
  protected readonly thumb = computed(() => thumbUrl(this.video().id));
  protected readonly published = computed(() => date(this.video().published));
  protected readonly hitsLine = computed(() =>
    plural(this.video().total, ['совпадение', 'совпадения', 'совпадений']),
  );

  protected expand() {
    this.appearFrom.set(SHOWN);
    this.expanded.set(true);
  }

  protected isPlaying(h: Hit) {
    return this.player.isPlaying(this.video().id, h.s);
  }

  protected play(h: Hit) {
    const v = this.video();
    this.player.play({ id: v.id, title: v.title, channelTitle: v.channelTitle }, h.s);
  }

  protected watch(h: Hit) {
    return watchUrl(this.video().id, h.s);
  }

  protected async copy(h: Hit) {
    try {
      await navigator.clipboard.writeText(this.watch(h));
      this.copied.set(h.s);
      setTimeout(() => {
        if (this.copied() === h.s) this.copied.set(null);
      }, 1200);
    } catch {
      // Буфер обмена недоступен — ссылка всё равно открывается кнопкой рядом.
    }
  }
}
