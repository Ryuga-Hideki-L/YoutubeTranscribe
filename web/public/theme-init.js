// Ставит тему и анимации до первой отрисовки. Берёт выбор человека,
// а без него системные настройки.
// Это отдельный файл, а не встроенный скрипт, потому что CSP страницы
// запрещает встроенные.
// Ключи и значения такие же, как в src/app/theme.service.ts и motion.service.ts.
(function () {
  var root = document.documentElement;
  function read(key) {
    try {
      return localStorage.getItem(key);
    } catch (err) {
      return null;
    }
  }
  var theme = read("doslovno-theme");
  if (theme !== "light" && theme !== "dark") {
    theme = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  var motion = read("doslovno-motion");
  if (motion !== "on" && motion !== "off") {
    motion = window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "off" : "on";
  }
  root.dataset.theme = theme;
  root.dataset.motion = motion;
  var meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute("content", theme === "dark" ? "#1b1916" : "#f7f3e9");
})();
