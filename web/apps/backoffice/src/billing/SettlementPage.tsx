import { useStepUp } from '@kapsora/auth';
import { formatDate, formatDateTime, formatMoney, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Spinner,
  StepUpDialog,
  Textarea,
  useToast,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { BillingNav } from './BillingNav';
import {
  useApproveSettlement,
  useCancelSettlement,
  useCreatePaymentRecord,
  useSettlement,
} from './queries';
import { settlementTone } from './status';

/**
 * One settlement as the sequence that produced it: the icmal it came from, the approved
 * figure, what was withheld and why, what is payable, the approval with its second
 * signature, and the payments the bank confirmed. The figures are the server's; the
 * remaining amount is read, never subtracted here.
 */
export function SettlementPage() {
  const { settlementId } = useParams({ from: '/app/billing/settlements/$settlementId' });
  const { t } = useTranslation();
  const toast = useToast();
  const stepUp = useStepUp();
  const settlement = useSettlement(settlementId);
  const approve = useApproveSettlement(settlementId);
  const cancel = useCancelSettlement(settlementId);
  const pay = useCreatePaymentRecord(settlementId);
  const record = settlement.data?.data ?? null;
  const etag = settlement.data?.etag ?? '';
  const [cancelOpen, setCancelOpen] = useState(false);
  const [reasonCode, setReasonCode] = useState('');
  const [reasonText, setReasonText] = useState('');
  const [payment, setPayment] = useState({ externalReference: '', amount: '', paidAt: '' });

  if (settlement.isPending) return <Spinner />;
  if (settlement.isError) return <ProblemAlert problem={problemOf(settlement.error)} />;
  if (!record) return null;
  const canApprove = record.status === 'PENDING_APPROVAL';
  const canPay =
    record.status === 'APPROVED' ||
    record.status === 'POSTED' ||
    record.status === 'PARTIALLY_PAID';

  async function onApprove() {
    try {
      const result = await stepUp.run(() => approve.mutateAsync(etag));
      if (result) {
        toast.notify({ tone: 'success', title: t('billing.office.settlementApproved') });
      }
    } catch {
      // The mutation keeps the problem; the alert below shows it.
    }
  }

  function onCancel(e: FormEvent) {
    e.preventDefault();
    cancel.mutate(
      {
        etag,
        body: {
          reasonCode: reasonCode.trim(),
          ...(reasonText.trim() ? { reasonText: reasonText.trim() } : {}),
        },
      },
      {
        onSuccess: () => {
          setCancelOpen(false);
          toast.notify({ tone: 'info', title: t('billing.office.settlementCancelled') });
        },
      },
    );
  }

  function onPay(e: FormEvent) {
    e.preventDefault();
    pay.mutate(
      {
        externalReference: payment.externalReference.trim(),
        amount: payment.amount.trim(),
        paidAt: new Date(payment.paidAt).toISOString(),
      },
      {
        onSuccess: () => {
          setPayment({ externalReference: '', amount: '', paidAt: '' });
          toast.notify({ tone: 'success', title: t('billing.office.paymentRecorded') });
        },
      },
    );
  }

  return (
    <div className="grid gap-4">
      <Breadcrumb
        items={[
          {
            label: t('billing.office.settlementsTitle'),
            render: (label) => <Link to="/billing/settlements">{label}</Link>,
          },
          { label: record.reference },
        ]}
      />
      <PageHeader
        title={record.reference}
        description={`${record.providerName ?? ''} · ${t('billing.office.batch')} ${record.batchReference}`}
        actions={
          <Badge tone={settlementTone(record.status)} data-testid="settlement-status">
            {t(`billing.settlementStatus.${record.status}`)}
          </Badge>
        }
      />
      <BillingNav />

      <Card>
        <dl
          className="max-w-sm grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm"
          data-testid="settlement-figures"
        >
          <dt className="text-fg-muted">{t('billing.totals.approved')}</dt>
          <dd className="text-right font-mono tabular-nums">
            {formatMoney(record.approvedAmount, record.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.totals.withheld')}</dt>
          <dd className="text-right font-mono tabular-nums">
            {formatMoney(record.withheldAmount, record.currencyCode)}
          </dd>
          <dt className="font-medium">{t('billing.totals.payable')}</dt>
          <dd
            className="text-right font-mono text-base font-semibold tabular-nums"
            data-testid="payable"
          >
            {formatMoney(record.payableAmount, record.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.totals.paid')}</dt>
          <dd className="text-right font-mono tabular-nums" data-testid="paid">
            {formatMoney(record.paidAmount, record.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.office.due')}</dt>
          <dd className="text-right">{formatDate(record.dueDate)}</dd>
        </dl>
        <p className="text-fg-muted mt-2 text-xs">{t('billing.totals.arithmetic')}</p>
      </Card>

      <Card className="min-w-0">
        <h2 className="text-base font-semibold">{t('billing.office.recoveries')}</h2>
        {record.recoveries.length === 0 ? (
          <p className="text-fg-muted mt-2 text-sm">{t('billing.office.noRecoveries')}</p>
        ) : (
          <ul className="mt-2 grid gap-1 text-sm" data-testid="recovery-list">
            {record.recoveries.map((r) => (
              <li key={r.id} className="flex justify-between gap-3">
                <Link
                  to="/claims/$claimId"
                  params={{ claimId: r.claimId }}
                  className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                >
                  {t('claims.title')} · {r.claimId.slice(0, 8)}
                </Link>
                <span className="font-mono tabular-nums">
                  {formatMoney(r.amount, record.currencyCode)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card>
        <h2 className="text-base font-semibold">{t('billing.office.sequence')}</h2>
        <ol className="mt-2 grid gap-1 text-sm" data-testid="settlement-sequence">
          <li className="flex justify-between gap-3">
            <span>{t('billing.office.decidedAt')}</span>
            <span className="text-fg-muted">{formatDateTime(record.createdAt)}</span>
          </li>
          {record.approvedAt ? (
            <li className="flex justify-between gap-3">
              <span>{t('billing.office.approvedAt')}</span>
              <span className="text-fg-muted">{formatDateTime(record.approvedAt)}</span>
            </li>
          ) : null}
          {record.checkedBy ? (
            <li className="flex justify-between gap-3" data-testid="checked-line">
              <span>{t('billing.office.checkedBy')}</span>
              <span className="text-fg-muted">{t('billing.office.checkedHint')}</span>
            </li>
          ) : null}
          {record.payments.map((p) => (
            <li key={p.id} className="flex justify-between gap-3">
              <span>
                {t('billing.office.payments')} ·{' '}
                <span className="font-mono text-xs">{p.externalReference}</span>
              </span>
              <span className="font-mono tabular-nums">
                {formatMoney(p.amount, p.currencyCode)}
                <span className="text-fg-muted ml-2 font-sans">{formatDate(p.paidAt)}</span>
              </span>
            </li>
          ))}
        </ol>
      </Card>

      {canApprove ? (
        <Card>
          <h2 className="text-base font-semibold">{t('billing.office.approveSettlement')}</h2>
          <p className="text-fg-muted mt-1 text-sm">{t('billing.office.approveHint')}</p>
          <ProblemAlert
            problem={
              approve.isError
                ? problemOf(approve.error)
                : cancel.isError
                  ? problemOf(cancel.error)
                  : null
            }
            className="mt-3"
          />
          <div className="mt-3 flex flex-wrap gap-2">
            <Button
              onClick={() => void onApprove()}
              loading={approve.isPending}
              data-testid="approve-settlement"
            >
              {t('billing.office.approveSettlement')}
            </Button>
            <Button variant="secondary" onClick={() => setCancelOpen((v) => !v)}>
              {t('billing.office.cancelSettlement')}
            </Button>
          </div>
          {cancelOpen ? (
            <form
              onSubmit={onCancel}
              className="border-line mt-3 grid gap-3 border-t pt-3 sm:grid-cols-2"
              noValidate
              data-testid="cancel-form"
            >
              <FormField
                label={t('billing.office.cancelReason')}
                required
                requiredLabel={t('common.requiredMark')}
              >
                <Input
                  name="reasonCode"
                  value={reasonCode}
                  onChange={(e) => setReasonCode(e.target.value.toUpperCase())}
                  className="font-mono"
                  required
                />
              </FormField>
              <FormField label={t('billing.office.reasonText')}>
                <Textarea
                  name="reasonText"
                  rows={2}
                  value={reasonText}
                  onChange={(e) => setReasonText(e.target.value)}
                />
              </FormField>
              <div className="sm:col-span-2">
                <Button type="submit" size="sm" variant="secondary" loading={cancel.isPending}>
                  {t('billing.office.cancelSettlement')}
                </Button>
              </div>
            </form>
          ) : null}
          <StepUpDialog
            open={stepUp.required}
            action={t('billing.office.approveSettlement')}
            busy={stepUp.busy}
            problem={stepUp.error}
            onConfirm={(password) => void stepUp.confirm(password)}
            onCancel={stepUp.cancel}
          />
        </Card>
      ) : null}

      {canPay ? (
        <Card>
          <h2 className="text-base font-semibold">{t('billing.office.recordPayment')}</h2>
          <p className="text-fg-muted mt-1 text-sm">{t('billing.office.remainingHint')}</p>
          <form
            onSubmit={onPay}
            className="mt-3 grid gap-3 sm:grid-cols-3"
            noValidate
            data-testid="payment-form"
          >
            <FormField
              label={t('billing.office.externalReference')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="externalReference"
                value={payment.externalReference}
                onChange={(e) => setPayment({ ...payment, externalReference: e.target.value })}
                className="font-mono"
                required
              />
            </FormField>
            <FormField
              label={t('billing.office.amount')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="amount"
                inputMode="decimal"
                value={payment.amount}
                onChange={(e) => setPayment({ ...payment, amount: e.target.value })}
                className="text-right font-mono"
                required
              />
            </FormField>
            <FormField
              label={t('billing.office.paidAt')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                type="date"
                name="paidAt"
                value={payment.paidAt}
                onChange={(e) => setPayment({ ...payment, paidAt: e.target.value })}
                required
              />
            </FormField>
            <div className="sm:col-span-3">
              <ProblemAlert problem={pay.isError ? problemOf(pay.error) : null} className="mb-3" />
              <Button
                type="submit"
                size="sm"
                loading={pay.isPending}
                disabled={
                  payment.externalReference.trim() === '' ||
                  payment.amount.trim() === '' ||
                  payment.paidAt === ''
                }
                data-testid="record-payment"
              >
                {t('billing.office.recordPayment')}
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}
