// i18n: JSON catalogs (the same files the server uses) and Intl for dates.

let catalog = {};
let fallback = {};
let locale = 'en';

export async function loadLanguage(lang) {
  const load = async (l) => (await fetch(`ui/i18n/${l}.json`)).json();
  fallback = await load('en');
  catalog = lang === 'en' ? fallback : await load(lang);
  locale = lang;
  document.documentElement.lang = lang;
}

// t returns the text for a key, with {name} placeholders filled from vars.
export function t(key, vars) {
  let s = catalog[key] ?? fallback[key] ?? key;
  for (const [k, v] of Object.entries(vars || {})) s = s.replaceAll(`{${k}}`, v);
  return s;
}

// label renders a wizard option or chip: literal text, an i18n label, or both
// ("Agenda · Eric").
export function label(o) {
  if (o.label && o.text) return `${t(o.label)} · ${o.text}`;
  return o.text || (o.label ? t(o.label) : o.value);
}

// A YYYY-MM-DD date in the user's locale. Parsed as a local date, not UTC.
export function fmtDate(ymd) {
  if (!ymd) return '';
  const [y, m, d] = ymd.split('-').map(Number);
  return new Intl.DateTimeFormat(locale, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' })
    .format(new Date(y, m - 1, d));
}

export function fmtTime(iso) {
  if (!iso) return '';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso));
}
