export { reimbursementTone } from '@kapsora/api-client';

/** The account as typed, with every space and dash gone; the server validates the rest. */
export function normalizeAccount(raw: string): string {
  return raw.replace(/[\s-]/g, '').toUpperCase();
}
