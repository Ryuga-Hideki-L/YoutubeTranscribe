import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  ElementRef,
  Injectable,
  computed,
  effect,
  inject,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { Icon } from './icon';
import { timecode, watchUrl } from './format';

export interface Playing {
  id: string;
  title: string;
  channelTitle: string;
  start: number;
  // Номер нажатия: повторный клик по той же метке тоже перематывает.
  seq: number;
}

// Что играет сейчас. Плеер один на всё приложение: клик по другой метке
// того же видео — перемотка, по другому видео — loadVideoById, без
// пересоздания iframe.
@Injectable({ providedIn: 'root' })
export class PlayerService {
  readonly current = signal<Playing | null>(null);
  private seq = 0;

  play(video: { id: string; title: string; channelTitle: string }, start: number) {
    this.current.set({ ...video, start, seq: ++this.seq });
  }

  close() {
    this.current.set(null);
  }

  isPlaying(id: string, start: number) {
    const c = this.current();
    return c !== null && c.id === id && c.start === start;
  }
}

// Минимум IFrame API YouTube, который здесь нужен.
interface YTPlayer {
  loadVideoById(args: { videoId: string; startSeconds: number }): void;
  seekTo(seconds: number, allowSeekAhead: boolean): void;
  playVideo(): void;
  pauseVideo(): void;
  getVideoData?(): { video_id?: string };
}

interface YTNamespace {
  Player: new (
    el: HTMLElement,
    options: {
      host: string;
      videoId: string;
      playerVars: Record<string, number | string>;
      events: {
        onReady?: (e: { target: YTPlayer }) => void;
        onError?: (e: { data: number }) => void;
      };
    },
  ) => YTPlayer;
}

declare global {
  interface Window {
    YT?: YTNamespace;
    onYouTubeIframeAPIReady?: () => void;
  }
}

let apiLoading: Promise<YTNamespace> | null = null;

// Скрипт IFrame API грузится один раз и только когда человек впервые нажал
// на метку времени.
function loadApi(): Promise<YTNamespace> {
  apiLoading ??= new Promise((resolve, reject) => {
    if (window.YT?.Player) {
      resolve(window.YT);
      return;
    }
    window.onYouTubeIframeAPIReady = () => resolve(window.YT!);
    const script = document.createElement('script');
    script.src = 'https://www.youtube.com/iframe_api';
    script.onerror = () => {
      apiLoading = null;
      reject(new Error('не загрузился плеер YouTube'));
    };
    document.head.append(script);
  });
  return apiLoading;
}

function errorText(code: number): string {
  switch (code) {
    case 101:
    case 150:
      return 'Автор запретил показывать это видео на других сайтах.';
    case 100:
      return 'Видео удалено или стало приватным.';
    case 153:
      return 'YouTube не принял страницу без адреса источника.';
    default:
      return 'Плеер YouTube не смог открыть видео.';
  }
}

