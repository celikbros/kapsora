import type { BookingStatus, NoShowStatus, WaitlistStatus } from '@kapsora/api-client';
import type { BadgeTone } from '@kapsora/ui';

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

export function noShowTone(status: NoShowStatus): BadgeTone {
  switch (status) {
    case 'CONFIRMED':
      return 'danger';
    case 'REJECTED':
      return 'success';
    case 'DISPUTED':
      return 'warning';
    default:
      return 'info';
  }
}

export function waitlistTone(status: WaitlistStatus): BadgeTone {
  switch (status) {
    case 'OFFERED':
    case 'ACCEPTED':
      return 'success';
    case 'EXPIRED':
    case 'CANCELLED':
      return 'neutral';
    default:
      return 'info';
  }
}

export const BOOKING_STATUSES: readonly BookingStatus[] = [
  'HOLD',
  'PENDING_APPROVAL',
  'CONFIRMED',
  'CHECKED_IN',
  'COMPLETED',
  'CANCELLED',
  'NO_SHOW',
  'EXPIRED',
];
