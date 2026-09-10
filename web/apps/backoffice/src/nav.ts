/** Backoffice main navigation (v1.2 section 15.6). Unimplemented entries open "Yakında". */
export interface NavEntry {
  key: string;
  path: string;
  /** i18n key under nav.* */
  labelKey: string;
  implemented: boolean;
  /** Permission that must be present for the entry to show; undefined = always. */
  permission?: string;
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
  { key: 'wallets', path: '/wallets', labelKey: 'nav.wallets', implemented: false },
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
  { key: 'health', path: '/health-services', labelKey: 'nav.health', implemented: false },
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
    permission: 'settlement.read',
  },
  {
    key: 'reconciliation',
    path: '/reconciliation',
    labelKey: 'nav.reconciliation',
    implemented: false,
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
  { key: 'reports', path: '/reports', labelKey: 'nav.reports', implemented: false },
  { key: 'integrations', path: '/integrations', labelKey: 'nav.integrations', implemented: false },
  { key: 'admin', path: '/admin', labelKey: 'nav.admin', implemented: false },
  { key: 'security', path: '/security', labelKey: 'nav.security', implemented: false },
];

/** Paths that currently render the "coming soon" page. */
export const SOON_PATHS = NAV_ENTRIES.filter((e) => !e.implemented).map((e) => e.path);
