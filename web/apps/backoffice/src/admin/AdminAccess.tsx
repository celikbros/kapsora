import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { EmptyState } from '@kapsora/ui';
import { Fragment, type ReactNode } from 'react';
import { directoryContextKey } from '../api';

/** The server supplies grant/scope correlation; flattened permissions are insufficient. */
export function AdminAccess({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const allowed = useSession((s) => s.activeTenant?.canReadTenantUsers === true);
  const contextKey = useSession(directoryContextKey);
  if (!allowed) return <EmptyState title={t('problems.PERMISSION_DENIED')} />;
  return <Fragment key={contextKey}>{children}</Fragment>;
}
