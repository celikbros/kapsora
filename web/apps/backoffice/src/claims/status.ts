import type { ClaimStatus, MedicalReportStatus } from '@kapsora/api-client';
import type { BadgeTone } from '@kapsora/ui';

/** Tone follows what the status asks of the reader, not its severity as a word. */
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

/** A quantity as a person reads it: "3", not "3.000000". String work only; nothing is parsed. */
export function qty(value: string | null | undefined): string {
  if (value == null || value === '') return '—';
  return value.includes('.') ? value.replace(/\.?0+$/, '') : value;
}

/** Reason codes are machine words: upper-case, no spaces, at most eighty characters. */
export const REASON_CODE = /^[A-Z][A-Z0-9_.:-]{1,79}$/;
