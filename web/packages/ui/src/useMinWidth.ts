import { useEffect, useState } from 'react';

/**
 * Whether the viewport is at least `px` wide, kept current as it changes. A screen uses it
 * to render one of two structures — a table from `md` up, stacked rows below — rather than
 * both with one hidden, so a test and a screen reader meet the same single control set.
 *
 * Where `matchMedia` does not exist (jsdom, a server render) the answer is "wide", which is
 * the layout every test in this repository was written against.
 */
export function useMinWidth(px: number): boolean {
  const query = `(min-width: ${px}px)`;
  const [matches, setMatches] = useState(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return true;
    return window.matchMedia(query).matches;
  });
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const list = window.matchMedia(query);
    const onChange = () => setMatches(list.matches);
    onChange();
    list.addEventListener('change', onChange);
    return () => list.removeEventListener('change', onChange);
  }, [query]);
  return matches;
}
