import { describe, expect, it } from 'vitest';
import { appsFor, fitsApp } from './apps';

describe('which app an account has work in', () => {
  it('reads the apps the server listed for each tenant', () => {
    expect(fitsApp({ apps: ['backoffice'] }, 'backoffice')).toBe(true);
    expect(fitsApp({ apps: ['backoffice'] }, 'member')).toBe(false);
    expect(fitsApp({ apps: ['backoffice', 'member'] }, 'member')).toBe(true);
  });

  it('unions the apps across tenants in a fixed order', () => {
    expect(appsFor([{ apps: ['member'] }, { apps: ['backoffice'] }])).toEqual([
      'backoffice',
      'member',
    ]);
    expect(appsFor([{ apps: ['member', 'provider', 'backoffice'] }])).toEqual([
      'backoffice',
      'provider',
      'member',
    ]);
  });

  it('gives an account the server listed no app for no app at all', () => {
    expect(appsFor([{ apps: [] }])).toEqual([]);
    expect(appsFor([])).toEqual([]);
  });
});
