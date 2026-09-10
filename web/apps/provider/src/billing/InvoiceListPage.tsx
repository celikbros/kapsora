import type { InvoiceStatus } from '@kapsora/api-client';
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
import { useInvoices } from './queries';
import { invoiceTone } from './status';

const STATUSES: InvoiceStatus[] = [
  'DRAFT',
  'SUBMITTED',
  'IN_BATCH',
  'RETURNED',
  'APPROVED',
  'PARTIALLY_APPROVED',
  'REJECTED',
  'SETTLED',
  'CANCELLED',
];

/** The provider's invoices as they are, newest first, with the batch each sits in. */
export function InvoiceListPage() {
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const [status, setStatus] = useState('');
  const invoices = useInvoices(status ? { status: status as InvoiceStatus } : {});
  const rows = invoices.data?.items ?? [];

  return (
    <div className="grid gap-4">
      <PageHeader
        title={t('billing.provider.invoicesTitle')}
        actions={
          <Link
            to="/billing"
            className="text-primary self-center text-sm underline-offset-4 hover:underline"
          >
            {t('billing.provider.earningsTitle')}
          </Link>
        }
      />
      <FormField label={t('billing.provider.status')}>
        <Select
          name="status"
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={STATUSES.map((s) => ({ value: s, label: t(`billing.invoiceStatus.${s}`) }))}
          placeholder={t('billing.office.allStatuses')}
          className="w-56"
        />
      </FormField>
      {invoices.isPending ? (
        <Spinner />
      ) : invoices.isError ? (
        <ProblemAlert problem={problemOf(invoices.error)} />
      ) : rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('billing.provider.invoicesEmpty')}</p>
      ) : !wide ? (
        <ul className="grid gap-2" data-testid="invoice-table">
          {rows.map((inv) => (
            <li
              key={inv.id}
              className="bg-surface-raised border-line rounded-lg border p-3"
              data-testid="invoice-row"
            >
              <div className="flex items-baseline justify-between gap-3">
                <Link
                  to="/billing/invoices/$invoiceId"
                  params={{ invoiceId: inv.id }}
                  className="text-primary font-mono text-sm underline-offset-4 hover:underline"
                >
                  {inv.invoiceNumber}
                </Link>
                <Badge tone={invoiceTone(inv.status)}>
                  {t(`billing.invoiceStatus.${inv.status}`)}
                </Badge>
              </div>
              <p className="text-fg-muted mt-1 flex justify-between text-sm">
                <span>{formatDate(inv.invoiceDate)}</span>
                <span className="text-fg font-mono tabular-nums">
                  {formatMoney(inv.payableAmount, inv.currencyCode)}
                </span>
              </p>
            </li>
          ))}
        </ul>
      ) : (
        <div className="relative overflow-x-auto">
          <Table data-testid="invoice-table">
            <THead>
              <TR>
                <TH>{t('billing.provider.number')}</TH>
                <TH>{t('billing.provider.date')}</TH>
                <TH>{t('billing.provider.status')}</TH>
                <TH className="text-right">{t('billing.provider.payable')}</TH>
                <TH className="text-right">{t('billing.provider.allocationTotal')}</TH>
                <TH>{t('billing.provider.batch')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((inv) => (
                <TR key={inv.id} data-testid="invoice-row">
                  <TD>
                    <Link
                      to="/billing/invoices/$invoiceId"
                      params={{ invoiceId: inv.id }}
                      className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                    >
                      {inv.invoiceNumber}
                    </Link>
                  </TD>
                  <TD>{formatDate(inv.invoiceDate)}</TD>
                  <TD>
                    <Badge tone={invoiceTone(inv.status)}>
                      {t(`billing.invoiceStatus.${inv.status}`)}
                    </Badge>
                  </TD>
                  <TD className="text-right font-mono tabular-nums">
                    {formatMoney(inv.payableAmount, inv.currencyCode)}
                  </TD>
                  <TD className="text-right font-mono tabular-nums">
                    {formatMoney(inv.allocationTotal, inv.currencyCode)}
                  </TD>
                  <TD>
                    {inv.batchId ? (
                      <Link
                        to="/billing/batches/$batchId"
                        params={{ batchId: inv.batchId }}
                        className="text-primary underline-offset-4 hover:underline"
                      >
                        {t('billing.provider.batch')}
                      </Link>
                    ) : (
                      <span className="text-fg-muted">{t('billing.provider.noBatch')}</span>
                    )}
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
