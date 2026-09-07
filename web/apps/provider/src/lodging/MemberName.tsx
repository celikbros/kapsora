import { usePermission } from '@kapsora/auth';

import { usePersonName } from '../health/queries';

/**
 * The guest by name. A booking carries the person's id; the desk reads the name when it
 * holds member.read and shows a dash when it does not — never an identifier.
 */
export function MemberName({ personId }: { personId: string }) {
  const canRead = usePermission('member.read');
  const name = usePersonName(canRead ? personId : null);
  if (!canRead) return <>—</>;
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}
