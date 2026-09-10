import type {
  BatchDecision,
  BatchStatus,
  InvoiceStatus,
  ReimbursementStatus,
  SettlementStatus,
} from './billing';

/**
 * One tone per status, imported by every app: the member and the operator see the same
 * colour for the same record. The union is the UI package's BillingTone, spelled out here
 * so the wire package does not depend on the component library.
 */
export type BillingTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger';

export function invoiceTone(status: InvoiceStatus): BillingTone {
  switch (status) {
    case 'APPROVED':
    case 'SETTLED':
      return 'success';
    case 'PARTIALLY_APPROVED':
    case 'RETURNED':
      return 'warning';
    case 'REJECTED':
    case 'CANCELLED':
      return 'danger';
    case 'SUBMITTED':
    case 'IN_BATCH':
      return 'info';
    default:
      return 'neutral';
  }
}

export function batchTone(status: BatchStatus): BillingTone {
  switch (status) {
    case 'DECIDED':
    case 'CLOSED':
      return 'success';
    case 'SUBMITTED':
    case 'UNDER_REVIEW':
    case 'SETTLING':
      return 'info';
    case 'CANCELLED':
      return 'danger';
    default:
      return 'neutral';
  }
}

export function decisionTone(decision: BatchDecision | null | undefined): BillingTone {
  switch (decision) {
    case 'APPROVE':
      return 'success';
    case 'CUT':
    case 'RETURN':
      return 'warning';
    case 'REJECT':
      return 'danger';
    default:
      return 'neutral';
  }
}

export function settlementTone(status: SettlementStatus): BillingTone {
  switch (status) {
    case 'APPROVED':
    case 'PAID':
    case 'RECONCILED':
      return 'success';
    case 'PENDING_APPROVAL':
    case 'PARTIALLY_PAID':
    case 'POSTED':
      return 'info';
    case 'CANCELLED':
      return 'danger';
    default:
      return 'neutral';
  }
}

export function reimbursementTone(status: ReimbursementStatus): BillingTone {
  switch (status) {
    case 'APPROVED':
    case 'PARTIALLY_APPROVED':
    case 'PAID':
      return 'success';
    case 'SUBMITTED':
    case 'UNDER_REVIEW':
    case 'PAYMENT_ORDERED':
      return 'info';
    case 'REJECTED':
    case 'CANCELLED':
      return 'danger';
    default:
      return 'neutral';
  }
}
