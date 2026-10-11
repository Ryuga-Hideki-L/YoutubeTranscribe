// Прокси для ng serve: /api уходит на Go-сервер, запущенный с -dev.
// Адрес берётся из DOSLOVNO_API, его ставит ./tasks.sh dev.
// По умолчанию это 127.0.0.1:3000.
export default {
  '/api': {
    target: process.env.DOSLOVNO_API ?? 'http://127.0.0.1:3000',
    secure: false,
  },
};
