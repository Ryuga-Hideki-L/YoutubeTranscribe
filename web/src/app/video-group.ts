import { ChangeDetectionStrategy, Component, computed, inject, input, signal } from '@angular/core';
import { Hit, VideoHits } from './api';
import { date, highlight, plural, thumbUrl, timecode, watchUrl } from './format';
import { Icon } from './icon';
import { PlayerService } from './player';

const HITS_SHOWN_AT_FIRST = 3;

// Совпадения в одном видео. Сверху превью и название, ниже строки с метками времени.
@Component({
  selector: 'app-video-group',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [Icon],
  template: `
    @let videoHits = video();
    <header class="head">
      <button class="thumb" type="button" (click)="play(videoHits.hits[0])" [attr.aria-label]="'Смотреть: ' + videoHits.title">
        <img [src]="thumbnailUrl()" alt="" width="128" height="72" loading="lazy" referrerpolicy="no-referrer" />
        @if (videoHits.duration) {
          <span class="duration mono">{{ timecode(videoHits.duration) }}</span>
        }
      </button>
      <div class="info">
        <h3 class="title" [title]="videoHits.title">{{ videoHits.title }}</h3>
        <p class="meta">
          {{ videoHits.channelTitle }}
          @if (publishedDate()) {
            · {{ publishedDate() }}
          }
          · {{ hitsLine() }}
        </p>
      </div>
    </header>

    <ol class="hits">
      @for (hit of visibleHits(); track hit.s; let i = $index) {
        <li class="hit" [class.playing]="isPlaying(hit)" [class.appear]="i >= appearFrom()" [style.--i]="i - appearFrom()">
          <button class="time mono" type="button" (click)="play(hit)" [attr.aria-label]="'Смотреть с ' + timecode(hit.s)">
            {{ timecode(hit.s) }}
          </button>
          <div class="lines">
            @for (line of hit.lines; track $index) {
              <p class="line" [class.context]="!line.m">
                @for (piece of pieces(line.t, line.m); track $index) {
                  @if (piece.mark) {
                    <mark>{{ piece.text }}</mark>
                  } @else {
                    {{ piece.text }}
                  }
                }
              </p>
            }
          </div>
          <div class="row-actions">
            <a class="btn icon quiet" [href]="watch(hit)" target="_blank" rel="noopener" title="Открыть на YouTube с этого места">
              <svg appIcon name="external-link" [size]="15"></svg>
              <span class="sr-only">Открыть на YouTube</span>
            </a>
            <button class="btn icon quiet" type="button" (click)="copy(hit)" [title]="copied() === hit.s ? 'Скопировано' : 'Скопировать ссылку с меткой времени'">
              <svg appIcon [name]="copied() === hit.s ? 'check' : 'link'" [size]="15"></svg>
              <span class="sr-only">Скопировать ссылку</span>
            </button>
          </div>
        </li>
      }
    </ol>

    @if (hiddenCount() > 0) {
      <button class="btn quiet more" type="button" (click)="expand()">
        <svg appIcon name="chevron-down" [size]="16"></svg>
        Ещё {{ hiddenCount() }}
      </button>
    } @else if (videoHits.total > videoHits.hits.length) {
      <p class="cut subtle">Показаны первые {{ videoHits.hits.length }} из {{ videoHits.total }} — уточните запрос.</p>
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
  // С какого совпадения начинать проявление. После «Ещё» проявляются только новые.
  protected readonly appearFrom = signal(Infinity);

  protected readonly visibleHits = computed(() =>
    this.expanded() ? this.video().hits : this.video().hits.slice(0, HITS_SHOWN_AT_FIRST),
  );
  protected readonly hiddenCount = computed(() => this.video().hits.length - this.visibleHits().length);
  protected readonly thumbnailUrl = computed(() => thumbUrl(this.video().id));
  protected readonly publishedDate = computed(() => date(this.video().published));
  protected readonly hitsLine = computed(() =>
    plural(this.video().total, ['совпадение', 'совпадения', 'совпадений']),
  );

  protected expand() {
    this.appearFrom.set(HITS_SHOWN_AT_FIRST);
    this.expanded.set(true);
  }

  protected isPlaying(hit: Hit) {
    return this.player.isPlaying(this.video().id, hit.s);
  }

  protected play(hit: Hit) {
    const video = this.video();
    this.player.play({ id: video.id, title: video.title, channelTitle: video.channelTitle }, hit.s);
  }

  protected watch(hit: Hit) {
    return watchUrl(this.video().id, hit.s);
  }

  protected async copy(hit: Hit) {
    try {
      await navigator.clipboard.writeText(this.watch(hit));
      this.copied.set(hit.s);
      setTimeout(() => {
        if (this.copied() === hit.s) this.copied.set(null);
      }, 1200);
    } catch {
      // Буфер обмена недоступен. Ссылку всё равно можно открыть кнопкой рядом.
    }
  }
}
