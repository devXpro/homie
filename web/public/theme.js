// Applies the saved appearance and language before the first paint (DESIGN.md §8.9, §11), so there is no flash and no
// inline script. Keep in sync with the UI package's lib/theme.ts and i18n (prefix "homie", src/setup.ts).
(function () {
  var pref = 'dark';
  var lang = '';
  try {
    pref = window.localStorage.getItem('homie.theme') || 'dark';
    lang = window.localStorage.getItem('homie.lang') || '';
  } catch (e) {
    pref = 'dark';
  }
  var dark = true;
  if (pref === 'light') {
    dark = false;
  } else if (pref === 'system' && window.matchMedia) {
    dark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  }
  var root = document.documentElement;
  root.setAttribute('data-theme', dark ? 'dark' : 'light');
  if (lang !== 'ru' && lang !== 'en') {
    var langs = (navigator.languages && navigator.languages.length ? navigator.languages : [navigator.language || 'ru']).join(',');
    lang = /(^|,)(ru|uk|be)\b/i.test(langs) ? 'ru' : 'en';
  }
  root.setAttribute('lang', lang);
  var meta = document.querySelector('meta[name="theme-color"]');
  if (meta) {
    meta.setAttribute('content', dark ? '#000000' : '#F2F2F7');
  }
})();