@Component({
  selector: 'app-player',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [Icon],
  host: {
    '[class.open]': 'current() !== null',
    '[attr.aria-label]': '"Плеер"',
    role: 'region',
  },
  template: `
    <div class="frame">
      <div class="video" [class.hidden]="current() === null">
        <div #mount></div>
        @if (error(); as e) {
          <div class="video-error">
            <p>{{ e }}</p>
            @if (current(); as c) {
              <a class="btn" [href]="link()" target="_blank" rel="noopener">
                <svg appIcon name="external-link" [size]="16"></svg> Открыть на YouTube
              </a>
            }
          </div>
        }
      </div>
      @if (current(); as c) {
        <div class="meta">
          <div class="text">
            <p class="title" [title]="c.title">{{ c.title }}</p>
            <p class="sub">
              <span>{{ c.channelTitle }}</span> · <span class="mono">{{ timecode(c.start) }}</span>
            </p>
          </div>
          <a class="btn icon quiet" [href]="link()" target="_blank" rel="noopener" title="Открыть на YouTube">
            <svg appIcon name="external-link" [size]="17"></svg>
            <span class="sr-only">Открыть на YouTube</span>
          </a>
          <button class="btn icon quiet" type="button" (click)="close()" title="Закрыть плеер">
            <svg appIcon name="x" [size]="18"></svg>
            <span class="sr-only">Закрыть плеер</span>
          </button>
        </div>
      } @else {
        <div class="placeholder">
          <svg appIcon name="play" [size]="22"></svg>
          <p>Нажмите на метку времени — видео начнётся с этого места.</p>
        </div>
      }
    </div>
  `,
  styles: `
    :host {
      display: block;
    }
    .frame {
      border: 1px solid var(--line);
      border-radius: var(--radius-m);
      background: var(--surface);
      overflow: hidden;
    }
    /* Узкий экран: плеер прижат к низу экрана — скругление только сверху. */
    @media (max-width: 1023px) {
      .frame {
        border-bottom: 0;
        border-radius: var(--radius-m) var(--radius-m) 0 0;
      }
    }
    .video {
      position: relative;
      aspect-ratio: 16 / 9;
      background: #000;
    }
    .video.hidden {
      display: none;
    }
    .video-error {
      position: absolute;
      inset: 0;
      display: grid;
      place-content: center;
      justify-items: center;
      gap: 12px;
      padding: 16px;
      text-align: center;
      background: var(--surface-raised);
      color: var(--text);
    }
    .meta {
      display: flex;
      align-items: center;
      gap: 4px;
      padding: 10px 8px 10px 14px;
    }
    .text {
      flex: 1;
      min-width: 0;
    }
    .title {
      font-weight: 600;
      font-size: 14px;
      overflow: hidden;
      white-space: nowrap;
      text-overflow: ellipsis;
    }
    .sub {
      font-size: 13px;
      color: var(--text-muted);
      overflow: hidden;
      white-space: nowrap;
      text-overflow: ellipsis;
    }
    .placeholder {
      display: grid;
      justify-items: center;
      gap: 10px;
      aspect-ratio: 16 / 9;
      align-content: center;
      padding: 24px;
      text-align: center;
      color: var(--text-subtle);
      font-size: 13px;
    }
  `,
})
export class Player {
  private readonly svc = inject(PlayerService);
  private readonly mount = viewChild.required<ElementRef<HTMLElement>>('mount');
  protected readonly current = this.svc.current;
  protected readonly error = signal<string | null>(null);
  protected readonly timecode = timecode;
  protected readonly link = computed(() => {
    const c = this.current();
    return c ? watchUrl(c.id, c.start) : '';
  });
  private player: YTPlayer | null = null;
  private playerReady: Promise<YTPlayer> | null = null;

  constructor() {
    effect(() => {
      const c = this.current();
      if (c) untracked(() => void this.show(c));
    });
    // Высота плеера — в --player-h на <html>: на узком экране плеер висит
    // поверх результатов, и отступ под ним считается от настоящей высоты
    // (видео плюс строка с названием), а не на глаз.
    const host = inject(ElementRef<HTMLElement>).nativeElement;
    const root = document.documentElement;
    const observer = new ResizeObserver(() => root.style.setProperty('--player-h', `${host.offsetHeight}px`));
    observer.observe(host);
    inject(DestroyRef).onDestroy(() => {
      observer.disconnect();
      root.style.removeProperty('--player-h');
    });
  }

  private async show(c: Playing) {
    this.error.set(null);
    try {
      const player = await this.ensurePlayer(c);
      if (player.getVideoData?.().video_id === c.id) {
        player.seekTo(c.start, true);
      } else {
        player.loadVideoById({ videoId: c.id, startSeconds: Math.floor(c.start) });
      }
      player.playVideo();
    } catch {
      this.error.set('Не загрузился плеер YouTube — проверьте сеть.');
    }
  }

  // Первый показ создаёт плеер сразу с нужным видео; дальше он только
  // переключается.
  private ensurePlayer(c: Playing): Promise<YTPlayer> {
    this.playerReady ??= loadApi().then(
      (YT) =>
        new Promise<YTPlayer>((resolve) => {
          this.player = new YT.Player(this.mount().nativeElement, {
            host: 'https://www.youtube-nocookie.com',
            videoId: c.id,
            playerVars: { start: Math.floor(c.start), autoplay: 1, playsinline: 1, rel: 0 },
            events: {
              onReady: (e) => resolve(e.target),
              onError: (e) => this.error.set(errorText(e.data)),
            },
          });
        }),
    );
    this.playerReady.catch(() => (this.playerReady = null));
    return this.playerReady;
  }

  protected close() {
    this.player?.pauseVideo();
    this.svc.close();
  }
}
