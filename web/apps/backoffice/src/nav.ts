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
  { key: 'catalog', path: '/catalog', labelKey: 'nav.catalog', implemented: false },
  {
    key: 'providers',
    path: '/organizations',
    labelKey: 'nav.providers',
    implemented: true,
    permission: 'organization.read',
  },
  { key: 'requests', path: '/requests', labelKey: 'nav.requests', implemented: false },
  { key: 'health', path: '/health-services', labelKey: 'nav.health', implemented: false },
  { key: 'lodging', path: '/lodging', labelKey: 'nav.lodging', implemented: false },
  { key: 'claims', path: '/claims', labelKey: 'nav.claims', implemented: false },
  {
    key: 'reconciliation',
    path: '/reconciliation',
    labelKey: 'nav.reconciliation',
    implemented: false,
  },
  { key: 'worklist', path: '/worklist', labelKey: 'nav.worklist', implemented: false },
  { key: 'reports', path: '/reports', labelKey: 'nav.reports', implemented: false },
  { key: 'integrations', path: '/integrations', labelKey: 'nav.integrations', implemented: false },
  { key: 'admin', path: '/admin', labelKey: 'nav.admin', implemented: false },
  { key: 'security', path: '/security', labelKey: 'nav.security', implemented: false },
];

/** Paths that currently render the "coming soon" page. */
export const SOON_PATHS = NAV_ENTRIES.filter((e) => !e.implemented).map((e) => e.path);
