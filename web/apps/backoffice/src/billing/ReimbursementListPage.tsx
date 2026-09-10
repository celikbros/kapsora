import type { ReimbursementStatus } from '@kapsora/api-client';
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

import { usePersonName } from '../claims/names';
import { problemOf } from '../problems';
import { BillingNav } from './BillingNav';
import { useReimbursements } from './queries';
import { reimbursementTone } from './status';

const STATUSES: ReimbursementStatus[] = [
  'SUBMITTED',
  'UNDER_REVIEW',
  'APPROVED',
  'PARTIALLY_APPROVED',
  'REJECTED',
  'PAYMENT_ORDERED',
  'PAID',
  'CANCELLED',
];

/** Members' reimbursement requests, the ones waiting for a decision first. */
export function ReimbursementListPage() {
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const [status, setStatus] = useState('');
  const reimbursements = useReimbursements(status ? { status: status as ReimbursementStatus } : {});
  const rows = (reimbursements.data?.items ?? []).filter((r) => r.status !== 'DRAFT');

  return (
    <div className="grid gap-4">
      <PageHeader title={t('billing.office.reimbursementsTitle')} />
      <BillingNav />
      <p className="text-fg-muted text-sm">{t('billing.office.reimbursementsIntro')}</p>
      <FormField label={t('billing.office.status')}>
        <Select
          name="status"
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={STATUSES.map((s) => ({
            value: s,
            label: t(`billing.reimbursementStatus.${s}`),
          }))}
          placeholder={t('billing.office.allStatuses')}
          className="w-56"
        />
      </FormField>
      {reimbursements.isPending ? (
        <Spinner />
      ) : reimbursements.isError ? (
        <ProblemAlert problem={problemOf(reimbursements.error)} />
      ) : rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('billing.office.reimbursementsEmpty')}</p>
      ) : !wide ? (
        <ul className="grid gap-2" data-testid="reimbursement-table">
          {rows.map((r) => (
            <StackedRow key={r.id} row={r} />
          ))}
        </ul>
      ) : (
        <div className="relative overflow-x-auto">
          <Table data-testid="reimbursement-table">
            <THead>
              <TR>
                <TH>{t('billing.office.reference')}</TH>
                <TH>{t('billing.office.person')}</TH>
                <TH>{t('billing.office.serviceDate')}</TH>
                <TH>{t('billing.office.status')}</TH>
                <TH className="text-right">{t('billing.office.requested')}</TH>
                <TH className="text-right">{t('billing.office.approved')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((r) => (
                <Row key={r.id} row={r} />
              ))}
            </TBody>
          </Table>
        </div>
      )}
    </div>
  );
}

interface RowProps {
  row: {
    id: string;
    reference: string;
    personId: string;
    serviceDate: string;
    status: ReimbursementStatus;
    requestedAmount: string;
    approvedAmount?: string | null;
    currencyCode: string;
  };
}

function StackedRow({ row }: RowProps) {
  const { t } = useTranslation();
  const name = usePersonName(row.personId);
  return (
    <li
      className="bg-surface-raised border-line rounded-lg border p-3"
      data-testid="reimbursement-row"
    >
      <div className="flex items-baseline justify-between gap-3">
        <Link
          to="/billing/reimbursements/$reimbursementId"
          params={{ reimbursementId: row.id }}
          className="text-primary font-mono text-xs underline-offset-4 hover:underline"
        >
          {row.reference}
        </Link>
        <Badge tone={reimbursementTone(row.status)}>
          {t(`billing.reimbursementStatus.${row.status}`)}
        </Badge>
      </div>
      <p className="mt-1 text-sm">{name === undefined ? '…' : (name ?? '—')}</p>
      <p className="text-fg-muted mt-1 text-sm">{formatDate(row.serviceDate)}</p>
      <p className="text-fg-muted mt-1 flex justify-between text-sm">
        <span>{t('billing.office.requested')}</span>
        <span className="text-fg font-mono tabular-nums">
          {formatMoney(row.requestedAmount, row.currencyCode)}
        </span>
      </p>
      <p className="text-fg-muted flex justify-between text-sm">
        <span>{t('billing.office.approved')}</span>
        <span className="text-fg font-mono tabular-nums">
          {row.approvedAmount ? formatMoney(row.approvedAmount, row.currencyCode) : '—'}
        </span>
      </p>
    </li>
  );
}

function Row({ row }: RowProps) {
  const { t } = useTranslation();
  const name = usePersonName(row.personId);
  return (
    <TR data-testid="reimbursement-row">
      <TD>
        <Link
          to="/billing/reimbursements/$reimbursementId"
          params={{ reimbursementId: row.id }}
          className="text-primary font-mono text-xs underline-offset-4 hover:underline"
        >
          {row.reference}
        </Link>
      </TD>
      <TD>{name === undefined ? '…' : (name ?? '—')}</TD>
      <TD className="whitespace-nowrap">{formatDate(row.serviceDate)}</TD>
      <TD>
        <Badge tone={reimbursementTone(row.status)}>
          {t(`billing.reimbursementStatus.${row.status}`)}
        </Badge>
      </TD>
      <TD className="text-right font-mono tabular-nums">
        {formatMoney(row.requestedAmount, row.currencyCode)}
      </TD>
      <TD className="text-right font-mono tabular-nums">
        {row.approvedAmount ? formatMoney(row.approvedAmount, row.currencyCode) : '—'}
      </TD>
    </TR>
  );
}
