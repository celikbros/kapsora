import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { EmptyState, PageHeader } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import type { ReactNode } from 'react';

function ReportRow({ children, description }: { children: ReactNode; description: string }) {
  return (
    <li className="px-4 py-3 sm:px-5">
      <div className="text-sm font-medium">{children}</div>
      <p className="text-fg-muted mt-1 text-sm">{description}</p>
    </li>
  );
}

/** Entry to existing read-only report lists for the active tenant. */
export function ReportsPage() {
  const { t } = useTranslation();
  const permissions = useSession((s) => s.activeTenant?.permissions);
  const canRead = permissions?.includes('report.read') ?? false;

  return (
    <section data-testid="reports-services">
      <PageHeader title={t('reportsServices.title')} description={t('reportsServices.intro')} />
      {canRead ? (
        <ul className="divide-line divide-y rounded-lg border border-line bg-surface">
          <ReportRow description={t('reportsServices.reconciliationDescription')}>
            <Link
              to="/billing/reconciliation"
              data-testid="reports-reconciliation"
              className="text-primary hover:underline"
            >
              {t('reportsServices.reconciliation')}
            </Link>
          </ReportRow>
          <ReportRow description={t('reportsServices.exportsDescription')}>
            <Link
              to="/billing/exports"
              data-testid="reports-exports"
              className="text-primary hover:underline"
            >
              {t('reportsServices.exports')}
            </Link>
          </ReportRow>
        </ul>
      ) : (
        <EmptyState title={t('problems.PERMISSION_DENIED')} />
      )}
    </section>
  );
}
