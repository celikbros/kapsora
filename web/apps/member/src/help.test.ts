import { helpPage, initI18n } from '@kapsora/i18n';
import { beforeAll, describe, expect, it } from 'vitest';
import { HELP_ROUTES } from './help';

beforeAll(() => {
  initI18n('tr');
});

describe('page help', () => {
  it('is written for every route in the table and for the app itself', () => {
    const keys = [...new Set(Object.values(HELP_ROUTES)), 'app'];
    const missing = keys.filter((key) => helpPage('member', key) === undefined);
    expect(missing).toEqual([]);
  });

  it('says what each page is for, in whole sentences', () => {
    const keys = [...new Set(Object.values(HELP_ROUTES)), 'app'];
    for (const key of keys) {
      const page = helpPage('member', key);
      expect(page?.title, key).toBeTruthy();
      expect(page?.purpose, key).toMatch(/\.$/);
      for (const entry of [
        ...(page?.sections ?? []),
        ...(page?.actions ?? []),
        ...(page?.statuses ?? []),
      ]) {
        expect(entry.label, `${key}: ${entry.label}`).toBeTruthy();
        expect(entry.body, `${key}: ${entry.label}`).toMatch(/\.$/);
      }
    }
  });
});
