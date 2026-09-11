/**
 * Which of the three KAPSORA apps an account has work in, tenant by tenant.
 *
 * The server does not know which app a request came from, and it does not need to: every
 * call is narrowed by the account's grants, so opening the wrong app shows nothing that
 * account could not read anyway. What it does not do is tell the person. A reviewer who
 * opens the member app would meet screens that answer "you are not bound to a person", and a
 * hospital clerk in the backoffice would meet a sidebar of pages that all refuse them. This
 * is the rule that turns both into one plain sentence.
 *
 * It mirrors how the server itself places a caller in a tenant, not a second opinion:
 *   - bound to a person (a PERSON grant, `personId` on the context) — the member app;
 *   - holding an ORGANIZATION grant — the provider portal, for that hospital or hotel;
 *   - neither, with permissions — the backoffice, for the tenant as a whole.
 *
 * The three are exclusive within one tenant because the server's narrowing is: a PERSON
 * grant makes every booking and reimbursement call a member's call, and an ORGANIZATION
 * grant narrows every provider-side read to that organization. An account that works in two
 * apps does so across tenants, and then each app lets it choose only among the tenants it
 * fits.
 */
import type { TenantContext } from '@kapsora/api-client';

export type KapsoraApp = 'backoffice' | 'provider' | 'member';

export const KAPSORA_APPS: readonly KapsoraApp[] = ['backoffice', 'provider', 'member'];

type Placement = Pick<TenantContext, 'permissions' | 'personId' | 'scopes'>;

function boundToPerson(ctx: Placement): boolean {
  return typeof ctx.personId === 'string' && ctx.personId !== '';
}

function holdsOrganization(ctx: Placement): boolean {
  return (ctx.scopes ?? []).some((s) => s.type === 'ORGANIZATION' && !!s.id);
}

/** Whether this tenant context gives the account work in the app. */
export function fitsApp(ctx: Placement, app: KapsoraApp): boolean {
  switch (app) {
    case 'member':
      return boundToPerson(ctx);
    case 'provider':
      return !boundToPerson(ctx) && holdsOrganization(ctx);
    case 'backoffice':
      return !boundToPerson(ctx) && !holdsOrganization(ctx) && ctx.permissions.length > 0;
  }
}

/** The apps an account has work in, across all of its tenants, in a fixed order. */
export function appsFor(tenants: readonly Placement[]): KapsoraApp[] {
  return KAPSORA_APPS.filter((app) => tenants.some((ctx) => fitsApp(ctx, app)));
}
