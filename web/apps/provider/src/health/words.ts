import type {
  ClaimStatus,
  HealthCaseStatus,
  InpatientStayStatus,
  MedicalReportStatus,
} from '@kapsora/api-client';
import type { BadgeTone } from '@kapsora/ui';

/** Tone follows what the status asks of the reader, not its severity as a word. */
export function caseTone(status: HealthCaseStatus): BadgeTone {
  return status === 'OPEN' ? 'info' : 'neutral';
}

export function reportTone(status: MedicalReportStatus): BadgeTone {
  switch (status) {
    case 'APPROVED':
      return 'success';
    case 'SUBMITTED':
    case 'UNDER_REVIEW':
      return 'info';
    case 'REJECTED':
      return 'danger';
    case 'EXPIRED':
      return 'warning';
    default:
      return 'neutral';
  }
}

export function stayTone(status: InpatientStayStatus): BadgeTone {
  switch (status) {
    case 'AUTHORIZED':
    case 'ADMITTED':
      return 'success';
    case 'REQUESTED':
      return 'info';
    case 'REJECTED':
      return 'danger';
    default:
      return 'neutral';
  }
}

export function claimTone(status: ClaimStatus): BadgeTone {
  switch (status) {
    case 'APPROVED':
    case 'PARTIALLY_APPROVED':
    case 'SETTLED':
      return 'success';
    case 'PENDING_MEDICAL':
    case 'PENDING_FINANCIAL':
    case 'SUBMITTED':
    case 'AUTO_ADJUDICATED':
    case 'INVOICED':
    case 'BATCHED':
      return 'info';
    case 'RETURNED':
      return 'warning';
    case 'REJECTED':
      return 'danger';
    default:
      return 'neutral';
  }
}

/** A quantity as a person reads it: "3", not "3.000000". String work only; nothing is parsed. */
export function qty(value: string | null | undefined): string {
  if (value == null || value === '') return '—';
  return value.includes('.') ? value.replace(/\.?0+$/, '') : value;
}

/** Reason codes are machine words: upper-case, no spaces, at most eighty characters. */
export const REASON_CODE = /^[A-Z][A-Z0-9_.:-]{1,79}$/;

/** Today as the ISO day the date inputs want. */
export function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/** A day some days from today, as the ISO day the date inputs want. */
export function daysFromToday(days: number): string {
  const d = new Date();
  d.setDate(d.getDate() + days);
  return d.toISOString().slice(0, 10);
}

/** Now as the local datetime the datetime-local input wants, to the minute. */
export function nowLocal(): string {
  const d = new Date();
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A datetime-local value as the instant the API wants; empty stays empty. */
export function toInstant(local: string): string {
  return local ? new Date(local).toISOString() : '';
}
