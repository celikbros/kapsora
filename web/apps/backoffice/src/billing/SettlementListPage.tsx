import type { SettlementStatus } from '@kapsora/api-client';
import { formatDate, formatMoney, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  FormField,
  HelpHint,
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
import { Link, useSearch } from '@tanstack/react-router';
import { useState } from 'react';

import { problemOf } from '../problems';
import { BillingNav } from './BillingNav';
import { useSettlements } from './queries';
import { settlementTone } from './status';

const STATUSES: SettlementStatus[] = [
  'PENDING_APPROVAL',
  'APPROVED',
  'POSTED',
  'PARTIALLY_PAID',
  'PAID',
  'RECONCILED',
  'CANCELLED',
];

/** What the payer owes whom, by settlement; the ones waiting for a signature first. */
export function SettlementListPage() {
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const search = useSearch({ from: '/app/billing/settlements' });
  const [status, setStatus] = useState(search.status ?? '');
  const settlements = useSettlements(status ? { status: status as SettlementStatus } : {});
  const rows = settlements.data?.items ?? [];

  return (
    <div className="grid gap-4">
      <PageHeader title={t('billing.office.settlementsTitle')} />
      <BillingNav />
      <p className="text-fg-muted text-sm">{t('billing.office.settlementsIntro')}</p>
      <FormField label={t('billing.office.status')}>
        <Select
          name="status"
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={STATUSES.map((s) => ({ value: s, label: t(`billing.settlementStatus.${s}`) }))}
          placeholder={t('billing.office.allStatuses')}
          className="w-56"
        />
      </FormField>
      {settlements.isPending ? (
        <Spinner />
      ) : settlements.isError ? (
        <ProblemAlert problem={problemOf(settlements.error)} />
      ) : rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('billing.office.settlementsEmpty')}</p>
      ) : !wide ? (
        <ul className="grid gap-2" data-testid="settlement-table">
          {rows.map((s) => (
            <li
              key={s.id}
              className="bg-surface-raised border-line rounded-lg border p-3"
              data-testid="settlement-row"
            >
              <div className="flex items-baseline justify-between gap-3">
                <Link
                  to="/billing/settlements/$settlementId"
                  params={{ settlementId: s.id }}
                  className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                >
                  {s.reference}
                </Link>
                <Badge tone={settlementTone(s.status)}>
                  {t(`billing.settlementStatus.${s.status}`)}
                </Badge>
              </div>
              <p className="mt-1 text-sm">{s.providerName ?? '…'}</p>
              <p className="text-fg-muted mt-1 flex justify-between text-sm">
                <span>
                  {t('billing.office.due')} {formatDate(s.dueDate)}
                </span>
                <span className="text-fg font-mono tabular-nums">
                  {formatMoney(s.payableAmount, s.currencyCode)}
                </span>
              </p>
            </li>
          ))}
        </ul>
      ) : (
        <div className="relative overflow-x-auto">
          <Table data-testid="settlement-table">
            <THead>
              <TR>
                <TH>{t('billing.office.reference')}</TH>
                <TH>{t('billing.office.provider')}</TH>
                <TH>
                  <span className="inline-flex items-center gap-1.5">
                    {t('billing.office.batch')}
                    <HelpHint term="icmal" />
                  </span>
                </TH>
                <TH>{t('billing.office.status')}</TH>
                <TH>{t('billing.office.due')}</TH>
                <TH className="text-right">{t('billing.totals.payable')}</TH>
                <TH className="text-right">{t('billing.totals.paid')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((s) => (
                <TR key={s.id} data-testid="settlement-row">
                  <TD>
                    <Link
                      to="/billing/settlements/$settlementId"
                      params={{ settlementId: s.id }}
                      className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                    >
                      {s.reference}
                    </Link>
                  </TD>
                  <TD>{s.providerName ?? '…'}</TD>
                  <TD className="font-mono text-xs">{s.batchReference}</TD>
                  <TD>
                    <Badge tone={settlementTone(s.status)}>
                      {t(`billing.settlementStatus.${s.status}`)}
                    </Badge>
                  </TD>
                  <TD className="whitespace-nowrap">{formatDate(s.dueDate)}</TD>
                  <TD className="text-right font-mono tabular-nums">
                    {formatMoney(s.payableAmount, s.currencyCode)}
                  </TD>
                  <TD className="text-right font-mono tabular-nums">
                    {formatMoney(s.paidAmount, s.currencyCode)}
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
