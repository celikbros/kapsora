import { formatDate, formatDateTime, formatMoney, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  FormField,
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
  useMinWidth,
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { useState } from 'react';

import { problemOf } from '../problems';
import { BillingNav } from './BillingNav';
import { useReconciliationRuns } from './queries';
import { runTone } from './reportStatus';

/** Every daily run, newest first: balanced or not, and by how much. */
export function ReconciliationListPage() {
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const [scope, setScope] = useState('');
  const [status, setStatus] = useState('');
  const runs = useReconciliationRuns({
    ...(scope ? { scope: scope as 'TENANT' | 'PROVIDER' } : {}),
    ...(status ? { status } : {}),
  });
  const rows = runs.data?.items ?? [];

  return (
    <div className="grid gap-4">
      <PageHeader title={t('billing.report.reconciliationTitle')} />
      <BillingNav />
      <p className="text-fg-muted text-sm">{t('billing.report.reconciliationIntro')}</p>
      <div className="flex flex-wrap gap-3">
        <FormField label={t('billing.report.scope')}>
          <Select
            name="scope"
            value={scope}
            onChange={(e) => setScope(e.target.value)}
            options={[
              { value: 'TENANT', label: t('billing.report.scopes.TENANT') },
              { value: 'PROVIDER', label: t('billing.report.scopes.PROVIDER') },
            ]}
            placeholder={t('billing.report.allScopes')}
            className="w-48"
          />
        </FormField>
        <FormField label={t('billing.office.status')}>
          <Select
            name="status"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            options={['BALANCED', 'DIFFERENCES', 'FAILED'].map((s) => ({
              value: s,
              label: t(`billing.report.runStatus.${s}`),
            }))}
            placeholder={t('billing.office.allStatuses')}
            className="w-48"
          />
        </FormField>
      </div>
      {runs.isPending ? (
        <p className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('billing.report.runsLoading')}
        </p>
      ) : runs.isError ? (
        <ProblemAlert problem={problemOf(runs.error)} />
      ) : rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('billing.report.reconciliationEmpty')}</p>
      ) : !wide ? (
        <ul className="grid gap-2" data-testid="run-table">
          {rows.map((r) => (
            <li
              key={r.id}
              className="bg-surface-raised border-line rounded-lg border p-3"
              data-testid="run-row"
            >
              <div className="flex items-baseline justify-between gap-3">
                <Link
                  to="/billing/reconciliation/$runId"
                  params={{ runId: r.id }}
                  className="text-primary text-sm underline-offset-4 hover:underline"
                >
                  {formatDate(r.periodFrom)} · {t('billing.report.runNo')} {r.runNo}
                </Link>
                <Badge tone={runTone(r.status)}>{t(`billing.report.runStatus.${r.status}`)}</Badge>
              </div>
              <p className="mt-1 text-sm">
                {r.scope === 'PROVIDER' ? (r.providerName ?? '…') : t('billing.report.tenantWide')}
              </p>
              <p className="text-fg-muted mt-1 flex justify-between text-sm">
                <span>{t('billing.report.differenceCount', { count: r.differenceCount })}</span>
                <span className="text-fg font-mono tabular-nums">
                  {formatMoney(r.difference, r.currencyCode)}
                </span>
              </p>
            </li>
          ))}
        </ul>
      ) : (
        <div className="relative overflow-x-auto">
          <Table data-testid="run-table">
            <THead>
              <TR>
                <TH>{t('billing.report.period')}</TH>
                <TH>{t('billing.office.status')}</TH>
                <TH>{t('billing.report.scope')}</TH>
                <TH>{t('billing.report.provider')}</TH>
                <TH className="text-right">{t('billing.report.totals.settled')}</TH>
                <TH className="text-right">{t('billing.report.totals.paid')}</TH>
                <TH className="text-right">{t('billing.report.difference')}</TH>
                <TH>{t('billing.report.ranAt')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((r) => (
                <TR key={r.id} data-testid="run-row">
                  <TD className="whitespace-nowrap">
                    <Link
                      to="/billing/reconciliation/$runId"
                      params={{ runId: r.id }}
                      className="text-primary underline-offset-4 hover:underline"
                    >
                      {formatDate(r.periodFrom)}
                    </Link>
                    <span className="text-fg-muted ml-2 text-xs">
                      {t('billing.report.runNo')} {r.runNo}
                    </span>
                  </TD>
                  <TD>
                    <Badge tone={runTone(r.status)}>
                      {t(`billing.report.runStatus.${r.status}`)}
                    </Badge>
                  </TD>
                  <TD>{t(`billing.report.scopes.${r.scope}`)}</TD>
                  <TD>{r.scope === 'PROVIDER' ? (r.providerName ?? '…') : '—'}</TD>
                  <TD className="text-right font-mono tabular-nums">
                    {formatMoney(r.settledTotal, r.currencyCode)}
                  </TD>
                  <TD className="text-right font-mono tabular-nums">
                    {formatMoney(r.paidTotal, r.currencyCode)}
                  </TD>
                  <TD className="text-right font-mono tabular-nums">
                    {formatMoney(r.difference, r.currencyCode)}
                    <span className="text-fg-muted block font-sans text-xs">
                      {t('billing.report.differenceCount', { count: r.differenceCount })}
                    </span>
                  </TD>
                  <TD className="whitespace-nowrap">{formatDateTime(r.ranAt)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
    </div>
  );
}
