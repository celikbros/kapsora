import type { BatchStatus } from '@kapsora/api-client';
import { formatDate, formatMoney, useTranslation } from '@kapsora/i18n';
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
import { useBatches } from './queries';
import { batchTone } from './status';

const STATUSES: BatchStatus[] = [
  'SUBMITTED',
  'UNDER_REVIEW',
  'DECIDED',
  'SETTLING',
  'CLOSED',
  'CANCELLED',
];

/** The icmals providers sent, the ones waiting first. */
export function BatchReviewListPage() {
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const [status, setStatus] = useState('');
  const batches = useBatches(status ? { status: status as BatchStatus } : {});
  const rows = (batches.data?.items ?? []).filter((b) => b.status !== 'DRAFT');

  return (
    <div className="grid gap-4">
      <PageHeader title={t('billing.office.batchesTitle')} />
      <BillingNav />
      <p className="text-fg-muted text-sm">{t('billing.office.batchesIntro')}</p>
      <FormField label={t('billing.office.status')}>
        <Select
          name="status"
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={STATUSES.map((s) => ({ value: s, label: t(`billing.batchStatus.${s}`) }))}
          placeholder={t('billing.office.allStatuses')}
          className="w-56"
        />
      </FormField>
      {batches.isPending ? (
        <Spinner />
      ) : batches.isError ? (
        <ProblemAlert problem={problemOf(batches.error)} />
      ) : rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('billing.office.batchesEmpty')}</p>
      ) : !wide ? (
        <ul className="grid gap-2" data-testid="batch-table">
          {rows.map((b) => (
            <li
              key={b.id}
              className="bg-surface-raised border-line rounded-lg border p-3"
              data-testid="batch-row"
            >
              <div className="flex items-baseline justify-between gap-3">
                <Link
                  to="/billing/batches/$batchId"
                  params={{ batchId: b.id }}
                  className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                >
                  {b.reference}
                </Link>
                <Badge tone={batchTone(b.status)}>{t(`billing.batchStatus.${b.status}`)}</Badge>
              </div>
              <p className="mt-1 text-sm">{b.providerName ?? '…'}</p>
              <p className="text-fg-muted mt-1 flex justify-between text-sm">
                <span>
                  {formatDate(b.periodFrom)} – {formatDate(b.periodTo)}
                </span>
                <span className="text-fg font-mono tabular-nums">
                  {formatMoney(b.submittedTotal, b.currencyCode)}
                </span>
              </p>
            </li>
          ))}
        </ul>
      ) : (
        <div className="relative overflow-x-auto">
          <Table data-testid="batch-table">
            <THead>
              <TR>
                <TH>{t('billing.office.reference')}</TH>
                <TH>{t('billing.office.provider')}</TH>
                <TH>{t('billing.office.period')}</TH>
                <TH>{t('billing.office.status')}</TH>
                <TH className="text-right">{t('billing.office.invoices')}</TH>
                <TH className="text-right">{t('billing.office.submitted')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((b) => (
                <TR key={b.id} data-testid="batch-row">
                  <TD>
                    <Link
                      to="/billing/batches/$batchId"
                      params={{ batchId: b.id }}
                      className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                    >
                      {b.reference}
                    </Link>
                  </TD>
                  <TD>{b.providerName ?? '…'}</TD>
                  <TD className="whitespace-nowrap">
                    {formatDate(b.periodFrom)} – {formatDate(b.periodTo)}
                  </TD>
                  <TD>
                    <Badge tone={batchTone(b.status)}>{t(`billing.batchStatus.${b.status}`)}</Badge>
                  </TD>
                  <TD className="text-right font-mono tabular-nums">{b.invoiceCount}</TD>
                  <TD className="text-right font-mono tabular-nums">
                    {formatMoney(b.submittedTotal, b.currencyCode)}
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
    </div>
  );
}
