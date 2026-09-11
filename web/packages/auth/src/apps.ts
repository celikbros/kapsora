/**
 * Which of the three KAPSORA apps an account has work in, tenant by tenant.
 *
 * The server decides it. Every tenant context carries `apps`: the apps the account's grants
 * belong to — a PERSON grant is the member app's, an ORGANIZATION grant the provider
 * portal's, a tenant-wide role the backoffice's. One account may hold all three kinds in one
 * tenant, and each app asks the server under its own name (X-Kapsora-App), so each gets only
 * its own grants. This file only reads the answer; there is no second rule on the client
 * that could disagree with the server's.
 */
import type { TenantContext } from '@kapsora/api-client';

export type KapsoraApp = 'backoffice' | 'provider' | 'member';

export const KAPSORA_APPS: readonly KapsoraApp[] = ['backoffice', 'provider', 'member'];

type Placement = Pick<TenantContext, 'apps'>;

/** Whether the account has work in the app in this tenant. */
export function fitsApp(ctx: Placement, app: KapsoraApp): boolean {
  return (ctx.apps ?? []).includes(app);
}

/** The apps an account has work in, across all of its tenants, in a fixed order. */
export function appsFor(tenants: readonly Placement[]): KapsoraApp[] {
  return KAPSORA_APPS.filter((app) => tenants.some((ctx) => fitsApp(ctx, app)));
}
