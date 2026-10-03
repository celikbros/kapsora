/** Backoffice main navigation (v1.2 section 15.6). Unimplemented entries open "Yakında". */
export interface NavEntry {
  key: string;
  path: string;
  /** i18n key under nav.* */
  labelKey: string;
  implemented: boolean;
  /** Permission that must be present for the entry to show; undefined = always. */
  permission?: string;
  /** The path prefix that marks the entry current, when the section is wider than its landing list. */
  match?: string;
}

export const NAV_ENTRIES: NavEntry[] = [
  { key: 'home', path: '/', labelKey: 'nav.home', implemented: true },
  {
    key: 'people',
    path: '/people',
    labelKey: 'nav.people',
    implemented: true,
    permission: 'member.read',
  },
  {
    key: 'programs',
    path: '/programs',
    labelKey: 'nav.programs',
    implemented: true,
    permission: 'program.read',
  },
  { key: 'wallets', path: '/wallets', labelKey: 'nav.wallets', implemented: true },
  {
    key: 'adjustments',
    path: '/entitlement-adjustments',
    labelKey: 'nav.adjustments',
    implemented: true,
    permission: 'entitlement.adjust',
  },
  {
    key: 'imports',
    path: '/imports',
    labelKey: 'nav.imports',
    implemented: true,
    permission: 'import.execute',
  },
  {
    key: 'catalog',
    path: '/catalog/definitions',
    labelKey: 'nav.catalog',
    implemented: true,
    permission: 'catalog.read',
  },
  {
    key: 'codeSystems',
    path: '/catalog/code-systems',
    labelKey: 'nav.codeSystems',
    implemented: true,
    permission: 'catalog.read',
  },
  {
    key: 'contracts',
    path: '/contracts',
    labelKey: 'nav.contracts',
    implemented: true,
    permission: 'contract.read',
  },
  {
    key: 'rules',
    path: '/rule-sets',
    labelKey: 'nav.rules',
    implemented: true,
    permission: 'rule.read',
  },
  {
    key: 'pricing',
    path: '/pricing',
    labelKey: 'nav.pricing',
    implemented: true,
    permission: 'pricing.quote',
  },
  {
    key: 'providers',
    path: '/providers',
    labelKey: 'nav.providers',
    implemented: true,
    permission: 'provider.read',
  },
  {
    key: 'organizations',
    path: '/organizations',
    labelKey: 'nav.organizations',
    implemented: true,
    permission: 'organization.read',
  },
  {
    key: 'requests',
    path: '/requests',
    labelKey: 'nav.requests',
    implemented: true,
    permission: 'service_request.read',
  },
  { key: 'health', path: '/health-services', labelKey: 'nav.health', implemented: true },
  {
    key: 'lodging',
    path: '/lodging/bookings',
    labelKey: 'nav.lodging',
    implemented: true,
    permission: 'accommodation.property.read',
  },
  {
    key: 'claims',
    path: '/claims',
    labelKey: 'nav.claims',
    implemented: true,
    permission: 'claim.read',
  },
  {
    key: 'medicalReports',
    path: '/medical-reports',
    labelKey: 'nav.medicalReports',
    implemented: true,
    permission: 'health.medical_report.review',
  },
  {
    key: 'billing',
    path: '/billing/batches',
    labelKey: 'nav.billing',
    implemented: true,
    match: '/billing',
  },
  {
    key: 'worklist',
    path: '/worklist',
    labelKey: 'nav.worklist',
    implemented: true,
    permission: 'worklist.read',
  },
  {
    key: 'notifications',
    path: '/notifications',
    labelKey: 'nav.notifications',
    implemented: true,
    permission: 'notification.read',
  },
  {
    key: 'reports',
    path: '/reports',
    labelKey: 'nav.reports',
    implemented: true,
    permission: 'report.read',
  },
  { key: 'integrations', path: '/integrations', labelKey: 'nav.integrations', implemented: false },
  { key: 'admin', path: '/admin', labelKey: 'nav.admin', implemented: true },
  {
    key: 'security',
    path: '/security',
    labelKey: 'nav.security',
    implemented: true,
    permission: 'audit.read',
  },
];

/** Destinations on the health landing, based on grants in the active tenant. */
export function healthAccess(permissions: readonly string[]) {
  const has = (permission: string) => permissions.includes(permission);
  const claims = has('claim.read');
  return {
    reports: has('health.medical_report.review'),
    medicalClaims: claims && has('claim.medical.review'),
    financialClaims: claims && has('claim.financial.review'),
    claimRecords: claims,
    requests: has('service_request.read'),
    personRecords: has('member.read') && (has('health.case.read') || claims),
  };
}

export function canDiscoverWallets(permissions: readonly string[]): boolean {
  return permissions.includes('member.read') && permissions.includes('entitlement.read');
}

/** First billing list the active role can actually read. */
export function billingLanding(permissions: readonly string[]): string | null {
  if (permissions.includes('invoice.read')) return '/billing/batches';
  if (permissions.includes('settlement.read')) return '/billing/settlements';
  if (permissions.includes('claim.financial.review')) return '/billing/reimbursements';
  return null;
}

/** The sidebar and home tiles must agree on which sections are reachable. */
export function visibleNavEntries(
  permissions: readonly string[],
  canReadTenantUsers = false,
): NavEntry[] {
  return NAV_ENTRIES.flatMap((entry) => {
    if (entry.key === 'health')
      return Object.values(healthAccess(permissions)).some(Boolean) ? [entry] : [];
    if (entry.key === 'wallets') return canDiscoverWallets(permissions) ? [entry] : [];
    if (entry.key === 'admin') return canReadTenantUsers ? [entry] : [];
    if (entry.key === 'billing') {
      const path = billingLanding(permissions);
      return path ? [{ ...entry, path }] : [];
    }
    return !entry.permission || permissions.includes(entry.permission) ? [entry] : [];
  });
}

/** Paths that currently render the "coming soon" page. */
export const SOON_PATHS = NAV_ENTRIES.filter((e) => !e.implemented).map((e) => e.path);
