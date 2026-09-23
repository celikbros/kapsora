/** Normalize decimal separators without converting exact API decimals to floats. */
export const claimDecimal = (value: string) => value.trim().replace(',', '.');

export function validClaimDecimal(value: string, positive = false): boolean {
  const normalized = claimDecimal(value);
  return /^\d{1,14}(\.\d{1,6})?$/.test(normalized) && (!positive || /[1-9]/.test(normalized));
}
