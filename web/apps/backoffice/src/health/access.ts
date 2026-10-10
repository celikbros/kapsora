import { ApiError, type AccessContext, type AccessPurpose } from '@kapsora/api-client';
import { useSession } from '@kapsora/auth';
import { useCallback, useState } from 'react';

/**
 * What the operator has said about one sensitive record in this session: the purpose they
 * stated, or that they declined to open its clinical half. It lives in memory for the tab's
 * lifetime and nowhere else — a stated purpose is not a credential to store — and it is
 * asked once per record, because asking again on every refetch would turn a real question
 * into a click-through.
 */
export interface AccessState {
  purpose?: AccessPurpose;
  reason?: string;
  declined?: boolean;
}

const remembered = new Map<string, AccessState>();

/** The purposes the reference table seeds; the dialog offers exactly these. */
export const ACCESS_PURPOSES: AccessPurpose[] = [
  'MEDICAL_REVIEW',
  'CLAIM_REVIEW',
  'PRE_AUTHORIZATION',
  'TREATMENT',
  'MEMBER_REQUEST',
  'AUDIT',
];

export function useAccessState(resourceId: string) {
  const sessionScope = useSession((s) =>
    s.session && s.activeTenant
      ? `${s.session.actorId}:${s.session.expiresAt}:${s.activeTenant.tenant.id}`
      : '',
  );
  const key = `${sessionScope}:${resourceId}`;
  const [selection, setSelection] = useState(() => ({ key, state: remembered.get(key) ?? {} }));
  // Resolve the new scope during render, before a query can send the previous purpose.
  // An effect would be too late: the first read could already disclose clinical data.
  const state = selection.key === key ? selection.state : (remembered.get(key) ?? {});
  const grant = useCallback(
    (purpose: AccessPurpose, reason: string) => {
      const next: AccessState = reason.trim() ? { purpose, reason: reason.trim() } : { purpose };
      remembered.set(key, next);
      setSelection({ key, state: next });
    },
    [key],
  );
  const decline = useCallback(() => {
    const next: AccessState = { declined: true };
    remembered.set(key, next);
    setSelection({ key, state: next });
  }, [key]);
  return { state, grant, decline };
}

/** The headers a read sends for the state the operator is in. */
export function accessFor(state: AccessState): AccessContext | undefined {
  if (state.purpose) {
    return state.reason
      ? { purpose: state.purpose, reason: state.reason }
      : { purpose: state.purpose };
  }
  if (state.declined) return { projection: 'FINANCIAL' };
  return undefined;
}

/** Whether an error is the server asking for a purpose before it opens the clinical half. */
export function needsPurpose(err: unknown): boolean {
  return err instanceof ApiError && err.problem.status === 428;
}

/** For tests: forget every stated purpose. */
export function resetAccessMemory() {
  remembered.clear();
}
