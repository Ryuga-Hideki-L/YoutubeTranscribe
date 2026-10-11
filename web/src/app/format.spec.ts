import { countdown, highlight, plural, timecode } from './format';

describe('format', () => {
  it('склоняет числа по-русски', () => {
    const forms: [string, string, string] = ['совпадение', 'совпадения', 'совпадений'];
    expect(plural(1, forms)).toBe('1 совпадение');
    expect(plural(3, forms)).toBe('3 совпадения');
    expect(plural(11, forms)).toBe('11 совпадений');
    expect(plural(22, forms)).toBe('22 совпадения');
  });

  it('пишет метку времени', () => {
    expect(timecode(7)).toBe('0:07');
    expect(timecode(754.9)).toBe('12:34');
    expect(timecode(3723)).toBe('1:02:03');
  });

  it('считает обратный отсчёт', () => {
    expect(countdown(48_000)).toBe('48 с');
    expect(countdown(185_000)).toBe('3 мин 05 с');
  });

  it('режет строку по подсветке в кодовых точках, а не в UTF-16', () => {
    // Сервер считает позиции в символах: эмодзи — один символ, не два.
    const pieces = highlight('🎉 ёлка и ёлка', [
      [2, 6],
      [9, 13],
    ]);
    expect(pieces).toEqual([
      { text: '🎉 ', mark: false },
      { text: 'ёлка', mark: true },
      { text: ' и ', mark: false },
      { text: 'ёлка', mark: true },
    ]);
    expect(highlight('текст', undefined)).toEqual([{ text: 'текст', mark: false }]);
  });
});
