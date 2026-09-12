/**
 * Which page help each route shows, in the router's own spelling. The key names the page
 * under `help:pages.provider`; `help.test.ts` fails when a page in this table has no help
 * written.
 */
export const HELP_ROUTES: Readonly<Record<string, string>> = {
  '/': 'newRequest',
  '/requests': 'requests',
  '/requests/$requestId': 'request',
  '/eligibility': 'eligibility',
  '/cases': 'cases',
  '/cases/new': 'caseNew',
  '/cases/$caseId': 'case',
  '/reports/$reportId': 'report',
  '/stays/$stayId': 'stay',
  '/claims': 'claims',
  '/claims/new': 'claimNew',
  '/claims/$claimId': 'claim',
  '/lodging/inventory': 'inventory',
  '/lodging/desk': 'desk',
  '/billing': 'earnings',
  '/billing/invoices': 'invoices',
  '/billing/invoices/new': 'invoiceNew',
  '/billing/invoices/$invoiceId': 'invoice',
  '/billing/batches': 'batches',
  '/billing/batches/new': 'batchNew',
  '/billing/batches/$batchId': 'batch',
  '/billing/statement': 'statement',
};
