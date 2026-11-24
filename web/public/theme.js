// Applies the saved appearance before the first paint (DESIGN.md §8.9), so there is no flash and no inline script.
// Keep in sync with src/lib/theme.ts.
(function () {
  var pref = 'dark';
  try {
    pref = window.localStorage.getItem('homie.theme') || 'dark';
  } catch (e) {
    pref = 'dark';
  }
  var dark = true;
  if (pref === 'light') {
    dark = false;
  } else if (pref === 'system' && window.matchMedia) {
    dark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  }
  document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
  var meta = document.querySelector('meta[name="theme-color"]');
  if (meta) {
    meta.setAttribute('content', dark ? '#000000' : '#F2F2F7');
  }
})();
