import type {
  Booking,
  BookingStatus,
  CancellationQuote,
  LodgingPolicySnapshot,
} from '@kapsora/api-client';
import { formatDate, formatMoney, formatNumber } from '@kapsora/i18n';
import type { BadgeTone } from '@kapsora/ui';
import type { TFunction } from 'i18next';

/**
 * The words of the member's lodging screens. Everything here turns a server value into a
 * sentence; nothing here computes one. Money is a string the server produced and is shown
 * with its currency; a quantity is formatted for reading and never added to anything.
 */

export function bookingTone(status: BookingStatus): BadgeTone {
  switch (status) {
    case 'CONFIRMED':
    case 'CHECKED_IN':
    case 'COMPLETED':
      return 'success';
    case 'HOLD':
    case 'PENDING_APPROVAL':
      return 'warning';
    case 'CANCELLED':
    case 'NO_SHOW':
    case 'EXPIRED':
      return 'danger';
    default:
      return 'neutral';
  }
}

/** The statuses a member still has something to do with or wait for. */
export const LIVE_STATUSES: readonly BookingStatus[] = [
  'HOLD',
  'PENDING_APPROVAL',
  'CONFIRMED',
  'CHECKED_IN',
];

export function isLive(booking: Booking): boolean {
  return LIVE_STATUSES.includes(booking.status);
}

/** "12 Eyl – 15 Eyl 2026": the stay as two dates, the morning of departure included. */
export function stayDates(checkIn: string, checkOut: string): string {
  return `${formatDate(checkIn)} – ${formatDate(checkOut)}`;
}

export function money(amount: string | null | undefined, currencyCode: string): string {
  return formatMoney(amount ?? null, currencyCode);
}

/** A quantity the server sent (an exact decimal string or a number), formatted for reading. */
export function quantity(value: string | number | null | undefined, digits = 0): string {
  if (value === null || value === undefined || value === '') return '';
  const n = typeof value === 'string' ? Number(value) : value;
  if (!Number.isFinite(n)) return String(value);
  return formatNumber(n, {}, Number.isInteger(n) ? 0 : digits);
}

/** The unit of an entitlement as the word that follows its figure. */
export function unitWord(t: TFunction, unitType: string): string {
  switch (unitType) {
    case 'NIGHT':
      return t('lodging.unit.NIGHT');
    case 'SESSION':
      return t('lodging.unit.SESSION');
    case 'HOUR':
      return t('lodging.unit.HOUR');
    case 'MONEY':
      return '';
    default:
      return t('lodging.unit.UNIT');
  }
}

/** The cancellation policy as the sentences a member can act on. */
export function policySentences(t: TFunction, policy: LodgingPolicySnapshot | null): string[] {
  if (!policy) return [t('lodging.policy.none')];
  const lines: string[] = [];
  if (policy.freeCancellationHoursBefore > 0) {
    lines.push(t('lodging.policy.free', { hours: policy.freeCancellationHoursBefore }));
  } else {
    lines.push(t('lodging.policy.freeNone'));
  }
  if (policy.penaltyKind === 'NIGHTS' && policy.penaltyNights !== null) {
    lines.push(t('lodging.policy.penaltyNights', { count: policy.penaltyNights ?? 0 }));
  } else if (policy.penaltyKind === 'PERCENT' && policy.penaltyPercent) {
    lines.push(t('lodging.policy.penaltyPercent', { percent: quantity(policy.penaltyPercent, 2) }));
  }
  lines.push(t('lodging.policy.noShow', { percent: quantity(policy.noShowPercent, 2) }));
  return lines;
}

/** What cancelling now costs, in the member's words. */
export function cancellationSentences(t: TFunction, quote: CancellationQuote): string[] {
  const lines: string[] = [];
  if (quote.free) {
    lines.push(
      quote.freeUntil
        ? t('lodging.booking.cancelFreeUntil', { until: formatDate(quote.freeUntil) })
        : t('lodging.booking.cancelFree'),
    );
  } else {
    lines.push(`${t('lodging.booking.cancelFee')}: ${money(quote.memberFee, quote.currencyCode)}`);
    if (quote.penaltyNights > 0) {
      lines.push(t('lodging.booking.cancelPenaltyNights', { count: quote.penaltyNights }));
    }
  }
  if (quote.releasedNights > 0) {
    lines.push(t('lodging.booking.cancelReleased', { count: quote.releasedNights }));
  }
  return lines;
}
