import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { EmptyState, PageHeader } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import type { ReactNode } from 'react';
import { healthAccess } from '../nav';

function WorkflowRow({ children, description }: { children: ReactNode; description: string }) {
  return (
    <li className="px-4 py-3 sm:px-5">
      <div className="text-sm font-medium">{children}</div>
      <p className="text-fg-muted mt-1 text-sm">{description}</p>
    </li>
  );
}

/** A permission-scoped entry to the existing health review and record screens. */
export function HealthServicesPage() {
  const { t } = useTranslation();
  const active = useSession((s) => s.activeTenant);
  const access = healthAccess(active?.permissions ?? []);
  const allowed = Object.values(access).some(Boolean);

  return (
    <section data-testid="health-services">
      <PageHeader title={t('healthServices.title')} description={t('healthServices.intro')} />
      {allowed ? (
        <ul className="divide-line divide-y rounded-lg border border-line bg-surface">
          {access.reports ? (
            <WorkflowRow description={t('healthServices.reportsDescription')}>
              <Link
                to="/medical-reports"
                data-testid="health-report-review"
                className="text-primary hover:underline"
              >
                {t('healthServices.reports')}
              </Link>
            </WorkflowRow>
          ) : null}
          {access.medicalClaims ? (
            <WorkflowRow description={t('healthServices.medicalClaimsDescription')}>
              <Link
                to="/claims"
                search={{ status: 'PENDING_MEDICAL' }}
                data-testid="health-medical-claims"
                className="text-primary hover:underline"
              >
                {t('healthServices.medicalClaims')}
              </Link>
            </WorkflowRow>
          ) : null}
          {access.financialClaims ? (
            <WorkflowRow description={t('healthServices.financialClaimsDescription')}>
              <Link
                to="/claims"
                search={{ status: 'PENDING_FINANCIAL' }}
                data-testid="health-financial-claims"
                className="text-primary hover:underline"
              >
                {t('healthServices.financialClaims')}
              </Link>
            </WorkflowRow>
          ) : null}
          {access.claimRecords ? (
            <WorkflowRow description={t('healthServices.claimRecordsDescription')}>
              <Link
                to="/claims"
                data-testid="health-claim-records"
                className="text-primary hover:underline"
              >
                {t('healthServices.claimRecords')}
              </Link>
            </WorkflowRow>
          ) : null}
          {access.requests ? (
            <WorkflowRow description={t('healthServices.requestsDescription')}>
              <Link
                to="/requests"
                data-testid="health-requests"
                className="text-primary hover:underline"
              >
                {t('healthServices.requests')}
              </Link>
            </WorkflowRow>
          ) : null}
          {access.personRecords ? (
            <WorkflowRow description={t('healthServices.personRecordsDescription')}>
              <Link
                to="/people"
                data-testid="health-person-records"
                className="text-primary hover:underline"
              >
                {t('healthServices.personRecords')}
              </Link>
            </WorkflowRow>
          ) : null}
        </ul>
      ) : (
        <EmptyState title={t('problems.PERMISSION_DENIED')} />
      )}
    </section>
  );
}
