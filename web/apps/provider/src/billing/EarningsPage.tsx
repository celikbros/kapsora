import { formatMoney, useTranslation } from '@kapsora/i18n';
import {
  Button,
  Card,
  FormField,
  Input,
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
import { Link, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';

import { problemOf } from '../problems';
import { useProviderOrganizationId } from '../queries';
import { useEarnings } from './queries';

function monthStart(): string {
  const d = new Date();
  return new Date(Date.UTC(d.getFullYear(), d.getMonth(), 1)).toISOString().slice(0, 10);
}
function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/**
 * Hakediş: what the provider earned in a period, per currency, and the invoiceable part —
 * the figure its invoice will be checked against. Every number is the server's.
 */
export function EarningsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const providerId = useProviderOrganizationId();
  const [from, setFrom] = useState(monthStart);
  const [to, setTo] = useState(today);
  const [currency, setCurrency] = useState('');
  const earnings = useEarnings(providerId, {
    from,
    to,
    ...(currency ? { currency } : {}),
  });

  return (
    <div className="grid gap-4">
      <PageHeader
        title={t('billing.provider.earningsTitle')}
        actions={
          <div className="flex gap-2">
            <Link
              to="/billing/invoices"
              className="text-primary self-center text-sm underline-offset-4 hover:underline"
            >
              {t('billing.provider.invoicesTitle')}
            </Link>
            <Link
              to="/billing/batches"
              className="text-primary self-center text-sm underline-offset-4 hover:underline"
            >
              {t('billing.provider.batchesTitle')}
            </Link>
          </div>
        }
      />
      <p className="text-fg-muted text-sm">{t('billing.provider.earningsIntro')}</p>
      <Card>
        <div className="grid gap-3 sm:grid-cols-3">
          <FormField label={t('billing.provider.from')}>
            <Input type="date" name="from" value={from} onChange={(e) => setFrom(e.target.value)} />
          </FormField>
          <FormField label={t('billing.provider.to')}>
            <Input type="date" name="to" value={to} onChange={(e) => setTo(e.target.value)} />
          </FormField>
          <FormField label={t('billing.provider.currency')}>
            <Select
              name="currency"
              value={currency}
              onChange={(e) => setCurrency(e.target.value)}
              options={[{ value: 'TRY', label: 'TRY' }]}
              placeholder={t('billing.provider.anyCurrency')}
            />
          </FormField>
        </div>
      </Card>

      {earnings.isPending && providerId ? (
        <Spinner />
      ) : earnings.isError ? (
        <ProblemAlert problem={problemOf(earnings.error)} />
      ) : !earnings.data || earnings.data.currencies.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('billing.provider.noEarnings')}</p>
      ) : (
        earnings.data.currencies.map((c) => (
          <Card key={c.currencyCode} className="min-w-0" data-testid="earnings-currency">
            <div className="flex flex-wrap items-baseline justify-between gap-3">
              <h2 className="text-base font-semibold">{c.currencyCode}</h2>
              <span className="text-fg-muted text-sm">
                {t('billing.provider.invoiceableCount', { count: c.invoiceableClaimIds.length })}
              </span>
            </div>
            <div className="relative mt-3 overflow-x-auto">
              <Table>
                <THead>
                  <TR>
                    <TH>{t('billing.provider.status')}</TH>
                    <TH className="text-right">{t('billing.provider.count')}</TH>
                    <TH className="text-right">{t('billing.provider.approved')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {c.byStatus.map((row) => (
                    <TR key={row.status}>
                      <TD>{t(`claims.status.${row.status}`)}</TD>
                      <TD className="text-right font-mono tabular-nums">{row.claimCount}</TD>
                      <TD className="text-right font-mono tabular-nums">
                        {formatMoney(row.approvedTotal, c.currencyCode)}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
            <dl className="mt-3 max-w-sm grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm">
              <dt className="text-fg-muted">{t('billing.provider.approved')}</dt>
              <dd className="text-right font-mono tabular-nums">
                {formatMoney(c.approvedTotal, c.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('billing.provider.adjustments')}</dt>
              <dd className="text-right font-mono tabular-nums">
                {formatMoney(c.adjustmentTotal, c.currencyCode)}
              </dd>
              <dt className="font-medium">{t('billing.provider.invoiceable')}</dt>
              <dd
                className="text-right font-mono text-base font-semibold tabular-nums"
                data-testid="invoiceable-total"
              >
                {formatMoney(c.invoiceableTotal, c.currencyCode)}
              </dd>
            </dl>
            {c.invoiceableClaimIds.length > 0 ? (
              <div className="mt-3">
                <Button
                  size="sm"
                  onClick={() =>
                    void navigate({
                      to: '/billing/invoices/new',
                      search: { currency: c.currencyCode, claims: c.invoiceableClaimIds.join(',') },
                    })
                  }
                >
                  {t('billing.provider.newInvoice')}
                </Button>
              </div>
            ) : null}
          </Card>
        ))
      )}
    </div>
  );
}
