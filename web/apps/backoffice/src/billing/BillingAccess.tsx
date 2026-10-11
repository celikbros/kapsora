import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { EmptyState } from '@kapsora/ui';
import { Fragment, type ReactNode } from 'react';

/** Stop unauthorized list/detail reads before their page hooks mount. */
export function BillingAccess({
  permission,
  children,
}: {
  permission: string;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  const tenant = useSession((s) => s.activeTenant);
  const actorId = useSession((s) => s.session?.actorId ?? '');
  const allowed = tenant?.permissions.includes(permission) ?? false;
  const contextKey = `${tenant?.tenant.id ?? ''}:${actorId}:${tenant?.permissions.join('|') ?? ''}`;
  return allowed ? (
    <Fragment key={contextKey}>{children}</Fragment>
  ) : (
    <EmptyState title={t('problems.PERMISSION_DENIED')} />
  );
}
