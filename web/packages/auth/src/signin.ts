/**
 * The single sign-in: whichever app's sign-in screen a person uses, they end up where their
 * account has work. One app — straight there. Several — they choose. None — they are told.
 *
 * The three apps share one session. In development they run on three ports of one host, and
 * a cookie is not told apart by port; in a deployment they are three paths of one origin.
 * Either way, signing in once signs in everywhere, which is what lets this hand a person to
 * another app with a plain page load.
 */
import type { TenantContext } from '@kapsora/api-client';
import { appsFor, KAPSORA_APPS, type KapsoraApp } from './apps';

export type AppUrls = Record<KapsoraApp, string>;

/** The development ports the start commands and the demo launcher use. */
const DEV_PORTS: Record<KapsoraApp, number> = { backoffice: 5181, provider: 5182, member: 5183 };
/** One origin, three paths: one cookie, so one sign-in (deploy/nginx, deploy/caddy). */
const DEPLOYED_PATHS: AppUrls = { backoffice: '/', provider: '/portal/', member: '/uye/' };

/**
 * Where each app lives. VITE_BACKOFFICE_URL, VITE_PROVIDER_URL and VITE_MEMBER_URL win when
 * set; otherwise a development server assumes the same host on the usual ports and a build
 * assumes the deployed paths.
 */
export function appUrlsFrom(
  env: Record<string, unknown>,
  dev: boolean,
  where: { protocol: string; hostname: string },
): AppUrls {
  const out = { ...DEPLOYED_PATHS };
  for (const app of KAPSORA_APPS) {
    const set = env[`VITE_${app.toUpperCase()}_URL`];
    if (typeof set === 'string' && set !== '') out[app] = set;
    else if (dev) out[app] = `${where.protocol}//${where.hostname}:${DEV_PORTS[app]}/`;
  }
  return out;
}

export type AfterSignIn =
  { kind: 'stay' } | { kind: 'go'; app: KapsoraApp } | { kind: 'choose' } | { kind: 'none' };

/**
 * What the sign-in screen of `current` does once the account is known: stay when this is the
 * account's only app, go when its only app is another one, let the person choose when it has
 * several, and say so when it has none.
 */
export function afterSignIn(
  tenants: readonly Pick<TenantContext, 'apps' | 'tenant'>[],
  current: KapsoraApp,
): AfterSignIn {
  const fits = appsFor(tenants.filter((t) => t.tenant.status === 'ACTIVE'));
  if (fits.length === 0) return { kind: 'none' };
  if (fits.length > 1) return { kind: 'choose' };
  return fits[0] === current ? { kind: 'stay' } : { kind: 'go', app: fits[0]! };
}

/** Leaving for another app is a full page load. Tests replace `assign`. */
export const browser = {
  assign(url: string): void {
    window.location.assign(url);
  },
};
