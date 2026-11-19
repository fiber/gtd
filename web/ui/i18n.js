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
// ("Agenda · Dana").
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

// How long ago a moment was, in the user's language: "3 days ago", "last month".
export function fmtAgo(iso) {
  if (!iso) return '';
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' });
  const s = (new Date(iso).getTime() - Date.now()) / 1000;
  const units = [['year', 31557600], ['month', 2629800], ['week', 604800], ['day', 86400], ['hour', 3600], ['minute', 60]];
  for (const [unit, size] of units) {
    if (Math.abs(s) >= size) return rtf.format(Math.round(s / size), unit);
  }
  return rtf.format(0, 'minute');
}

// The day of a moment, without the time: "12 Mar 2024".
export function fmtDay(iso) {
  if (!iso) return '';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium' }).format(new Date(iso));
}

export function fmtTime(iso) {
  if (!iso) return '';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso));
}
