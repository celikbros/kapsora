import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { EmptyState } from '@kapsora/ui';
import { Fragment, type ReactNode } from 'react';

import { canOpen, type PortalArea } from './access';

/** Refuse a route before any of its page hooks mount; remount forms on context changes. */
export function RouteAccess({
  area,
  fromRequest = false,
  children,
}: {
  area: PortalArea;
  fromRequest?: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  const tenant = useSession((s) => s.activeTenant);
  const actorId = useSession((s) => s.session?.actorId ?? '');
  const permissions = tenant?.permissions ?? [];
  const contextKey = `${actorId}:${JSON.stringify(tenant ?? null)}`;
  return canOpen(area, permissions, fromRequest) ? (
    <Fragment key={contextKey}>{children}</Fragment>
  ) : (
    <div data-testid="provider-route-denied">
      <EmptyState title={t('problems.PERMISSION_DENIED')} />
    </div>
  );
}
