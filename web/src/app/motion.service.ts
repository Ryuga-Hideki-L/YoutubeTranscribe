import { DestroyRef, Injectable, computed, effect, inject, signal } from '@angular/core';
import { readChoice, saveChoice } from './choice-storage';

export type MotionPreference = 'system' | 'on' | 'off';

// Ключ и разбор должны совпадать со скриптом public/theme-init.js.
// Он ставит data-motion до первой отрисовки.
const STORAGE_KEY = 'doslovno-motion';
const reducedQuery = '(prefers-reduced-motion: reduce)';

// Анимации включает выбор человека или системная настройка «уменьшить
// движение». Итог пишется в html[data-motion="on|off"]. По нему CSS глушит
// переходы, а раскрытие темы смотрит на allowed().
@Injectable({ providedIn: 'root' })
export class MotionService {
  readonly preference = signal<MotionPreference>(readChoice(STORAGE_KEY, ['on', 'off']));
  private readonly systemReduced = signal(window.matchMedia(reducedQuery).matches);

  readonly allowed = computed(() => {
    const preference = this.preference();
    return preference === 'on' || (preference === 'system' && !this.systemReduced());
  });

  constructor() {
    const media = window.matchMedia(reducedQuery);
    const onChange = (event: MediaQueryListEvent) => this.systemReduced.set(event.matches);
    media.addEventListener('change', onChange);
    inject(DestroyRef).onDestroy(() => media.removeEventListener('change', onChange));

    effect(() => {
      document.documentElement.dataset['motion'] = this.allowed() ? 'on' : 'off';
    });
  }

  setPreference(preference: MotionPreference) {
    saveChoice(STORAGE_KEY, preference);
    this.preference.set(preference);
  }
}
