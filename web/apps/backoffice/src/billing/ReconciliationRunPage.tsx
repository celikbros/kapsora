import type { SettlementStatus } from '@kapsora/api-client';
import { formatDate, formatDateTime, formatMoney, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Card,
  PageHeader,
  ProblemAlert,
  Spinner,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  useMinWidth,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';

import { problemOf } from '../problems';
import { BillingNav } from './BillingNav';
import { useReconciliationRun } from './queries';
import { runTone } from './reportStatus';
import { settlementTone } from './status';

/**
 * One run: the day's figures as the server kept them, and each difference named by the
 * settlement's reference with what was expected, what happened and by how much.
 */
export function ReconciliationRunPage() {
  const { runId } = useParams({ from: '/app/billing/reconciliation/$runId' });
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const run = useReconciliationRun(runId);

  if (run.isPending) {
    return (
      <p className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
        <Spinner /> {t('billing.report.runLoading')}
      </p>
    );
  }
  if (run.isError) return <ProblemAlert problem={problemOf(run.error)} />;
  const r = run.data;
  const title = `${formatDate(r.periodFrom)} · ${t('billing.report.runNo')} ${r.runNo}`;

  return (
    <div className="grid gap-4">
      <Breadcrumb
        items={[
          {
            label: t('billing.report.reconciliationTitle'),
            render: (label) => <Link to="/billing/reconciliation">{label}</Link>,
          },
          { label: title },
        ]}
      />
      <PageHeader
        title={title}
        description={`${
          r.scope === 'PROVIDER' ? (r.providerName ?? '') : t('billing.report.tenantWide')
        } · ${r.currencyCode} · ${t('billing.report.ranAt')} ${formatDateTime(r.ranAt)}`}
        actions={
          <Badge tone={runTone(r.status)} data-testid="run-status">
            {t(`billing.report.runStatus.${r.status}`)}
          </Badge>
        }
      />
      <BillingNav />

      <Card>
        <dl
          className="grid max-w-sm grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm"
          data-testid="run-figures"
        >
          <dt className="text-fg-muted">{t('billing.report.totals.invoiced')}</dt>
          <dd className="text-right font-mono tabular-nums">
            {formatMoney(r.invoicedTotal, r.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.report.totals.approved')}</dt>
          <dd className="text-right font-mono tabular-nums">
            {formatMoney(r.approvedTotal, r.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.report.totals.settled')}</dt>
          <dd className="text-right font-mono tabular-nums" data-testid="run-settled">
            {formatMoney(r.settledTotal, r.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.report.totals.paid')}</dt>
          <dd className="text-right font-mono tabular-nums" data-testid="run-paid">
            {formatMoney(r.paidTotal, r.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.report.erpTotal')}</dt>
          <dd className="text-right font-mono tabular-nums">
            {r.erpTotal ? formatMoney(r.erpTotal, r.currencyCode) : '—'}
          </dd>
          <dt className="font-medium">{t('billing.report.totals.open')}</dt>
          <dd
            className="text-right font-mono text-base font-semibold tabular-nums"
            data-testid="run-open"
          >
            {formatMoney(r.openTotal, r.currencyCode)}
          </dd>
        </dl>
        <p className="text-fg-muted mt-2 text-xs">
          {t('billing.totals.arithmetic')} {r.erpTotal ? '' : t('billing.report.erpPending')}
        </p>
      </Card>

      <Card className="min-w-0">
        <div className="flex flex-wrap items-baseline justify-between gap-3">
          <h2 className="text-base font-semibold">{t('billing.report.differences')}</h2>
          <span className="text-fg-muted text-sm">
            {t('billing.report.differenceCount', { count: r.differenceCount })} ·{' '}
            <span className="text-fg font-mono tabular-nums">
              {formatMoney(r.difference, r.currencyCode)}
            </span>
          </span>
        </div>
        {r.differences.length === 0 ? (
          <p className="text-fg-muted mt-2 text-sm" data-testid="no-differences">
            {t('billing.report.noDifferences')}
          </p>
        ) : !wide ? (
          <ul className="mt-3 grid gap-2" data-testid="difference-table">
            {r.differences.map((d) => (
              <li
                key={d.reference}
                className="bg-surface-raised border-line rounded-lg border p-3"
                data-testid="difference-row"
              >
                <div className="flex items-baseline justify-between gap-3">
                  <span className="font-mono text-xs">{d.reference}</span>
                  <Badge tone={settlementTone(d.status as SettlementStatus)}>
                    {t(`billing.settlementStatus.${d.status}`)}
                  </Badge>
                </div>
                <p className="mt-1 text-sm">{t(`billing.report.kinds.${d.kind}`)}</p>
                <p className="text-fg-muted mt-1 flex justify-between text-sm">
                  <span>{t('billing.report.expected')}</span>
                  <span className="text-fg font-mono tabular-nums">
                    {formatMoney(d.expected, r.currencyCode)}
                  </span>
                </p>
                <p className="text-fg-muted flex justify-between text-sm">
                  <span>{t('billing.report.actual')}</span>
                  <span className="text-fg font-mono tabular-nums">
                    {formatMoney(d.actual, r.currencyCode)}
                  </span>
                </p>
                <p className="flex justify-between text-sm font-medium">
                  <span>{t('billing.report.difference')}</span>
                  <span className="font-mono tabular-nums">
                    {formatMoney(d.difference, r.currencyCode)}
                  </span>
                </p>
              </li>
            ))}
          </ul>
        ) : (
          <div className="relative mt-3 overflow-x-auto">
            <Table data-testid="difference-table">
              <THead>
                <TR>
                  <TH>{t('billing.report.settlement')}</TH>
                  <TH>{t('billing.office.status')}</TH>
                  <TH>{t('billing.report.due')}</TH>
                  <TH>{t('billing.report.kind')}</TH>
                  <TH className="text-right">{t('billing.report.expected')}</TH>
                  <TH className="text-right">{t('billing.report.actual')}</TH>
                  <TH className="text-right">{t('billing.report.difference')}</TH>
                </TR>
              </THead>
              <TBody>
                {r.differences.map((d) => (
                  <TR key={d.reference} data-testid="difference-row">
                    <TD className="font-mono text-xs">{d.reference}</TD>
                    <TD>
                      <Badge tone={settlementTone(d.status as SettlementStatus)}>
                        {t(`billing.settlementStatus.${d.status}`)}
                      </Badge>
                    </TD>
                    <TD className="whitespace-nowrap">{formatDate(d.dueDate)}</TD>
                    <TD>{t(`billing.report.kinds.${d.kind}`)}</TD>
                    <TD className="text-right font-mono tabular-nums">
                      {formatMoney(d.expected, r.currencyCode)}
                    </TD>
                    <TD className="text-right font-mono tabular-nums">
                      {formatMoney(d.actual, r.currencyCode)}
                    </TD>
                    <TD className="text-right font-mono font-medium tabular-nums">
                      {formatMoney(d.difference, r.currencyCode)}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </div>
        )}
      </Card>
    </div>
  );
}
