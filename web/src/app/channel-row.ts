import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  Injectable,
  computed,
  effect,
  inject,
  input,
  linkedSignal,
  signal,
} from '@angular/core';
import { ACTIVE_STATES, Api, ApiError, ChannelView } from './api';
import { clock, countdown, plural } from './format';
import { Icon } from './icon';

// Текущее время для обратных отсчётов. Тикает раз в секунду, только пока
// где-то идёт пауза после блокировки. В покое таймеров нет.
@Injectable({ providedIn: 'root' })
export class Clock {
  readonly now = signal(Date.now());

  constructor() {
    const api = inject(Api);
    let timer: ReturnType<typeof setInterval> | undefined;
    const cooling = computed(
      () => !!api.state()?.blockedUntil || api.channels().some((channel) => channel.job?.state === 'cooling'),
    );
    effect(() => {
      if (cooling() && timer === undefined) {
        this.now.set(Date.now());
        timer = setInterval(() => this.now.set(Date.now()), 1000);
      } else if (!cooling() && timer !== undefined) {
        clearInterval(timer);
        timer = undefined;
      }
    });
    inject(DestroyRef).onDestroy(() => clearInterval(timer));
  }
}

const STATE_LABEL: Record<string, string> = {
  queued: 'в очереди',
  listing: 'список видео…',
  running: 'идёт',
  cooling: 'пауза',
  done: 'готово',
  blocked: 'заблокировано',
  cancelled: 'остановлено',
  failed: 'ошибка',
};

