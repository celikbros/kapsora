import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { EmptyState, PageHeader } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { Fragment } from 'react';
import { directoryContextKey } from '../api';
import { AdminUsersPage } from './AdminUsersPage';

/** Management's landing admits each server capability independently. */
export function AdminLandingPage() {
  const { t } = useTranslation();
  const canReadUsers = useSession((s) => s.activeTenant?.canReadTenantUsers === true);
  const canReadSettings = useSession((s) => s.activeTenant?.canReadTenantSettings === true);
  const context = useSession(directoryContextKey);
  if (!canReadUsers && !canReadSettings)
    return <EmptyState title={t('problems.PERMISSION_DENIED')} />;
  return (
    <Fragment key={context}>
      {!canReadUsers && (
        <PageHeader title={t('nav.admin')} description={t('tenantSettings.managementIntro')} />
      )}
      {!canReadUsers && canReadSettings && (
        <Link
          to="/admin/tenant-settings"
          className="text-primary mt-4 inline-block text-sm font-medium underline-offset-4 hover:underline focus-visible:underline"
        >
          {t('tenantSettings.title')}
        </Link>
      )}
      {canReadUsers && <AdminUsersPage />}
    </Fragment>
  );
}
