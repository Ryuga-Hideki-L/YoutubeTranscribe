import { ChangeDetectionStrategy, Component, input } from '@angular/core';

// Сгенерировано scripts/fetch-icons.sh из Lucide 1.55.0 (ISC) — не править руками.

export type IconName =
  | 'search'
  | 'plus'
  | 'x'
  | 'refresh-cw'
  | 'square'
  | 'trash'
  | 'external-link'
  | 'copy'
  | 'check'
  | 'play'
  | 'sun'
  | 'moon'
  | 'monitor'
  | 'chevron-down'
  | 'triangle-alert'
  | 'loader-circle'
  | 'clock'
  | 'captions'
  | 'key-round'
  | 'link'
  | 'list-video';

// Значок интерфейса: <svg appIcon name="search" [size]="18"></svg>.
@Component({
  selector: 'svg[appIcon]',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    'aria-hidden': 'true',
    fill: 'none',
    viewBox: '0 0 24 24',
    stroke: 'currentColor',
    'stroke-linecap': 'round',
    'stroke-linejoin': 'round',
    '[attr.stroke-width]': 'stroke()',
    '[attr.width]': 'size()',
    '[attr.height]': 'size()',
  },
  template: `
    @switch (name()) {
      @case ('search') {
        <svg:path d="m21 21-4.34-4.34" />
        <svg:circle cx="11" cy="11" r="8" />
      }
      @case ('plus') {
        <svg:path d="M5 12h14" />
        <svg:path d="M12 5v14" />
      }
      @case ('x') {
        <svg:path d="M18 6 6 18" />
        <svg:path d="m6 6 12 12" />
      }
      @case ('refresh-cw') {
        <svg:path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8" />
        <svg:path d="M21 3v5h-5" />
        <svg:path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16" />
        <svg:path d="M8 16H3v5" />
      }
      @case ('square') {
        <svg:rect width="18" height="18" x="3" y="3" rx="2" />
      }
      @case ('trash') {
        <svg:path d="M10 11v6" />
        <svg:path d="M14 11v6" />
        <svg:path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
        <svg:path d="M3 6h18" />
        <svg:path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
      }
      @case ('external-link') {
        <svg:path d="M15 3h6v6" />
        <svg:path d="M10 14 21 3" />
        <svg:path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
      }
      @case ('copy') {
        <svg:rect width="14" height="14" x="8" y="8" rx="2" ry="2" />
        <svg:path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" />
      }
      @case ('check') {
        <svg:path d="M20 6 9 17l-5-5" />
      }
      @case ('play') {
        <svg:path d="M5 5a2 2 0 0 1 3.008-1.728l11.997 6.998a2 2 0 0 1 .003 3.458l-12 7A2 2 0 0 1 5 19z" />
      }
      @case ('sun') {
        <svg:circle cx="12" cy="12" r="4" />
        <svg:path d="M12 2v2" />
        <svg:path d="M12 20v2" />
        <svg:path d="m4.93 4.93 1.41 1.41" />
        <svg:path d="m17.66 17.66 1.41 1.41" />
        <svg:path d="M2 12h2" />
        <svg:path d="M20 12h2" />
        <svg:path d="m6.34 17.66-1.41 1.41" />
        <svg:path d="m19.07 4.93-1.41 1.41" />
      }
      @case ('moon') {
        <svg:path d="M20.985 12.486a9 9 0 1 1-9.473-9.472c.405-.022.617.46.402.803a6 6 0 0 0 8.268 8.268c.344-.215.825-.004.803.401" />
      }
      @case ('monitor') {
        <svg:rect width="20" height="14" x="2" y="3" rx="2" />
        <svg:line x1="8" x2="16" y1="21" y2="21" />
        <svg:line x1="12" x2="12" y1="17" y2="21" />
      }
      @case ('chevron-down') {
        <svg:path d="m6 9 6 6 6-6" />
      }
      @case ('triangle-alert') {
        <svg:path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3" />
        <svg:path d="M12 9v4" />
        <svg:path d="M12 17h.01" />
      }
      @case ('loader-circle') {
        <svg:path d="M21 12a9 9 0 1 1-6.219-8.56" />
      }
      @case ('clock') {
        <svg:circle cx="12" cy="12" r="10" />
        <svg:path d="M12 6v6l4 2" />
      }
      @case ('captions') {
        <svg:rect width="18" height="14" x="3" y="5" rx="2" ry="2" />
        <svg:path d="M7 15h4M15 15h2M7 11h2M13 11h4" />
      }
      @case ('key-round') {
        <svg:path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z" />
        <svg:circle cx="16.5" cy="7.5" r=".5" fill="currentColor" />
      }
      @case ('link') {
        <svg:path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
        <svg:path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />
      }
      @case ('list-video') {
        <svg:path d="M21 5H3" />
        <svg:path d="M10 12H3" />
        <svg:path d="M10 19H3" />
        <svg:path d="M15 12.003a1 1 0 0 1 1.517-.859l4.997 2.997a1 1 0 0 1 0 1.718l-4.997 2.997a1 1 0 0 1-1.517-.86z" />
      }
    }
  `,
})
export class Icon {
  readonly name = input.required<IconName>();
  readonly size = input(18);
  readonly stroke = input(1.8);
}
