// Выбор человека в настройках хранится в localStorage, «как в системе» -
// это отсутствие ключа. Тот же формат читает public/theme-init.js

export function readChoice<T extends string>(key: string, choices: readonly T[]): T | 'system' {
  try {
    const value = localStorage.getItem(key);
    return choices.find((choice) => choice === value) ?? 'system';
  } catch {
    return 'system';
  }
}

export function saveChoice(key: string, choice: string) {
  try {
    if (choice === 'system') localStorage.removeItem(key);
    else localStorage.setItem(key, choice);
  } catch {
    // Хранилище недоступно, например в приватном режиме, выбор живёт до перезагрузки
  }
}
