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

// Что играет сейчас. Плеер один на всё приложение. Клик по другой метке
// того же видео перематывает, клик по другому видео вызывает loadVideoById.
// Iframe при этом не пересоздаётся.
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
    const playing = this.current();
    return playing !== null && playing.id === id && playing.start === start;
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
    element: HTMLElement,
    options: {
      host: string;
      videoId: string;
      playerVars: Record<string, number | string>;
      events: {
        onReady?: (event: { target: YTPlayer }) => void;
        onError?: (event: { data: number }) => void;
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

// Скрипт IFrame API грузится один раз, при первом нажатии на метку времени.
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
        @if (error(); as message) {
          <div class="video-error">
            <p>{{ message }}</p>
            @if (current()) {
              <a class="btn" [href]="link()" target="_blank" rel="noopener">
                <svg appIcon name="external-link" [size]="16"></svg> Открыть на YouTube
              </a>
            }
          </div>
        }
      </div>
      @if (current(); as playing) {
        <div class="meta">
          <div class="text">
            <p class="title" [title]="playing.title">{{ playing.title }}</p>
            <p class="sub">
              <span>{{ playing.channelTitle }}</span> · <span class="mono">{{ timecode(playing.start) }}</span>
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
    /* Узкий экран. Плеер прижат к низу, поэтому скруглён только сверху. */
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
  private readonly playerService = inject(PlayerService);
  private readonly mount = viewChild.required<ElementRef<HTMLElement>>('mount');
  protected readonly current = this.playerService.current;
  protected readonly error = signal<string | null>(null);
  protected readonly timecode = timecode;
  protected readonly link = computed(() => {
    const playing = this.current();
    return playing ? watchUrl(playing.id, playing.start) : '';
  });
  private player: YTPlayer | null = null;
  private playerReady: Promise<YTPlayer> | null = null;

  constructor() {
    effect(() => {
      const playing = this.current();
      if (playing) untracked(() => void this.show(playing));
    });
    // Высоту плеера пишем в --player-h на <html>. На узком экране плеер
    // висит поверх результатов, и отступ под ним считается от настоящей
    // высоты: видео плюс строка названия.
    const host = inject(ElementRef<HTMLElement>).nativeElement;
    const root = document.documentElement;
    const observer = new ResizeObserver(() => root.style.setProperty('--player-h', `${host.offsetHeight}px`));
    observer.observe(host);
    inject(DestroyRef).onDestroy(() => {
      observer.disconnect();
      root.style.removeProperty('--player-h');
    });
  }

  private async show(playing: Playing) {
    this.error.set(null);
    try {
      const player = await this.ensurePlayer(playing);
      if (player.getVideoData?.().video_id === playing.id) {
        player.seekTo(playing.start, true);
      } else {
        player.loadVideoById({ videoId: playing.id, startSeconds: Math.floor(playing.start) });
      }
      player.playVideo();
    } catch {
      this.error.set('Не загрузился плеер YouTube — проверьте сеть.');
    }
  }

  // Первый показ создаёт плеер сразу с нужным видео. Дальше плеер только
  // переключается.
  private ensurePlayer(playing: Playing): Promise<YTPlayer> {
    this.playerReady ??= loadApi().then(
      (youtube) =>
        new Promise<YTPlayer>((resolve) => {
          this.player = new youtube.Player(this.mount().nativeElement, {
            host: 'https://www.youtube-nocookie.com',
            videoId: playing.id,
            playerVars: { start: Math.floor(playing.start), autoplay: 1, playsinline: 1, rel: 0 },
            events: {
              onReady: (event) => resolve(event.target),
              onError: (event) => this.error.set(errorText(event.data)),
            },
          });
        }),
    );
    this.playerReady.catch(() => (this.playerReady = null));
    return this.playerReady;
  }

  protected close() {
    this.player?.pauseVideo();
    this.playerService.close();
  }
}
