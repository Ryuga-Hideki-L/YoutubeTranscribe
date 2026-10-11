import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Api, ChannelView, JobState, LibraryState } from './api';
import { App } from './app';

function state(jobState?: JobState): LibraryState {
  const c: ChannelView = {
    id: 'UC1',
    title: 'Канал',
    uploads: 'UU1',
    videoCount: 2,
    limit: 200,
    addedAt: '2026-10-11T10:00:00Z',
    counts: { total: 2, ok: 0, noCaptions: 0, failed: 0, pending: 2, lines: 0 },
    job: jobState ? { channelId: 'UC1', state: jobState, total: 2, done: 0, ok: 0, noCaptions: 0, failed: 0 } : undefined,
  };
  return { hasKey: true, defaultLimit: 200, channels: [c] };
}

describe('App', () => {
  beforeAll(() => {
    // В jsdom нет matchMedia — темам и анимациям хватит «не совпало».
    window.matchMedia ??= ((query: string) => ({
      matches: false,
      media: query,
      addEventListener() {},
      removeEventListener() {},
    })) as unknown as typeof window.matchMedia;
    // И нет ResizeObserver — плеер меряет им свою высоту.
    globalThis.ResizeObserver ??= class {
      observe() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  });

  it('раскрывает каналы на старте индексации и не сворачивает их на конце', () => {
    const st = signal<LibraryState | null>(state());
    const indexing = signal(false);
    TestBed.configureTestingModule({
      providers: [
        {
          provide: Api,
          useValue: {
            state: st,
            channels: () => st()?.channels ?? [],
            indexing,
            connected: signal(true),
            searchableLines: signal(0),
          },
        },
      ],
    });
    const fixture = TestBed.createComponent(App);
    fixture.detectChanges();
    const open = () => (fixture.componentInstance as unknown as { railOpen: () => boolean }).railOpen();
    expect(open()).toBe(false);

    indexing.set(true);
    st.set(state('running'));
    fixture.detectChanges();
    expect(open()).toBe(true);

    indexing.set(false);
    st.set(state('done'));
    fixture.detectChanges();
    expect(open()).toBe(true);
  });
});
