/**
 * Quantities and money cross the wire as decimal strings (`numeric(20,6)`), so nothing
 * here parses them: the text is only trimmed and read for its sign. An exact
 * "-1.500000" therefore reaches the screen exactly, and no resulting balance is ever
 * computed in the browser.
 */

/** True when the decimal takes entitlement away. */
export function isNegativeDecimal(value: string): boolean {
  return value.trim().startsWith('-');
}

/** Drops the trailing zeros of the scale; the digits that carry meaning are untouched. */
export function trimDecimal(value: string): string {
  return value
    .trim()
    .replace(/(\.\d*?)0+$/, '$1')
    .replace(/\.$/, '');
}

/** A balance as text, with the ISO code when the account holds money. */
export function quantityText(
  value: string,
  unitType?: string,
  currencyCode?: string | null,
): string {
  const trimmed = trimDecimal(value);
  return unitType === 'MONEY' && currencyCode ? `${trimmed} ${currencyCode}` : trimmed;
}

/**
 * The delta with its sign spelled out, e.g. "+12" or "-1.5 TRY". The plus is prepended
 * as text; a minus already sits in the value the server sent.
 */
export function signedQuantityText(
  value: string,
  unitType?: string,
  currencyCode?: string | null,
): string {
  const trimmed = trimDecimal(value);
  const signed = trimmed.startsWith('-') || trimmed.startsWith('+') ? trimmed : `+${trimmed}`;
  return unitType === 'MONEY' && currencyCode ? `${signed} ${currencyCode}` : signed;
}
