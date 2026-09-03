/**
 * Light/dark theme. The choice lives in memory for the tab; the OS preference is the
 * default. Nothing is written to browser storage (lint forbids setItem anyway).
 */
export type Theme = 'light' | 'dark';

export function currentTheme(): Theme {
  const explicit = document.documentElement.dataset['theme'];
  if (explicit === 'dark' || explicit === 'light') return explicit;
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function applyTheme(theme: Theme): void {
  document.documentElement.dataset['theme'] = theme;
}

export function toggleTheme(): Theme {
  const next: Theme = currentTheme() === 'dark' ? 'light' : 'dark';
  applyTheme(next);
  return next;
}
