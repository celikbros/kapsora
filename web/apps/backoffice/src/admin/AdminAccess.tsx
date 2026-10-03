import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { EmptyState } from '@kapsora/ui';
import { Fragment, type ReactNode } from 'react';

/** The server supplies grant/scope correlation; flattened permissions are insufficient. */
export function AdminAccess({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const allowed = useSession((s) => s.activeTenant?.canReadTenantUsers === true);
  const contextKey = useSession((s) =>
    [
      s.session?.actorId ?? '',
      s.session?.expiresAt ?? '',
      s.activeTenant?.tenant.id ?? '',
      s.activeTenant?.canReadTenantUsers === true ? 'allowed' : 'denied',
      s.activeTenant?.permissions.join('|') ?? '',
      JSON.stringify(s.activeTenant?.scopes ?? []),
    ].join(':'),
  );
  if (!allowed) return <EmptyState title={t('problems.PERMISSION_DENIED')} />;
  return <Fragment key={contextKey}>{children}</Fragment>;
}
