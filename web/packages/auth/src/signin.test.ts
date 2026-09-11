import type { TenantContext } from '@kapsora/api-client';
import { describe, expect, it } from 'vitest';
import { afterSignIn, appUrlsFrom } from './signin';

const tenant = (
  apps: TenantContext['apps'],
  status: TenantContext['tenant']['status'] = 'ACTIVE',
): Pick<TenantContext, 'apps' | 'tenant'> => ({
  apps,
  tenant: { id: crypto.randomUUID(), code: 'T', displayName: 'T', status },
});

describe('the single sign-in', () => {
  it('stays, goes, lets the person choose, or says there is nowhere to go', () => {
    expect(afterSignIn([tenant(['backoffice'])], 'backoffice')).toEqual({ kind: 'stay' });
    expect(afterSignIn([tenant(['member'])], 'backoffice')).toEqual({ kind: 'go', app: 'member' });
    expect(afterSignIn([tenant(['backoffice', 'member'])], 'backoffice')).toEqual({
      kind: 'choose',
    });
    expect(afterSignIn([tenant(['provider']), tenant(['member'])], 'provider')).toEqual({
      kind: 'choose',
    });
    expect(afterSignIn([tenant([])], 'member')).toEqual({ kind: 'none' });
  });

  it('counts only active tenants', () => {
    expect(
      afterSignIn([tenant(['backoffice']), tenant(['member'], 'SUSPENDED')], 'backoffice'),
    ).toEqual({ kind: 'stay' });
  });

  it('finds the apps on the usual ports in development and on one origin when deployed', () => {
    const where = { protocol: 'http:', hostname: '127.0.0.1' };
    expect(appUrlsFrom({}, true, where)).toEqual({
      backoffice: 'http://127.0.0.1:5181/',
      provider: 'http://127.0.0.1:5182/',
      member: 'http://127.0.0.1:5183/',
    });
    expect(appUrlsFrom({}, false, where)).toEqual({
      backoffice: '/',
      provider: '/portal/',
      member: '/uye/',
    });
    expect(appUrlsFrom({ VITE_MEMBER_URL: 'http://127.0.0.1:5197/' }, true, where).member).toBe(
      'http://127.0.0.1:5197/',
    );
  });
});
