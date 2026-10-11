import { DestroyRef, Injectable, computed, inject, signal } from '@angular/core';

// Типы — зеркало JSON Go-сервера (server/internal/api, library, indexer).

export type JobState =
  | 'queued'
  | 'listing'
  | 'running'
  | 'cooling'
  | 'done'
  | 'blocked'
  | 'cancelled'
  | 'failed';

export interface Job {
  channelId: string;
  state: JobState;
  total: number;
  done: number;
  ok: number;
  noCaptions: number;
  failed: number;
  current?: string;
  cooldownUntil?: string;
  reason?: 'blocked' | 'network';
  error?: string;
  startedAt?: string;
  finishedAt?: string;
}

export interface Counts {
  total: number;
  ok: number;
  noCaptions: number;
  failed: number;
  pending: number;
  lines: number;
}

export interface Channel {
  id: string;
  title: string;
  handle?: string;
  thumb?: string;
  uploads: string;
  videoCount: number;
}

export interface ChannelView extends Channel {
  limit: number;
  addedAt: string;
  indexedAt?: string;
  counts: Counts;
  job?: Job;
}

export interface LibraryState {
  hasKey: boolean;
  defaultLimit: number;
  blockedUntil?: string;
  restricted?: boolean;
  channels: ChannelView[];
}

export interface Candidate extends Channel {
  inLibrary: boolean;
}

export interface ResolveResult {
  needsSearch?: boolean;
  channels: Candidate[];
}

export interface HitLine {
  s: number;
  t: string;
  m?: [number, number][];
}

export interface Hit {
  s: number;
  lines: HitLine[];
}

export interface VideoHits {
  id: string;
  title: string;
  published: string;
  duration?: number;
  channelId: string;
  channelTitle: string;
  total: number;
  hits: Hit[];
}

export interface SearchResult {
  query: string;
  totalHits: number;
  totalVideos: number;
  videos: VideoHits[];
}

// Ошибка API: code — для логики (no_key, quota, not_found…), message — человеку.
export class ApiError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

export const ACTIVE_STATES: JobState[] = ['queued', 'listing', 'running', 'cooling'];

async function request<T>(method: string, url: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, {
      method,
      signal,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError('network', 'Нет связи с сервером');
  }
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    throw new ApiError(data?.code ?? 'http', data?.error ?? `Сервер ответил ${res.status}`);
  }
  return data as T;
}

// Связь с Go-сервером. Состояние библиотеки приходит потоком SSE
// (/api/events): полный снимок при подключении и после каждого изменения.
// EventSource сам переподключается, пока сервер недоступен — тогда
// connected() = false и интерфейс это показывает.
@Injectable({ providedIn: 'root' })
export class Api {
  readonly state = signal<LibraryState | null>(null);
  readonly connected = signal(true);

  readonly channels = computed(() => this.state()?.channels ?? []);
  // Сколько строк субтитров уже можно искать — по изменению видно, что
  // результаты поиска устарели.
  readonly searchableLines = computed(() => this.channels().reduce((n, c) => n + c.counts.lines, 0));
  readonly indexing = computed(() =>
    this.channels().some((c) => c.job && ACTIVE_STATES.includes(c.job.state)),
  );

  constructor() {
    const events = new EventSource('/api/events');
    events.addEventListener('state', (e) => {
      this.state.set(JSON.parse((e as MessageEvent<string>).data));
      this.connected.set(true);
    });
    events.addEventListener('error', () => this.connected.set(false));
    inject(DestroyRef).onDestroy(() => events.close());
  }

  resolve(query: string, search = false) {
    return request<ResolveResult>('POST', '/api/resolve', { query, search });
  }

  async add(id: string, limit: number) {
    this.state.set(await request<LibraryState>('POST', '/api/channels', { id, limit }));
  }

  async refresh(id: string, force = false) {
    this.state.set(await request<LibraryState>('POST', `/api/channels/${encodeURIComponent(id)}/refresh`, { force }));
  }

  async cancel(id: string) {
    this.state.set(await request<LibraryState>('POST', `/api/channels/${encodeURIComponent(id)}/cancel`, {}));
  }

  async remove(id: string) {
    this.state.set(await request<LibraryState>('DELETE', `/api/channels/${encodeURIComponent(id)}`));
  }

  search(q: string, channel: string, signal?: AbortSignal) {
    const params = new URLSearchParams({ q });
    if (channel) params.set('channel', channel);
    return request<SearchResult>('GET', `/api/search?${params}`, undefined, signal);
  }
}