@Component({
  selector: 'app-channel-row',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [Icon],
  template: `
    @let channelView = channel();
    @let job = channelView.job;
    <div class="head">
      @if (channelView.thumb) {
        <img class="avatar" [src]="channelView.thumb" alt="" width="36" height="36" loading="lazy" referrerpolicy="no-referrer" />
      } @else {
        <span class="avatar empty"><svg appIcon name="captions" [size]="18"></svg></span>
      }
      <div class="names">
        <p class="title" [title]="channelView.title">{{ channelView.title }}</p>
        <p class="sub">
          @if (channelView.handle) {
            <span class="mono">{{ channelView.handle }}</span> ·
          }
          {{ videosLine() }}
        </p>
      </div>
      @if (stateLabel(); as label) {
        <span class="chip" [attr.data-state]="job?.state">
          @if (job?.state === 'running' || job?.state === 'listing') {
            <svg appIcon name="loader-circle" [size]="13" class="spin"></svg>
          }
          {{ label }}
        </span>
      }
    </div>

    @if (active()) {
      <div class="progress" [class.paused]="job?.state === 'cooling'" role="progressbar"
        [attr.aria-valuenow]="job?.done ?? 0" [attr.aria-valuemax]="job?.total ?? 0">
        @if (job?.state === 'cooling') {
          <!-- Отсчёт паузы: одна анимация ширины на всё время паузы. Новая
               пауза с другим cooldownUntil получает новый элемент и новую анимацию. -->
          @for (pause of pauses(); track pause.until) {
            <span class="bar countdown" [style.animation-duration.ms]="pause.ms"></span>
          }
        } @else {
          <span class="bar" [style.width.%]="percent()"></span>
        }
      </div>
      <p class="numbers mono">
        {{ job?.done ?? 0 }} из {{ job?.total ?? 0 }} · с субтитрами {{ job?.ok ?? 0 }} · без {{ job?.noCaptions ?? 0 }}
        @if (job?.failed) {
          · ошибки {{ job?.failed }}
        }
      </p>
      @if (job?.state === 'cooling') {
        <p class="current warn">
          @if (job?.reason === 'network') {
            Нет связи с YouTube — повторим через {{ remaining() }}
          } @else {
            YouTube притормозил запросы — продолжим через {{ remaining() }}
          }
        </p>
      } @else if (job?.current) {
        <p class="current" [title]="job?.current">{{ job?.current }}</p>
      }
    }

    @if (job?.state === 'blocked') {
      <div class="notice error">
        <svg appIcon name="triangle-alert" [size]="16"></svg>
        <div>
          <p>
            YouTube заблокировал запросы с этого адреса.
            @if (blockedUntil(); as until) {
              Сами попробуем снова после {{ until }}.
            }
          </p>
          <p class="hint">Можно подождать, сменить сеть или запустить сервер с <code>HTTPS_PROXY</code>.</p>
        </div>
      </div>
    } @else if (job?.state === 'failed') {
      <div class="notice error">
        <svg appIcon name="triangle-alert" [size]="16"></svg>
        <div>
          @if (job?.reason === 'network') {
            <p>Нет связи с YouTube — индексация остановлена, нескачанные видео остались в очереди. Проверьте сеть и нажмите «Докачать».</p>
            <p class="hint">{{ job?.error }}</p>
          } @else {
            <p>{{ job?.error }}</p>
          }
        </div>
      </div>
    } @else if (!active() && channelView.counts.failed > 0) {
      <p class="current warn">{{ failedLine() }}</p>
    }

    @if (actionError(); as message) {
      <p class="current warn">{{ message }}</p>
    }

    <div class="actions">
      @if (active()) {
        <button class="btn quiet" type="button" (click)="act(api.cancel(channelView.id))">
          <svg appIcon name="square" [size]="14"></svg> Остановить
        </button>
      } @else if (job?.state === 'blocked') {
        <button class="btn" type="button" (click)="act(api.refresh(channelView.id, true))">
          <svg appIcon name="refresh-cw" [size]="14"></svg> Повторить сейчас
        </button>
        <button class="btn quiet" type="button" (click)="act(api.cancel(channelView.id))" title="Не повторять самим после паузы">
          Отменить
        </button>
      } @else {
        <button class="btn quiet" type="button" (click)="act(api.refresh(channelView.id))"
          title="Проверить новые видео и докачать то, что не удалось">
          <svg appIcon name="refresh-cw" [size]="14"></svg>
          {{ channelView.counts.failed || channelView.counts.pending ? 'Докачать' : 'Обновить' }}
        </button>
      }
      <span class="spacer"></span>
      @if (confirmDelete()) {
        <button class="btn danger" type="button" (click)="act(api.remove(channelView.id))">Удалить канал?</button>
        <button class="btn quiet icon" type="button" (click)="confirmDelete.set(false)" title="Не удалять">
          <svg appIcon name="x" [size]="16"></svg>
          <span class="sr-only">Не удалять</span>
        </button>
      } @else {
        <button class="btn quiet icon" type="button" (click)="confirmDelete.set(true)" title="Удалить канал и его субтитры">
          <svg appIcon name="trash" [size]="16"></svg>
          <span class="sr-only">Удалить канал</span>
        </button>
      }
    </div>
  `,
  styles: `
    :host {
      display: grid;
      gap: 8px;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: var(--radius-m);
      background: var(--surface);
    }
    .head {
      display: flex;
      align-items: center;
      gap: 10px;
      min-width: 0;
    }
    .avatar {
      flex: none;
      width: 36px;
      height: 36px;
      border-radius: 50%;
      object-fit: cover;
      background: var(--chip-bg);
    }
    .avatar.empty {
      display: grid;
      place-items: center;
      color: var(--text-subtle);
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
      overflow: hidden;
      white-space: nowrap;
      text-overflow: ellipsis;
    }
    .chip {
      flex: none;
      display: inline-flex;
      align-items: center;
      gap: 4px;
      padding: 2px 8px;
      border-radius: 999px;
      border: 1px solid var(--chip-edge);
      background: var(--chip-bg);
      font-size: 12px;
      color: var(--text-muted);
    }
    .chip[data-state='done'] {
      color: var(--ok);
    }
    .chip[data-state='cooling'] {
      color: var(--warn);
    }
    .chip[data-state='blocked'],
    .chip[data-state='failed'] {
      color: var(--danger);
    }
    .progress {
      height: 3px;
      border-radius: 2px;
      background: var(--chip-bg);
      overflow: hidden;
    }
    .bar {
      display: block;
      height: 100%;
      background: var(--accent);
      border-radius: 2px;
      transition: width 400ms linear;
    }
    .paused .bar {
      background: var(--warn);
    }
    .countdown {
      width: 100%;
      animation: countdown linear both;
    }
    @keyframes countdown {
      to {
        width: 0;
      }
    }
    .numbers {
      font-size: 12px;
      color: var(--text-muted);
    }
    .current {
      font-size: 12px;
      color: var(--text-subtle);
      overflow: hidden;
      white-space: nowrap;
      text-overflow: ellipsis;
    }
    .current.warn {
      color: var(--warn);
      white-space: normal;
    }
    .hint {
      margin-top: 4px;
      color: var(--text-muted);
    }
    .actions {
      display: flex;
      align-items: center;
      gap: 4px;
      margin: 0 -6px -6px;
    }
    .actions .btn {
      min-height: 32px;
      padding: 0 8px;
    }
    .actions .btn.icon {
      width: 32px;
      padding: 0;
    }
    .spacer {
      flex: 1;
    }
  `,
})
export class ChannelRow {
  readonly channel = input.required<ChannelView>();
  protected readonly api = inject(Api);
  private readonly clockNow = inject(Clock).now;
  protected readonly confirmDelete = signal(false);
  protected readonly actionError = signal<string | null>(null);

