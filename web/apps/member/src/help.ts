/**
 * Which page help each route shows, in the router's own spelling. The key names the page
 * under `help:pages.member`; `help.test.ts` fails when a page in this table has no help
 * written.
 */
export const HELP_ROUTES: Readonly<Record<string, string>> = {
  '/': 'home',
  '/search': 'search',
  '/bookings': 'bookings',
  '/bookings/$bookingId': 'booking',
  '/reimbursements': 'reimbursements',
  '/reimbursements/new': 'reimbursementNew',
  '/reimbursements/$reimbursementId': 'reimbursement',
};
