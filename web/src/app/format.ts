// Форматирование чисел, времени и дат для интерфейса.

const plurals = new Intl.PluralRules('ru');

// plural(5, ['видео', 'видео', 'видео']) — формы для one, few, many.
export function plural(n: number, [one, few, many]: [string, string, string]): string {
  const form = plurals.select(n);
  const word = form === 'one' ? one : form === 'few' ? few : many;
  return `${n.toLocaleString('ru')} ${word}`;
}

// Метка времени в видео: 0:07, 12:34, 1:02:03.
export function timecode(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`;
}

const dateFormat = new Intl.DateTimeFormat('ru', { day: 'numeric', month: 'short', year: 'numeric' });

export function date(iso: string | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? '' : dateFormat.format(d);
}

const timeFormat = new Intl.DateTimeFormat('ru', { hour: '2-digit', minute: '2-digit' });

export function clock(iso: string): string {
  return timeFormat.format(new Date(iso));
}

// Сколько осталось до момента: «48 с», «3 мин 05 с».
export function countdown(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000));
  if (s < 60) return `${s} с`;
  const m = Math.floor(s / 60);
  return `${m} мин ${String(s % 60).padStart(2, '0')} с`;
}

// Куски строки для подсветки: marks — [от, до) в кодовых точках, как
// считает Go-сервер, поэтому строка режется через Array.from, а не по UTF-16.
export interface Piece {
  text: string;
  mark: boolean;
}

export function highlight(text: string, marks: [number, number][] | undefined): Piece[] {
  if (!marks?.length) return [{ text, mark: false }];
  const chars = Array.from(text);
  const pieces: Piece[] = [];
  let pos = 0;
  for (const [from, to] of [...marks].sort((a, b) => a[0] - b[0])) {
    if (from > pos) pieces.push({ text: chars.slice(pos, from).join(''), mark: false });
    if (to > Math.max(from, pos)) pieces.push({ text: chars.slice(Math.max(from, pos), to).join(''), mark: true });
    pos = Math.max(pos, to);
  }
  if (pos < chars.length) pieces.push({ text: chars.slice(pos).join(''), mark: false });
  return pieces;
}

export function watchUrl(id: string, seconds: number): string {
  return `https://www.youtube.com/watch?v=${encodeURIComponent(id)}&t=${Math.floor(seconds)}s`;
}

export function thumbUrl(id: string): string {
  // mqdefault — 16:9 без чёрных полос, которые есть у hqdefault.
  return `https://i.ytimg.com/vi/${encodeURIComponent(id)}/mqdefault.jpg`;
}
