import { ApplicationRef, DestroyRef, Injectable, inject, signal } from '@angular/core';
import { MotionService } from './motion.service';

export type Theme = 'light' | 'dark';
export type ThemePreference = 'system' | Theme;

// Ключ и разбор должны совпадать со скриптом public/theme-init.js:
// он ставит data-theme до первой отрисовки, чтобы не мигала светлая тема.
const STORAGE_KEY = 'doslovno-theme';
const darkQuery = '(prefers-color-scheme: dark)';

function readPreference(): ThemePreference {
  try {
    const value = localStorage.getItem(STORAGE_KEY);
    return value === 'light' || value === 'dark' ? value : 'system';
  } catch {
    return 'system';
  }
}

function savePreference(preference: ThemePreference) {
  try {
    if (preference === 'system') localStorage.removeItem(STORAGE_KEY);
    else localStorage.setItem(STORAGE_KEY, preference);
  } catch {
    // Хранилище недоступно (приватный режим) — выбор живёт до перезагрузки.
  }
}

function systemTheme(): Theme {
  return window.matchMedia(darkQuery).matches ? 'dark' : 'light';
}

function currentTheme(): Theme {
  return document.documentElement.dataset['theme'] === 'dark' ? 'dark' : 'light';
}

function paintTheme(theme: Theme) {
  document.documentElement.dataset['theme'] = theme;
  const background = getComputedStyle(document.documentElement).getPropertyValue('--bg').trim();
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', background);
}

@Injectable({ providedIn: 'root' })
export class ThemeService {
  private readonly appRef = inject(ApplicationRef);
  private readonly motion = inject(MotionService);
  private readonly state = signal<Theme>(currentTheme());
  // Что выбрал человек (или «как в системе») и что в итоге на экране.
  readonly preference = signal<ThemePreference>(readPreference());
  readonly theme = this.state.asReadonly();
  // Тема, к которой идём. Переход применяет её кадром позже, поэтому
  // сравнивать новый выбор с DOM нельзя: при быстрой смене (тёмная →
  // светлая → тёмная) второй выбор совпал бы со старой темой в DOM и
  // потерялся бы.
  private target: Theme = currentTheme();
  private transitionToken = 0;

  constructor() {
    // При «как в системе» тема следует за системной на лету.
    const media = window.matchMedia(darkQuery);
    const onChange = () => {
      if (this.preference() === 'system') this.apply(systemTheme());
    };
    media.addEventListener('change', onChange);
    inject(DestroyRef).onDestroy(() => media.removeEventListener('change', onChange));
  }

  private apply(next: Theme) {
    this.target = next;
    paintTheme(next);
    this.state.set(next);
  }

  // Новая тема раскрывается кругом из нажатого элемента; без View Transitions
  // или с выключенными анимациями меняется сразу.
  setPreference(preference: ThemePreference, origin?: HTMLElement | null) {
    savePreference(preference);
    this.preference.set(preference);
    const next = preference === 'system' ? systemTheme() : preference;
    if (next === this.target) return;
    this.target = next;

    if (!document.startViewTransition || !this.motion.allowed() || !origin) {
      this.apply(next);
      return;
    }

    const rect = origin.getBoundingClientRect();
    const x = rect.left + rect.width / 2;
    const y = rect.top + rect.height / 2;
    const radius = Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y));

    const root = document.documentElement;
    // vt-theme снимает стандартное проявление снимков: круг раскрывает новую
    // тему сразу непрозрачной (styles.css).
    root.classList.add('vt-theme');
    // tick() отрисовывает изменения внутри снимка перехода — как flushSync в React.
    const token = ++this.transitionToken;
    const transition = document.startViewTransition(() => {
      this.apply(this.target);
      this.appRef.tick();
    });
    transition.ready
      .then(() => {
        root.animate(
          { clipPath: [`circle(0px at ${x}px ${y}px)`, `circle(${radius}px at ${x}px ${y}px)`] },
          { duration: 520, easing: 'cubic-bezier(0.2, 0.7, 0.2, 1)', pseudoElement: '::view-transition-new(root)' },
        );
      })
      // Переход пропущен (вкладка скрыта, начался другой) — тема уже применена.
      .catch(() => {});
    transition.finished
      .catch(() => {})
      .finally(() => {
        // Класс снимает только последний переход — иначе первый снял бы его во время второго.
        if (token === this.transitionToken) root.classList.remove('vt-theme');
      });
  }
}
