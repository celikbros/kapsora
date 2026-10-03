/** The reads a route mounts and the grants its primary action needs. */
export type PortalArea =
  | 'newRequest'
  | 'requests'
  | 'eligibility'
  | 'cases'
  | 'caseOpen'
  | 'report'
  | 'stay'
  | 'claims'
  | 'claimNew'
  | 'inventory'
  | 'desk'
  | 'earnings'
  | 'invoices'
  | 'invoiceNew'
  | 'batches'
  | 'batchNew'
  | 'statement';

const required: Record<PortalArea, readonly string[]> = {
  newRequest: [
    'service_request.create',
    'service_request.submit',
    'member.read',
    'catalog.read',
    'eligibility.check',
  ],
  requests: ['service_request.read'],
  eligibility: ['eligibility.check', 'member.read', 'catalog.read'],
  cases: ['health.case.read'],
  caseOpen: [
    'health.case.read',
    'health.case.manage',
    'member.read',
    'catalog.read',
    'eligibility.check',
  ],
  report: ['health.case.read'],
  stay: ['health.case.read'],
  claims: ['claim.read'],
  claimNew: ['claim.create', 'claim.read'],
  inventory: ['accommodation.property.read', 'accommodation.inventory.manage'],
  desk: ['accommodation.property.read', 'accommodation.booking.manage'],
  earnings: ['claim.read'],
  invoices: ['invoice.read'],
  invoiceNew: ['invoice.manage', 'invoice.read'],
  batches: ['invoice.read'],
  batchNew: ['batch.create', 'invoice.read'],
  statement: ['report.read'],
};

export function canOpen(
  area: PortalArea,
  permissions: readonly string[],
  fromRequest = false,
): boolean {
  return (
    required[area].every((permission) => permissions.includes(permission)) &&
    (area !== 'caseOpen' || !fromRequest || permissions.includes('service_request.read'))
  );
}

export function billingDestination(
  permissions: readonly string[],
): '/billing' | '/billing/invoices' | '/billing/statement' | null {
  if (canOpen('earnings', permissions)) return '/billing';
  if (canOpen('invoices', permissions)) return '/billing/invoices';
  if (canOpen('statement', permissions)) return '/billing/statement';
  return null;
}

export function portalLanding(permissions: readonly string[]): string | null {
  if (canOpen('newRequest', permissions)) return '/';
  const billing = billingDestination(permissions);
  if (billing) return billing;
  if (canOpen('desk', permissions)) return '/lodging/desk';
  if (canOpen('inventory', permissions)) return '/lodging/inventory';
  if (canOpen('requests', permissions)) return '/requests';
  if (canOpen('eligibility', permissions)) return '/eligibility';
  if (canOpen('cases', permissions)) return '/cases';
  if (canOpen('claims', permissions)) return '/claims';
  return null;
}

export function portalNav(permissions: readonly string[]): { key: string; path: string }[] {
  const entries: { key: string; path: string }[] = [];
  if (canOpen('newRequest', permissions)) entries.push({ key: 'newRequest', path: '/' });
  if (canOpen('requests', permissions)) entries.push({ key: 'myRequests', path: '/requests' });
  if (canOpen('eligibility', permissions))
    entries.push({ key: 'eligibility', path: '/eligibility' });
  if (canOpen('cases', permissions)) entries.push({ key: 'cases', path: '/cases' });
  if (canOpen('claims', permissions)) entries.push({ key: 'claims', path: '/claims' });
  if (canOpen('inventory', permissions))
    entries.push({ key: 'inventory', path: '/lodging/inventory' });
  if (canOpen('desk', permissions)) entries.push({ key: 'desk', path: '/lodging/desk' });
  const billing = billingDestination(permissions);
  if (billing) entries.push({ key: 'billing', path: billing });
  return entries;
}
