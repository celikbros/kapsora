import type { ServiceRequest, ServiceRequestStatus } from '@kapsora/api-client';
import type { BadgeTone } from '@kapsora/ui';

/** Tone follows what the status asks of the reader, not its severity as a word. */
export function requestTone(status: ServiceRequestStatus): BadgeTone {
  switch (status) {
    case 'APPROVED':
      return 'success';
    case 'PARTIALLY_APPROVED':
      return 'success';
    case 'PENDING_REVIEW':
    case 'PENDING_DOCUMENT':
      return 'info';
    case 'ELIGIBILITY_FAILED':
    case 'REJECTED':
      return 'danger';
    case 'EXPIRED':
      return 'warning';
    default:
      return 'neutral';
  }
}

/** A draft that came back: the correction path, which is not the same page as a new draft. */
export function isReturned(request: ServiceRequest): boolean {
  return request.status === 'DRAFT' && Boolean(request.returnReasonCode);
}

/**
 * Which commands this status allows, in the server's own terms. A command not listed
 * here is absent from the screen — never disabled — because the server would refuse it
 * and a disabled button is a question the operator cannot answer.
 */
export function allowedCommands(status: ServiceRequestStatus) {
  const editable = status === 'DRAFT';
  const reviewable = status === 'PENDING_REVIEW';
  const returnable = status === 'PENDING_REVIEW' || status === 'PENDING_DOCUMENT';
  const cancellable =
    status === 'DRAFT' ||
    status === 'PENDING_REVIEW' ||
    status === 'PENDING_DOCUMENT' ||
    status === 'ELIGIBILITY_FAILED' ||
    status === 'APPROVED' ||
    status === 'PARTIALLY_APPROVED';
  return {
    edit: editable,
    submit: editable,
    return: returnable,
    reject: returnable,
    approve: reviewable,
    partiallyApprove: reviewable,
    cancel: cancellable,
  };
}
