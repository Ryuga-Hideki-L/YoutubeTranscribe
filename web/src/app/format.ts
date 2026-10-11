// Форматирование чисел, времени и дат для интерфейса.

const plurals = new Intl.PluralRules('ru');

// Пример: plural(5, ['видео', 'видео', 'видео']). Формы идут в порядке one, few, many.
export function plural(count: number, [one, few, many]: [string, string, string]): string {
  const form = plurals.select(count);
  const word = form === 'one' ? one : form === 'few' ? few : many;
  return `${count.toLocaleString('ru')} ${word}`;
}

// Метка времени в видео: 0:07, 12:34, 1:02:03.
export function timecode(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = String(total % 60).padStart(2, '0');
  return hours > 0 ? `${hours}:${String(minutes).padStart(2, '0')}:${secs}` : `${minutes}:${secs}`;
}

const dateFormat = new Intl.DateTimeFormat('ru', { day: 'numeric', month: 'short', year: 'numeric' });

export function date(iso: string | undefined): string {
  if (!iso) return '';
  const parsed = new Date(iso);
  return Number.isNaN(parsed.getTime()) || parsed.getFullYear() < 2000 ? '' : dateFormat.format(parsed);
}

const timeFormat = new Intl.DateTimeFormat('ru', { hour: '2-digit', minute: '2-digit' });

export function clock(iso: string): string {
  return timeFormat.format(new Date(iso));
}

// Сколько осталось до момента, например «48 с» или «3 мин 05 с».
export function countdown(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  if (total < 60) return `${total} с`;
  const minutes = Math.floor(total / 60);
  return `${minutes} мин ${String(total % 60).padStart(2, '0')} с`;
}

// Куски строки для подсветки. marks хранит пары [от, до) в кодовых точках,
// так же считает Go-сервер. Поэтому строку режем через Array.from, а не по UTF-16.
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