  protected readonly active = computed(() => {
    const job = this.channel().job;
    return !!job && ACTIVE_STATES.includes(job.state);
  });

  protected readonly stateLabel = computed(() => {
    const job = this.channel().job;
    if (!job) return '';
    if (job.state === 'cooling') return `пауза ${this.remaining()}`;
    return STATE_LABEL[job.state] ?? '';
  });

  protected readonly percent = computed(() => {
    const job = this.channel().job;
    return job && job.total > 0 ? (job.done / job.total) * 100 : 0;
  });

  // Пауза и сколько от неё осталось в момент, когда её увидели.
  // Длительность считаем один раз на каждый cooldownUntil. Снимки SSE
  // приходят и посреди паузы, и без этого анимация укорачивалась бы,
  // а полоска прыгала вперёд.
  protected readonly pauses = linkedSignal<string | undefined, { until: string; ms: number }[]>({
    source: () => this.channel().job?.cooldownUntil,
    computation: (until, previous) => {
      // Вход меняется на каждом снимке. Та же пауза сохраняет прежнюю длительность.
      if (previous && previous.source === until) return previous.value;
      return until ? [{ until, ms: Math.max(0, new Date(until).getTime() - Date.now()) }] : [];
    },
  });

  protected readonly remaining = computed(() => {
    const until = this.channel().job?.cooldownUntil;
    return until ? countdown(new Date(until).getTime() - this.clockNow()) : '';
  });

  protected readonly blockedUntil = computed(() => {
    const until = this.api.state()?.blockedUntil;
    return until ? clock(until) : '';
  });

  protected readonly videosLine = computed(() => {
    const counts = this.channel().counts;
    if (counts.total === 0) return 'видео ещё не получены';
    return `${plural(counts.ok, ['видео', 'видео', 'видео'])} с субтитрами из ${counts.total}`;
  });

  protected readonly failedLine = computed(() => {
    const counts = this.channel().counts;
    return `${plural(counts.failed, ['видео не скачалось', 'видео не скачались', 'видео не скачались'])} — можно докачать`;
  });

  protected async act(action: Promise<unknown>) {
    this.actionError.set(null);
    this.confirmDelete.set(false);
    try {
      await action;
    } catch (err) {
      this.actionError.set(err instanceof ApiError ? err.message : 'Не получилось');
    }
  }
}
