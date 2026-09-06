import type { MedicalReportStatus } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  EmptyState,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { useState } from 'react';

import { useOrganizationName, usePersonName } from '../claims/names';
import { useReports } from '../claims/queries';
import { reportTone } from '../claims/status';
import { problemOf } from '../problems';

const STATUSES: MedicalReportStatus[] = ['SUBMITTED', 'UNDER_REVIEW', 'APPROVED', 'REJECTED'];

function PersonCell({ personId }: { personId: string }) {
  const name = usePersonName(personId);
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}
function ProviderCell({ organizationId }: { organizationId: string | null | undefined }) {
  const name = useOrganizationName(organizationId);
  if (!organizationId) return <>—</>;
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}

/** The reports waiting on a medical reviewer, newest submission first. */
export function ReportReviewListPage() {
  const { t } = useTranslation();
  const [status, setStatus] = useState<MedicalReportStatus>('SUBMITTED');
  const canReadOrganizations = usePermission('organization.read');
  const query = useReports({ status, limit: 50 });
  const rows = query.data?.items ?? [];

  return (
    <>
      <PageHeader title={t('review.report.listTitle')} description={t('review.report.listIntro')} />
      <div className="mb-3 flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('health.reports.columns.status')}</span>
          <Select
            name="status"
            value={status}
            onChange={(e) => setStatus(e.target.value as MedicalReportStatus)}
            options={STATUSES.map((s) => ({ value: s, label: t(`health.reports.status.${s}`) }))}
          />
        </label>
      </div>
      {query.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : query.error ? (
        <ProblemAlert problem={problemOf(query.error)} />
      ) : rows.length === 0 ? (
        <EmptyState title={t('review.report.empty')} />
      ) : (
        <Table data-testid="report-table">
          <THead>
            <TR>
              <TH>{t('health.reports.columns.reference')}</TH>
              <TH>{t('health.reports.columns.status')}</TH>
              <TH>{t('claims.columns.member')}</TH>
              {canReadOrganizations ? <TH>{t('review.report.provider')}</TH> : null}
              <TH>{t('health.reports.columns.valid')}</TH>
            </TR>
          </THead>
          <TBody>
            {rows.map((r) => (
              <TR key={r.id} data-status={r.status}>
                <TD>
                  <Link
                    to="/medical-reports/$reportId"
                    params={{ reportId: r.id }}
                    className="font-mono underline-offset-2 hover:underline"
                  >
                    {r.reference} · v{r.versionNo}
                  </Link>
                </TD>
                <TD>
                  <Badge tone={reportTone(r.status)}>
                    {t(`health.reports.status.${r.status}`)}
                  </Badge>
                </TD>
                <TD>
                  <PersonCell personId={r.personId} />
                </TD>
                {canReadOrganizations ? (
                  <TD>
                    <ProviderCell organizationId={r.issuingProviderOrganizationId} />
                  </TD>
                ) : null}
                <TD>
                  {formatDate(r.validFrom)} – {formatDate(r.validTo)}
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
    </>
  );
}
