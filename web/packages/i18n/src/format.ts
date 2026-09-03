/**
 * Formatting helpers. Dates are shown in the tenant's time zone, money with its ISO
 * currency code (v1.2: `numeric(20,6)` + `char(3)`), never a symbol that could be
 * confused across currencies.
 */

export interface FormatContext {
  locale?: string;
  timeZone?: string;
}

const DEFAULT_LOCALE = 'tr-TR';
const DEFAULT_TIME_ZONE = 'Europe/Istanbul';

function toDate(value: string | Date): Date | null {
  const d = value instanceof Date ? value : new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** Calendar date (a `date` or a timestamp) as dd.MM.yyyy in the tenant zone. */
export function formatDate(
  value: string | Date | null | undefined,
  ctx: FormatContext = {},
): string {
  if (!value) return '';
  const d =
    typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value)
      ? new Date(`${value}T00:00:00Z`)
      : toDate(value);
  if (!d) return '';
  const zone =
    typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value)
      ? 'UTC'
      : (ctx.timeZone ?? DEFAULT_TIME_ZONE);
  return new Intl.DateTimeFormat(ctx.locale ?? DEFAULT_LOCALE, {
    day: '2-digit',
    month: '2-digit',
    year: 'numeric',
    timeZone: zone,
  }).format(d);
}

/** Timestamp as dd.MM.yyyy HH:mm in the tenant zone. */
export function formatDateTime(
  value: string | Date | null | undefined,
  ctx: FormatContext = {},
): string {
  if (!value) return '';
  const d = toDate(value);
  if (!d) return '';
  return new Intl.DateTimeFormat(ctx.locale ?? DEFAULT_LOCALE, {
    day: '2-digit',
    month: '2-digit',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    timeZone: ctx.timeZone ?? DEFAULT_TIME_ZONE,
  }).format(d);
}

/** Money with the ISO code after the amount, e.g. "1.250,00 TRY", in every locale. */
export function formatMoney(
  amount: number | string | null | undefined,
  currencyCode: string,
  ctx: FormatContext = {},
): string {
  if (amount === null || amount === undefined || amount === '') return '';
  const n = typeof amount === 'string' ? Number(amount) : amount;
  if (!Number.isFinite(n)) return '';
  const code = currencyCode.toUpperCase();
  const number = new Intl.NumberFormat(ctx.locale ?? DEFAULT_LOCALE, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(n);
  return `${number} ${code}`;
}

/** Plain number with locale separators. */
export function formatNumber(
  value: number | null | undefined,
  ctx: FormatContext = {},
  digits = 0,
): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '';
  return new Intl.NumberFormat(ctx.locale ?? DEFAULT_LOCALE, {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(value);
}
