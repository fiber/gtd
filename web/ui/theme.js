// Applies the colour theme before the first paint. A classic script, loaded in
// the head. The choice (light, dark or system) is kept per device.
(function () {
  var KEY = 'gtd.theme';
  var dark = window.matchMedia('(prefers-color-scheme: dark)');
  function choice() {
    try { return localStorage.getItem(KEY) || 'light'; } catch (e) { return 'light'; }
  }
  function apply() {
    var c = choice();
    document.documentElement.dataset.theme = c === 'system' ? (dark.matches ? 'dark' : 'light') : c;
  }
  dark.addEventListener('change', apply);
  // gtdTheme() returns the choice; gtdTheme('dark') sets it.
  window.gtdTheme = function (set) {
    if (set) {
      try { localStorage.setItem(KEY, set); } catch (e) { /* private mode */ }
      apply();
    }
    return choice();
  };
  apply();
})();
