// Прокси ng serve: /api уходит на Go-сервер в режиме -dev. Адрес задаёт
// ./tasks.sh dev через DOSLOVNO_API, по умолчанию — 127.0.0.1:3000.
export default {
  '/api': {
    target: process.env.DOSLOVNO_API ?? 'http://127.0.0.1:3000',
    secure: false,
  },
};
