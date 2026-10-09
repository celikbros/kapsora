import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { EmptyState } from '@kapsora/ui';
import { Fragment, type ReactNode } from 'react';
import { directoryContextKey } from '../api';

export function AdminRoleAccess({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const allowed = useSession((s) => s.activeTenant?.canManageTenantRoles === true);
  const context = useSession(directoryContextKey);
  if (!allowed) return <EmptyState title={t('problems.PERMISSION_DENIED')} />;
  return <Fragment key={context}>{children}</Fragment>;
}
