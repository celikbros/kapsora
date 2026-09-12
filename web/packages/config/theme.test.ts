import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

/**
 * The dark palette is written twice in theme.css — once for the theme button's explicit
 * choice, once for the operating system's preference — because CSS cannot share a
 * declaration block between a selector and a media query. These tests are what keeps the
 * two copies one palette.
 */

const css = readFileSync(fileURLToPath(new URL('./theme.css', import.meta.url)), 'utf8');

/** The declarations of the first rule whose selector line contains `selector`. */
function declarations(selector: string): string[] {
  const start = css.indexOf(selector);
  expect(start, `${selector} is missing from theme.css`).toBeGreaterThan(-1);
  const open = css.indexOf('{', start);
  const close = css.indexOf('}', open);
  return css
    .slice(open + 1, close)
    .split(';')
    .map((one) => one.trim())
    .filter((one) => one.length > 0);
}

describe('theme.css', () => {
  it('paints the same dark palette whichever way dark is reached', () => {
    const chosen = declarations("[data-theme='dark']");
    const fromSystem = declarations(":root:not([data-theme='light'])");

    expect(fromSystem).toEqual(chosen);
    expect(chosen).toContain('color-scheme: dark');
  });

  it('offers the system palette only inside the dark media query', () => {
    const rule = css.indexOf(":root:not([data-theme='light'])");
    const query = css.lastIndexOf('@media (prefers-color-scheme: dark)', rule);

    expect(query).toBeGreaterThan(-1);
    expect(css.slice(query, rule)).not.toContain('}');
  });

  it('lets an explicit light choice win on a dark machine', () => {
    // Both selectors weigh the same, so the media rule must come second in the file to win
    // when it matches; :not() is what stops it matching a person who chose light.
    expect(css.indexOf(":root:not([data-theme='light'])")).toBeGreaterThan(
      css.indexOf(":root[data-theme='dark']"),
    );
  });
});
