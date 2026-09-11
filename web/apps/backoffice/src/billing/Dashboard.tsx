import type { DashboardFilter } from '@kapsora/api-client';
import { formatDateTime, formatMoney, useTranslation } from '@kapsora/i18n';
import { Card, ProblemAlert, Spinner } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import type { ReactNode } from 'react';

import { problemOf } from '../problems';
import { useDashboard } from './queries';

/**
 * The list a figure's filter leads to, but only when that list reproduces the figure
 * exactly. The list endpoints take one status and no date window, so a filter naming two
 * statuses, an aging bucket or a due window has no list that counts the same rows — and a
 * link to a list that shows a different number is worse than no link.
 */
function exactTarget(filter: DashboardFilter): ReactNode | null {
  const windowed =
    filter.dueFrom || filter.dueTo || filter.dueBefore || filter.agingBucket ? true : false;
  if (filter.resource === 'workItems') return 'worklist';
  if (windowed || (filter.statuses ?? []).length !== 1) return null;
  return filter.resource === 'claims' ? 'claims' : null;
}

function FigureLabel({ filter, children }: { filter: DashboardFilter; children: ReactNode }) {
  const target = exactTarget(filter);
  const className = 'text-primary underline-offset-4 hover:underline';
  if (target === 'claims') {
    const status = filter.statuses![0]!;
    return (
      <Link to="/claims" search={{ status: status as never }} className={className}>
        {children}
      </Link>
    );
  }
  if (target === 'worklist') {
    return (
      <Link to="/worklist" search={{ view: 'overdue' }} className={className}>
        {children}
      </Link>
    );
  }
  return <span>{children}</span>;
}

/** One figure as a row: the label, the count, the amount — the figures block's grammar. */
function FigureRow({
  filter,
  label,
  count,
  amount,
  note,
  testId,
}: {
  filter: DashboardFilter;
  label: string;
  count: number;
  amount?: string | null;
  note?: string | null;
  testId?: string;
}) {
  return (
    <li
      className="col-span-3 grid grid-cols-subgrid items-baseline text-sm"
      {...(testId ? { 'data-testid': testId } : {})}
    >
      <span className="min-w-0">
        <FigureLabel filter={filter}>{label}</FigureLabel>
        {note ? (
          <span className="text-fg-muted block text-xs whitespace-nowrap">{note}</span>
        ) : null}
      </span>
      <span className="text-right font-mono whitespace-nowrap tabular-nums">{count}</span>
      <span className="text-right font-mono whitespace-nowrap tabular-nums">
        {amount === undefined || amount === null ? '' : formatMoney(amount, 'TRY')}
      </span>
    </li>
  );
}

/**
 * The operations dashboard: what is waiting and for how long, each figure as a row of the
 * figures block, linked to its list only where that list reproduces the number.
 */
export function Dashboard() {
  const { t } = useTranslation();
  const dashboard = useDashboard();
  if (dashboard.isPending) {
    return (
      <p className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
        <Spinner /> {t('billing.report.dashboardLoading')}
      </p>
    );
  }
  if (dashboard.isError) return <ProblemAlert problem={problemOf(dashboard.error)} />;
  const d = dashboard.data;

  return (
    <section aria-labelledby="dashboard-heading" className="grid gap-4" data-testid="dashboard">
      <div className="flex flex-wrap items-baseline justify-between gap-3">
        <h2 id="dashboard-heading" className="text-base font-semibold">
          {t('billing.report.dashboardTitle')}
        </h2>
        <span className="text-fg-muted text-xs">
          {t('billing.report.asOf')} {formatDateTime(d.asOf)}
        </span>
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        <Card data-testid="dashboard-claims">
          <h3 className="text-sm font-semibold">{t('billing.report.claimsByStatus')}</h3>
          <ul className="mt-2 grid grid-cols-[minmax(0,1fr)_auto_auto] gap-x-4 gap-y-1">
            {d.claimsByStatus.map((row) => (
              <FigureRow
                key={row.status}
                filter={row.filter}
                label={t(`claims.status.${row.status}`)}
                count={row.claimCount}
                amount={row.approvedTotal}
                testId={`claims-${row.status}`}
              />
            ))}
          </ul>
        </Card>
        <Card data-testid="dashboard-aging">
          <h3 className="text-sm font-semibold">{t('billing.report.claimAging')}</h3>
          <ul className="mt-2 grid grid-cols-[minmax(0,1fr)_auto_auto] gap-x-4 gap-y-1">
            {d.claimAging.map((row) => (
              <FigureRow
                key={row.bucket}
                filter={row.filter}
                label={t(`billing.report.aging.${row.bucket}`)}
                count={row.claimCount}
                amount={row.approvedTotal}
              />
            ))}
          </ul>
        </Card>
        <Card className="lg:col-span-2" data-testid="dashboard-waiting">
          <h3 className="text-sm font-semibold">{t('billing.report.waiting')}</h3>
          <ul className="mt-2 grid max-w-2xl grid-cols-[minmax(0,1fr)_auto_auto] gap-x-4 gap-y-2">
            <FigureRow
              filter={d.batchesAwaitingReview.filter}
              label={t('billing.report.batchesAwaitingReview')}
              count={d.batchesAwaitingReview.batchCount}
              amount={d.batchesAwaitingReview.submittedTotal}
              note={
                d.batchesAwaitingReview.oldestSubmittedAt
                  ? `${t('billing.report.oldest')} ${formatDateTime(d.batchesAwaitingReview.oldestSubmittedAt)}`
                  : null
              }
              testId="dashboard-batches"
            />
            <FigureRow
              filter={d.settlements.dueSoonFilter}
              label={t('billing.report.settlementsDueSoon')}
              count={d.settlements.dueSoonCount}
              amount={d.settlements.dueSoonTotal}
              testId="dashboard-due-soon"
            />
            <FigureRow
              filter={d.settlements.overdueFilter}
              label={t('billing.report.settlementsOverdue')}
              count={d.settlements.overdueCount}
              amount={d.settlements.overdueTotal}
              testId="dashboard-overdue"
            />
            <FigureRow
              filter={d.reimbursements.filter}
              label={t('billing.report.reimbursementsWaiting')}
              count={d.reimbursements.reimbursementCount}
              amount={d.reimbursements.requestedTotal}
              note={
                d.reimbursements.oldestSubmittedAt
                  ? `${t('billing.report.oldest')} ${formatDateTime(d.reimbursements.oldestSubmittedAt)}`
                  : null
              }
            />
            <FigureRow
              filter={d.workItemsPastSla.filter}
              label={t('billing.report.workItemsPastSla')}
              count={d.workItemsPastSla.itemCount}
              note={
                d.workItemsPastSla.oldestDueAt
                  ? `${t('billing.report.slaDue')} ${formatDateTime(d.workItemsPastSla.oldestDueAt)}`
                  : null
              }
              testId="dashboard-workitems"
            />
          </ul>
        </Card>
      </div>
    </section>
  );
}
