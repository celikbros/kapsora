import type { BatchDecision, BatchInvoice } from '@kapsora/api-client';
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
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  Textarea,
  useMinWidth,
  useToast,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { BillingNav } from './BillingNav';
import {
  useBatch,
  useBatchSummary,
  useDecideBatch,
  useInvoice,
  useReviewBatchInvoice,
} from './queries';
import { batchTone, decisionTone, invoiceTone } from './status';

const CUT_REASONS = [
  'TARIFF_EXCEEDED',
  'CONTRACT_TERMS',
  'NOT_COVERED',
  'DUPLICATE_SERVICE',
  'DOCUMENT_MISSING',
];

/**
 * One icmal in front of the payer: every invoice with the decision it has and the form for
 * the one it does not, the server's totals line beneath, and the close that the server
 * refuses to the submitter and, above the threshold, to the last decider.
 */
export function BatchReviewPage() {
  const { batchId } = useParams({ from: '/app/billing/batches/$batchId' });
  const { t } = useTranslation();
  const toast = useToast();
  const wide = useMinWidth(768);
  const batch = useBatch(batchId);
  const summary = useBatchSummary(batchId);
  const decide = useDecideBatch(batchId);
  const record = batch.data?.data ?? null;
  const etag = batch.data?.etag ?? '';
  const [open, setOpen] = useState<string | null>(null);

  if (batch.isPending) return <Spinner />;
  if (batch.isError) return <ProblemAlert problem={problemOf(batch.error)} />;
  if (!record) return null;
  const reviewable = record.status === 'SUBMITTED' || record.status === 'UNDER_REVIEW';
  const pending = record.invoices.filter((i) => !i.decision).length;
  const opened = open ? record.invoices.find((i) => i.invoiceId === open) : undefined;

  return (
    <div className="grid gap-4">
      <Breadcrumb
        items={[
          {
            label: t('billing.office.batchesTitle'),
            render: (label) => <Link to="/billing/batches">{label}</Link>,
          },
          { label: record.reference },
        ]}
      />
      <PageHeader
        title={record.reference}
        description={`${record.providerName ?? ''} · ${formatDate(record.periodFrom)} – ${formatDate(record.periodTo)} · ${record.currencyCode}`}
        actions={
          <Badge tone={batchTone(record.status)} data-testid="batch-status">
            {t(`billing.batchStatus.${record.status}`)}
          </Badge>
        }
      />
      <BillingNav />

      <Card className="min-w-0">
        <div className="flex flex-wrap items-baseline justify-between gap-3">
          <h2 className="text-base font-semibold">{t('billing.office.invoices')}</h2>
          {reviewable ? (
            <span className="text-fg-muted text-sm" data-testid="pending-count">
              {t('billing.office.pending', { count: pending })}
            </span>
          ) : null}
        </div>
        <p className="text-fg-muted mt-1 text-sm">{t('billing.office.decideHint')}</p>
        {!wide ? (
          <ul className="mt-3 grid gap-2" data-testid="review-table">
            {record.invoices.map((inv) => (
              <li
                key={inv.id}
                className="bg-surface-raised border-line rounded-lg border p-3"
                data-testid="review-row"
              >
                <div className="flex items-baseline justify-between gap-3">
                  <span className="font-mono text-xs">{inv.invoiceNumber}</span>
                  <Badge tone={invoiceTone(inv.invoiceStatus)}>
                    {t(`billing.invoiceStatus.${inv.invoiceStatus}`)}
                  </Badge>
                </div>
                <p className="text-fg-muted mt-1 flex justify-between text-sm">
                  <span>{formatDate(inv.invoiceDate)}</span>
                  <span className="text-fg font-mono tabular-nums">
                    {formatMoney(inv.submittedAmount, inv.currencyCode)}
                  </span>
                </p>
                <p className="mt-2 flex items-center justify-between gap-3 text-sm">
                  <Badge tone={decisionTone(inv.decision)} data-testid="row-decision">
                    {inv.decision
                      ? t(`billing.decision.${inv.decision}`)
                      : t('billing.decision.pending')}
                  </Badge>
                  <span className="font-mono tabular-nums">
                    {inv.approvedAmount ? formatMoney(inv.approvedAmount, inv.currencyCode) : '—'}
                  </span>
                </p>
                {inv.reasonCode ? (
                  <p className="text-fg-muted mt-1 text-xs">
                    {t(`billing.cutReasons.${inv.reasonCode}`, { defaultValue: inv.reasonCode })}
                    {inv.reasonText ? ` · ${inv.reasonText}` : ''}
                  </p>
                ) : null}
                {reviewable ? (
                  <div className="mt-2">
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => setOpen(open === inv.invoiceId ? null : inv.invoiceId)}
                      aria-expanded={open === inv.invoiceId}
                    >
                      {t('billing.office.decide')}
                    </Button>
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        ) : (
          <div className="relative mt-3 overflow-x-auto">
            <Table data-testid="review-table">
              <THead>
                <TR>
                  <TH>{t('billing.office.invoice')}</TH>
                  <TH>{t('billing.office.status')}</TH>
                  <TH className="text-right">{t('billing.office.submitted')}</TH>
                  <TH>{t('billing.office.decide')}</TH>
                  <TH className="text-right">{t('billing.office.approvedAmount')}</TH>
                  <TH>{t('billing.office.reasonCode')}</TH>
                  <TH>{t('billing.office.decidedBy')}</TH>
                  {reviewable ? <TH /> : null}
                </TR>
              </THead>
              <TBody>
                {record.invoices.map((inv) => (
                  <TR key={inv.id} data-testid="review-row">
                    <TD className="font-mono text-xs">
                      {inv.invoiceNumber}
                      <span className="text-fg-muted block font-sans">
                        {formatDate(inv.invoiceDate)}
                      </span>
                    </TD>
                    <TD>
                      <Badge tone={invoiceTone(inv.invoiceStatus)}>
                        {t(`billing.invoiceStatus.${inv.invoiceStatus}`)}
                      </Badge>
                    </TD>
                    <TD className="text-right font-mono tabular-nums">
                      {formatMoney(inv.submittedAmount, inv.currencyCode)}
                    </TD>
                    <TD>
                      <Badge tone={decisionTone(inv.decision)} data-testid="row-decision">
                        {inv.decision
                          ? t(`billing.decision.${inv.decision}`)
                          : t('billing.decision.pending')}
                      </Badge>
                    </TD>
                    <TD className="text-right font-mono tabular-nums">
                      {inv.approvedAmount ? formatMoney(inv.approvedAmount, inv.currencyCode) : '—'}
                    </TD>
                    <TD className="text-sm">
                      {inv.reasonCode
                        ? t(`billing.cutReasons.${inv.reasonCode}`, {
                            defaultValue: inv.reasonCode,
                          })
                        : '—'}
                      {inv.reasonText ? (
                        <span className="text-fg-muted block text-xs">{inv.reasonText}</span>
                      ) : null}
                    </TD>
                    <TD className="text-sm">{inv.decidedByDisplayName ?? '—'}</TD>
                    {reviewable ? (
                      <TD>
                        <Button
                          size="sm"
                          variant="secondary"
                          onClick={() => setOpen(open === inv.invoiceId ? null : inv.invoiceId)}
                          aria-expanded={open === inv.invoiceId}
                        >
                          {t('billing.office.decide')}
                        </Button>
                      </TD>
                    ) : null}
                  </TR>
                ))}
              </TBody>
            </Table>
          </div>
        )}
        {reviewable && opened ? (
          <DecisionForm
            key={opened.invoiceId}
            batchId={batchId}
            etag={etag}
            invoice={opened}
            onDone={() => setOpen(null)}
          />
        ) : null}
      </Card>

      <Card>
        <h2 className="text-base font-semibold">{t('billing.office.totalsLine')}</h2>
        <p className="text-fg-muted mt-1 text-xs">{t('billing.totals.arithmetic')}</p>
        {summary.data ? (
          <div className="mt-3 grid gap-4 md:grid-cols-2" data-testid="batch-totals">
            <dl className="max-w-sm grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm">
              <dt className="text-fg-muted">{t('billing.totals.submitted')}</dt>
              <dd className="text-right font-mono tabular-nums" data-testid="total-submitted">
                {formatMoney(summary.data.batch.submittedTotal, record.currencyCode)}
              </dd>
              <dt className="font-medium">{t('billing.totals.approved')}</dt>
              <dd
                className="text-right font-mono font-semibold tabular-nums"
                data-testid="total-approved"
              >
                {formatMoney(summary.data.batch.approvedTotal, record.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('billing.totals.cut')}</dt>
              <dd className="text-right font-mono tabular-nums" data-testid="total-cut">
                {formatMoney(summary.data.batch.cutTotal, record.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('billing.totals.returned')}</dt>
              <dd className="text-right font-mono tabular-nums" data-testid="total-returned">
                {formatMoney(summary.data.batch.returnedTotal, record.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('billing.totals.rejected')}</dt>
              <dd className="text-right font-mono tabular-nums" data-testid="total-rejected">
                {formatMoney(summary.data.batch.rejectedTotal, record.currencyCode)}
              </dd>
            </dl>
            {!wide ? (
              <ul className="grid gap-2" data-testid="decision-breakdown">
                {[
                  ...summary.data.decisions.map((row) => ({
                    key: row.decision,
                    label: t(`billing.decision.${row.decision}`),
                    count: row.count,
                    submitted: row.submittedTotal,
                    approved: row.approvedTotal as string | null,
                  })),
                  {
                    key: 'pending',
                    label: t('billing.decision.pending'),
                    count: summary.data.pendingCount,
                    submitted: summary.data.pendingTotal,
                    approved: null,
                  },
                ].map((row) => (
                  <li
                    key={row.key}
                    className="bg-surface-raised border-line rounded-lg border p-3 text-sm"
                    data-testid={`decision-${row.key === 'pending' ? 'PENDING' : row.key}`}
                  >
                    <div className="flex items-baseline justify-between gap-3">
                      <span>{row.label}</span>
                      <span className="text-fg-muted">
                        {t('billing.provider.invoiceCount', { count: row.count })}
                      </span>
                    </div>
                    <p className="text-fg-muted mt-1 flex justify-between">
                      <span>{t('billing.totals.submitted')}</span>
                      <span className="text-fg font-mono tabular-nums">
                        {formatMoney(row.submitted, record.currencyCode)}
                      </span>
                    </p>
                    {row.approved !== null ? (
                      <p className="text-fg-muted mt-1 flex justify-between">
                        <span>{t('billing.totals.approved')}</span>
                        <span className="text-fg font-mono tabular-nums">
                          {formatMoney(row.approved, record.currencyCode)}
                        </span>
                      </p>
                    ) : null}
                  </li>
                ))}
              </ul>
            ) : (
              <div className="relative overflow-x-auto">
                <Table>
                  <THead>
                    <TR>
                      <TH>{t('billing.office.decide')}</TH>
                      <TH className="text-right">{t('billing.office.invoices')}</TH>
                      <TH className="text-right">{t('billing.totals.submitted')}</TH>
                      <TH className="text-right">{t('billing.totals.approved')}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {summary.data.decisions.map((row) => (
                      <DecisionTotal key={row.decision} row={row} currency={record.currencyCode} />
                    ))}
                    <TR>
                      <TD>{t('billing.decision.pending')}</TD>
                      <TD className="text-right font-mono tabular-nums">
                        {summary.data.pendingCount}
                      </TD>
                      <TD className="text-right font-mono tabular-nums" data-testid="pending-total">
                        {formatMoney(summary.data.pendingTotal, record.currencyCode)}
                      </TD>
                      <TD />
                    </TR>
                  </TBody>
                </Table>
              </div>
            )}
          </div>
        ) : (
          <Spinner />
        )}
      </Card>

      {reviewable ? (
        <Card>
          <h2 className="text-base font-semibold">{t('billing.office.decideBatch')}</h2>
          <p className="text-fg-muted mt-1 text-sm">{t('billing.office.decideBatchHint')}</p>
          <ProblemAlert
            problem={decide.isError ? problemOf(decide.error) : null}
            className="mt-3"
          />
          {pending === 0 ? (
            <div className="mt-3">
              <Button
                onClick={() =>
                  decide.mutate(etag, {
                    onSuccess: () =>
                      toast.notify({ tone: 'success', title: t('billing.office.batchDecided') }),
                  })
                }
                loading={decide.isPending}
                data-testid="decide-batch"
              >
                {t('billing.office.decideBatch')}
              </Button>
            </div>
          ) : null}
        </Card>
      ) : record.decidedAt ? (
        <p className="text-fg-muted text-sm" data-testid="decided-line">
          {t('billing.office.decidedAt')}: {formatDateTime(record.decidedAt)}
        </p>
      ) : null}
    </div>
  );
}

function DecisionTotal({
  row,
  currency,
}: {
  row: { decision: BatchDecision; count: number; submittedTotal: string; approvedTotal: string };
  currency: string;
}) {
  const { t } = useTranslation();
  return (
    <TR data-testid={`decision-${row.decision}`}>
      <TD>{t(`billing.decision.${row.decision}`)}</TD>
      <TD className="text-right font-mono tabular-nums">{row.count}</TD>
      <TD className="text-right font-mono tabular-nums">
        {formatMoney(row.submittedTotal, currency)}
      </TD>
      <TD className="text-right font-mono tabular-nums">
        {formatMoney(row.approvedTotal, currency)}
      </TD>
    </TR>
  );
}

/** One decision for one invoice; the server checks the amount and the reason. */
function DecisionForm({
  batchId,
  etag,
  invoice,
  onDone,
}: {
  batchId: string;
  etag: string;
  invoice: BatchInvoice;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const review = useReviewBatchInvoice(batchId);
  const [decision, setDecision] = useState<BatchDecision>(invoice.decision ?? 'APPROVE');
  const [approvedAmount, setApprovedAmount] = useState(
    invoice.decision === 'CUT' ? (invoice.approvedAmount ?? '') : '',
  );
  const [reasonCode, setReasonCode] = useState(invoice.reasonCode ?? '');
  const [reasonText, setReasonText] = useState(invoice.reasonText ?? '');

  function submit(e: FormEvent) {
    e.preventDefault();
    review.mutate(
      {
        invoiceId: invoice.invoiceId,
        etag,
        body: {
          decision,
          ...(decision === 'CUT' ? { approvedAmount: approvedAmount.trim() } : {}),
          ...(decision !== 'APPROVE' ? { reasonCode: reasonCode.trim() } : {}),
          ...(reasonText.trim() ? { reasonText: reasonText.trim() } : {}),
        },
      },
      {
        onSuccess: () => {
          toast.notify({ tone: 'success', title: t('billing.office.decisionSaved') });
          onDone();
        },
      },
    );
  }

  return (
    <form
      onSubmit={submit}
      className="border-line mt-3 grid gap-3 border-t pt-3 md:grid-cols-4"
      noValidate
      data-testid="decision-form"
      aria-label={`${t('billing.office.decide')} ${invoice.invoiceNumber}`}
    >
      <p className="text-sm font-medium md:col-span-4" data-testid="deciding-for">
        {t('billing.office.decidingFor', { number: invoice.invoiceNumber })} ·{' '}
        <span className="font-mono tabular-nums">
          {formatMoney(invoice.submittedAmount, invoice.currencyCode)}
        </span>
      </p>
      <InvoiceClaims invoiceId={invoice.invoiceId} currency={invoice.currencyCode} />
      <FormField
        label={t('billing.office.decide')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Select
          name="decision"
          value={decision}
          onChange={(e) => setDecision(e.target.value as BatchDecision)}
          options={[
            { value: 'APPROVE', label: t('billing.office.approve') },
            { value: 'CUT', label: t('billing.office.cut') },
            { value: 'RETURN', label: t('billing.office.return') },
            { value: 'REJECT', label: t('billing.office.reject') },
          ]}
        />
      </FormField>
      {decision === 'CUT' ? (
        <FormField
          label={t('billing.office.approvedAmount')}
          required
          requiredLabel={t('common.requiredMark')}
          hint={`< ${formatMoney(invoice.submittedAmount, invoice.currencyCode)}`}
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
      {decision !== 'APPROVE' ? (
        <FormField
          label={t('billing.office.reasonCode')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          {decision === 'CUT' ? (
            <Select
              name="reasonCode"
              value={reasonCode}
              onChange={(e) => setReasonCode(e.target.value)}
              options={CUT_REASONS.map((c) => ({ value: c, label: t(`billing.cutReasons.${c}`) }))}
              placeholder="—"
            />
          ) : (
            <Input
              name="reasonCode"
              value={reasonCode}
              onChange={(e) => setReasonCode(e.target.value.toUpperCase())}
              className="font-mono"
              required
            />
          )}
        </FormField>
      ) : null}
      <div className="md:col-span-4">
        <FormField label={t('billing.office.reasonText')}>
          <Textarea
            name="reasonText"
            rows={2}
            value={reasonText}
            onChange={(e) => setReasonText(e.target.value)}
          />
        </FormField>
      </div>
      <div className="md:col-span-4">
        <ProblemAlert problem={review.isError ? problemOf(review.error) : null} className="mb-3" />
        <div className="flex gap-2">
          <Button type="submit" size="sm" loading={review.isPending} data-testid="save-decision">
            {t('billing.office.saveDecision')}
          </Button>
          <Button type="button" size="sm" variant="secondary" onClick={onDone}>
            {t('common.cancel')}
          </Button>
        </div>
      </div>
    </form>
  );
}

/**
 * The claims the invoice carries, each opening the claim itself — which answers with the
 * projection the reader holds, so a financial reviewer sees the lines and the sponsor's HR
 * never sees a line description.
 */
function InvoiceClaims({ invoiceId, currency }: { invoiceId: string; currency: string }) {
  const { t } = useTranslation();
  const invoice = useInvoice(invoiceId);
  if (invoice.isPending) {
    return (
      <p className="text-fg-muted flex items-center gap-2 text-sm md:col-span-4" aria-busy="true">
        <Spinner /> {t('billing.report.claimsLoading')}
      </p>
    );
  }
  if (invoice.isError) {
    return (
      <div className="md:col-span-4">
        <ProblemAlert problem={problemOf(invoice.error)} />
      </div>
    );
  }
  const allocations = invoice.data.data.allocations;
  return (
    <div className="md:col-span-4" data-testid="invoice-claims">
      <h3 className="text-sm font-semibold">{t('billing.report.claimsOnInvoice')}</h3>
      <ul className="mt-1 grid max-w-lg gap-1 text-sm">
        {allocations.map((a) => (
          <li
            key={a.claimId}
            className="grid grid-cols-[minmax(0,1fr)_9rem] items-baseline gap-x-3"
            data-testid="invoice-claim"
          >
            <Link
              to="/claims/$claimId"
              params={{ claimId: a.claimId }}
              className="text-primary font-mono text-xs underline-offset-4 hover:underline"
            >
              {a.claimReference}
            </Link>
            <span className="text-right font-mono tabular-nums">
              {formatMoney(a.allocatedAmount, currency)}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
