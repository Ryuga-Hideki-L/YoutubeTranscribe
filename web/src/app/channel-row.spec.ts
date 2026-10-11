import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Api, ChannelView } from './api';
import { ChannelRow } from './channel-row';

function channel(cooldownUntil?: string): ChannelView {
  return {
    id: 'UC1',
    title: 'Канал',
    uploads: 'UU1',
    videoCount: 10,
    limit: 200,
    addedAt: '2026-10-11T10:00:00Z',
    counts: { total: 10, ok: 3, noCaptions: 0, failed: 0, pending: 7, lines: 100 },
    job: { channelId: 'UC1', state: 'cooling', total: 7, done: 0, ok: 0, noCaptions: 0, failed: 0, cooldownUntil, reason: 'blocked' },
  };
}

describe('ChannelRow', () => {
  beforeEach(() => {
    // Настоящий Api открывает EventSource, поэтому в тесте вместо него заглушка.
    TestBed.configureTestingModule({
      providers: [{ provide: Api, useValue: { state: signal(null), channels: signal([]) } }],
    });
  });

  it('не укорачивает анимацию паузы на новых снимках с той же паузой', () => {
    const fixture = TestBed.createComponent(ChannelRow);
    const until = new Date(Date.now() + 120_000).toISOString();
    fixture.componentRef.setInput('channel', channel(until));
    fixture.detectChanges();
    const bar = () => fixture.nativeElement.querySelector('.countdown') as HTMLElement;
    const first = bar();
    const duration = first.style.animationDuration;
    expect(parseFloat(duration)).toBeGreaterThan(100_000);

    // Новый снимок SSE: другой объект, но та же пауза. Элемент и длительность остаются прежними.
    vi.useFakeTimers({ now: Date.now() + 40_000 });
    fixture.componentRef.setInput('channel', channel(until));
    fixture.detectChanges();
    expect(bar()).toBe(first);
    expect(bar().style.animationDuration).toBe(duration);

    // Новая пауза даёт новый элемент с новой длительностью.
    const next = new Date(Date.now() + 60_000).toISOString();
    fixture.componentRef.setInput('channel', channel(next));
    fixture.detectChanges();
    expect(bar()).not.toBe(first);
    expect(parseFloat(bar().style.animationDuration)).toBeLessThanOrEqual(60_000);
    vi.useRealTimers();
  });
});
