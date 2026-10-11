import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Api, ChannelView, JobState, LibraryState } from './api';
import { App } from './app';

function state(jobState?: JobState): LibraryState {
  const channel: ChannelView = {
    id: 'UC1',
    title: 'Канал',
    uploads: 'UU1',
    videoCount: 2,
    limit: 200,
    addedAt: '2026-10-11T10:00:00Z',
    counts: { total: 2, ok: 0, noCaptions: 0, failed: 0, pending: 2, lines: 0 },
    job: jobState ? { channelId: 'UC1', state: jobState, total: 2, done: 0, ok: 0, noCaptions: 0, failed: 0 } : undefined,
  };
  return { hasKey: true, defaultLimit: 200, channels: [channel] };
}

describe('App', () => {
  beforeAll(() => {
    // В jsdom нет matchMedia. Темам и анимациям хватит ответа «не совпало».
    window.matchMedia ??= ((query: string) => ({
      matches: false,
      media: query,
      addEventListener() {},
      removeEventListener() {},
    })) as unknown as typeof window.matchMedia;
    // ResizeObserver в jsdom тоже нет. Плеер меряет им свою высоту.
    globalThis.ResizeObserver ??= class {
      observe() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  });

  it('раскрывает каналы на старте индексации и не сворачивает их на конце', () => {
    const libraryState = signal<LibraryState | null>(state());
    const indexing = signal(false);
    TestBed.configureTestingModule({
      providers: [
        {
          provide: Api,
          useValue: {
            state: libraryState,
            channels: () => libraryState()?.channels ?? [],
            indexing,
            connected: signal(true),
            searchableLines: signal(0),
          },
        },
      ],
    });
    const fixture = TestBed.createComponent(App);
    fixture.detectChanges();
    const isOpen = () => (fixture.componentInstance as unknown as { railOpen: () => boolean }).railOpen();
    expect(isOpen()).toBe(false);

    indexing.set(true);
    libraryState.set(state('running'));
    fixture.detectChanges();
    expect(isOpen()).toBe(true);

    indexing.set(false);
    libraryState.set(state('done'));
    fixture.detectChanges();
    expect(isOpen()).toBe(true);
  });
});
