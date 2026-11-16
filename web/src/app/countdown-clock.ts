import { DestroyRef, Injectable, computed, effect, inject, signal } from '@angular/core';
import { Api } from './api';

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
