import { useSelfPersonId } from '@kapsora/auth';
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
  Select,
  Spinner,
  Textarea,
  useToast,
  OwnFileNotice,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { useDefinitionOptions } from '../catalogOptions';
import { useOrganizationName, usePersonName } from '../claims/names';
import { DocumentsPanel } from '../documents/DocumentsPanel';
import { problemOf } from '../problems';
import { BillingNav } from './BillingNav';
import { useDecideReimbursement, useRecordReimbursementPayment, useReimbursement } from './queries';
import { reimbursementTone } from './status';

type Choice = 'APPROVE' | 'PARTIAL' | 'REJECT';

/**
 * One member's reimbursement in front of the payer: who, what, when, the receipt, the
 * account's last four characters and nothing more, the duplicate the server noticed, and
 * the decision — full, partial with a reason, or a rejection with a reason.
 */
export function ReimbursementPage() {
  const { reimbursementId } = useParams({ from: '/app/billing/reimbursements/$reimbursementId' });
  const { t } = useTranslation();
  const toast = useToast();
  const reimbursement = useReimbursement(reimbursementId);
  const decide = useDecideReimbursement(reimbursementId);
  const pay = useRecordReimbursementPayment(reimbursementId);
  const self = useSelfPersonId();
  const record = reimbursement.data?.data ?? null;
  const etag = reimbursement.data?.etag ?? '';
  const person = usePersonName(record?.personId);
  const provider = useOrganizationName(record?.providerOrganizationId);
  const definitions = useDefinitionOptions();
  const [choice, setChoice] = useState<Choice>('APPROVE');
  const [approvedAmount, setApprovedAmount] = useState('');
  const [reasonCode, setReasonCode] = useState('');
  const [reasonText, setReasonText] = useState('');
  const [paymentReference, setPaymentReference] = useState('');

  if (reimbursement.isPending) return <Spinner />;
  if (reimbursement.isError) return <ProblemAlert problem={problemOf(reimbursement.error)} />;
  if (!record) return null;
  // A reviewer's own refund: readable, decided by somebody else.
  const own = self !== null && record.personId === self;
  const open = record.status === 'SUBMITTED' || record.status === 'UNDER_REVIEW';
  const decidable = open && !own;
  const payable =
    record.status === 'APPROVED' ||
    record.status === 'PARTIALLY_APPROVED' ||
    record.status === 'PAYMENT_ORDERED';
  // A reviewer without the catalogue grant is told nothing rather than kept waiting.
  const service = definitions.isError
    ? null
    : definitions.data
      ? (definitions.data.find((d) => d.value === record.serviceDefinitionId)?.label ?? null)
      : undefined;

  function onDecide(e: FormEvent) {
    e.preventDefault();
    decide.mutate(
      {
        etag,
        body: {
          decision: choice === 'REJECT' ? 'REJECT' : 'APPROVE',
          ...(choice === 'PARTIAL' ? { approvedAmount: approvedAmount.trim() } : {}),
          ...(choice !== 'APPROVE' ? { reasonCode: reasonCode.trim() } : {}),
          ...(reasonText.trim() ? { reasonText: reasonText.trim() } : {}),
        },
      },
      {
        onSuccess: () =>
          toast.notify({ tone: 'success', title: t('billing.office.reimbursementDecided') }),
      },
    );
  }

  function onPay(e: FormEvent) {
    e.preventDefault();
    pay.mutate(
      { etag, body: { paymentReference: paymentReference.trim() } },
      {
        onSuccess: () =>
          toast.notify({ tone: 'success', title: t('billing.office.reimbursementPaid') }),
      },
    );
  }

  return (
    <div className="grid gap-4">
      <Breadcrumb
        items={[
          {
            label: t('billing.office.reimbursementsTitle'),
            render: (label) => <Link to="/billing/reimbursements">{label}</Link>,
          },
          { label: record.reference },
        ]}
      />
      <PageHeader
        title={record.reference}
        actions={
          <Badge tone={reimbursementTone(record.status)} data-testid="reimbursement-status">
            {t(`billing.reimbursementStatus.${record.status}`)}
          </Badge>
        }
      />
      <BillingNav />

      {record.duplicateOfReference ? (
        <p
          className="border-warning bg-warning-soft rounded-md border px-3 py-2 text-sm"
          role="status"
          data-testid="duplicate-line"
        >
          {t('billing.office.duplicate', { reference: record.duplicateOfReference })}
        </p>
      ) : null}

      <Card>
        <dl
          className="max-w-lg grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm"
          data-testid="reimbursement-facts"
        >
          <dt className="text-fg-muted">{t('billing.office.person')}</dt>
          <dd>
            <Link
              to="/people/$personId"
              params={{ personId: record.personId }}
              className="text-primary underline-offset-4 hover:underline"
            >
              {person === undefined ? '…' : (person ?? '—')}
            </Link>
          </dd>
          <dt className="text-fg-muted">{t('billing.member.service')}</dt>
          <dd>{service === undefined ? '…' : (service ?? '—')}</dd>
          <dt className="text-fg-muted">{t('billing.office.serviceDate')}</dt>
          <dd>{formatDate(record.serviceDate)}</dd>
          <dt className="text-fg-muted">{t('billing.member.provider')}</dt>
          <dd>{provider === undefined ? '…' : (provider ?? '—')}</dd>
          {record.decisionReasonCode ? (
            <>
              <dt className="text-fg-muted">{t('billing.office.reasonCode')}</dt>
              <dd>
                <span className="font-mono text-xs">{record.decisionReasonCode}</span>
              </dd>
            </>
          ) : null}
        </dl>
        <dl
          className="border-line mt-3 max-w-sm grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm border-t pt-3"
          data-testid="reimbursement-figures"
        >
          <dt className="text-fg-muted">{t('billing.office.requested')}</dt>
          <dd className="text-right font-mono tabular-nums" data-testid="requested">
            {formatMoney(record.requestedAmount, record.currencyCode)}
          </dd>
          <dt className={record.approvedAmount ? 'font-medium' : 'text-fg-muted'}>
            {t('billing.office.approved')}
          </dt>
          <dd
            className={
              record.approvedAmount
                ? 'text-right font-mono font-semibold tabular-nums'
                : 'text-right font-mono tabular-nums'
            }
            data-testid="approved"
          >
            {record.approvedAmount ? formatMoney(record.approvedAmount, record.currencyCode) : '—'}
          </dd>
          <dt className="text-fg-muted">{t('billing.office.account')}</dt>
          <dd className="text-right font-mono" data-testid="account-masked">
            {t('billing.member.ibanMasked', { tail: record.bankAccountMasked })}
          </dd>
        </dl>
      </Card>

      <Card>
        <h2 className="text-base font-semibold">{t('billing.office.receipt')}</h2>
        <div className="mt-3">
          <DocumentsPanel
            aggregateType="SERVICE_REQUEST"
            aggregateId={record.serviceRequestId}
            readOnly
          />
        </div>
      </Card>

      <Card>
        <h2 className="text-base font-semibold">{t('billing.office.sequence')}</h2>
        <ol className="mt-2 grid gap-1 text-sm" data-testid="reimbursement-sequence">
          {record.submittedAt ? (
            <li className="flex justify-between gap-3">
              <span>{t('billing.reimbursementStatus.SUBMITTED')}</span>
              <span className="text-fg-muted">{formatDateTime(record.submittedAt)}</span>
            </li>
          ) : null}
          {record.decidedAt ? (
            <li className="flex justify-between gap-3">
              <span>{t('billing.member.decided')}</span>
              <span className="text-fg-muted">{formatDateTime(record.decidedAt)}</span>
            </li>
          ) : null}
          {record.paidAt ? (
            <li className="flex justify-between gap-3">
              <span>
                {t('billing.member.paid')} ·{' '}
                <span className="font-mono text-xs">{record.paymentReference}</span>
              </span>
              <span className="text-fg-muted">{formatDateTime(record.paidAt)}</span>
            </li>
          ) : null}
        </ol>
      </Card>

      {open && own ? <OwnFileNotice /> : null}

      {decidable ? (
        <Card>
          <h2 className="text-base font-semibold">{t('billing.office.decisionTitle')}</h2>
          <form
            onSubmit={onDecide}
            className="mt-3 grid gap-3 sm:grid-cols-3"
            noValidate
            data-testid="reimbursement-decision"
          >
            <FormField
              label={t('billing.office.choice')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Select
                name="choice"
                value={choice}
                onChange={(e) => setChoice(e.target.value as Choice)}
                options={[
                  { value: 'APPROVE', label: t('billing.office.approveFull') },
                  { value: 'PARTIAL', label: t('billing.office.approvePart') },
                  { value: 'REJECT', label: t('billing.office.rejectReimbursement') },
                ]}
              />
            </FormField>
            {choice === 'PARTIAL' ? (
              <FormField
                label={t('billing.office.approvedAmount')}
                required
                requiredLabel={t('common.requiredMark')}
                hint={`< ${formatMoney(record.requestedAmount, record.currencyCode)}`}
              >
                <Input
                  name="approvedAmount"
                  inputMode="decimal"
                  value={approvedAmount}
                  onChange={(e) => setApprovedAmount(e.target.value)}
                  className="text-right font-mono"
                  required
                />
              </FormField>
            ) : null}
            {choice !== 'APPROVE' ? (
              <FormField
                label={t('billing.office.reasonCode')}
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
            ) : null}
            <div className="sm:col-span-3">
              <FormField label={t('billing.office.reasonText')}>
                <Textarea
                  name="reasonText"
                  rows={2}
                  value={reasonText}
                  onChange={(e) => setReasonText(e.target.value)}
                />
              </FormField>
            </div>
            <div className="sm:col-span-3">
              <ProblemAlert
                problem={decide.isError ? problemOf(decide.error) : null}
                className="mb-3"
              />
              <Button type="submit" loading={decide.isPending} data-testid="decide-reimbursement">
                {t('billing.office.decideReimbursement')}
              </Button>
            </div>
          </form>
        </Card>
      ) : null}

      {payable ? (
        <Card>
          <h2 className="text-base font-semibold">
            {t('billing.office.recordReimbursementPayment')}
          </h2>
          <form
            onSubmit={onPay}
            className="mt-3 grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end"
            noValidate
            data-testid="reimbursement-payment"
          >
            <FormField
              label={t('billing.office.paymentReference')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="paymentReference"
                value={paymentReference}
                onChange={(e) => setPaymentReference(e.target.value)}
                className="font-mono"
                required
              />
            </FormField>
            <Button
              type="submit"
              loading={pay.isPending}
              disabled={paymentReference.trim() === ''}
              data-testid="pay-reimbursement"
            >
              {t('billing.office.recordReimbursementPayment')}
            </Button>
            <div className="sm:col-span-2">
              <ProblemAlert problem={pay.isError ? problemOf(pay.error) : null} />
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}
