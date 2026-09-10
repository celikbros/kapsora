import type { Batch } from '@kapsora/api-client';
import { useStepUp } from '@kapsora/auth';
import { formatDate, formatMoney, useTranslation } from '@kapsora/i18n';
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
  StepUpDialog,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  useToast,
} from '@kapsora/ui';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useProviderOrganizationId } from '../queries';
import {
  useBatch,
  useCreateBatch,
  useInvoices,
  usePutBatchInvoices,
  useSubmitBatch,
} from './queries';
import { batchTone, decisionTone, invoiceTone } from './status';

function monthStart(): string {
  const d = new Date();
  return new Date(Date.UTC(d.getFullYear(), d.getMonth(), 1)).toISOString().slice(0, 10);
}
function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/** A new batch: one payer, one domain, one currency, one period. */
export function BatchNewPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const providerId = useProviderOrganizationId();
  const create = useCreateBatch();
  const [form, setForm] = useState({
    periodFrom: monthStart(),
    periodTo: today(),
    domainCode: 'HEALTH',
    currencyCode: 'TRY',
  });
  const set = (key: keyof typeof form) => (value: string) =>
    setForm((f) => ({ ...f, [key]: value }));

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!providerId) return;
    create.mutate(
      {
        providerOrganizationId: providerId,
        periodFrom: form.periodFrom,
        periodTo: form.periodTo,
        domainCode: form.domainCode,
        currencyCode: form.currencyCode,
      },
      {
        onSuccess: (created) => {
          toast.notify({ tone: 'success', title: t('billing.provider.batchCreated') });
          void navigate({ to: '/billing/batches/$batchId', params: { batchId: created.data.id } });
        },
      },
    );
  }

  return (
    <div className="grid gap-4">
      <Breadcrumb
        items={[
          {
            label: t('billing.provider.batchesTitle'),
            render: (label) => <Link to="/billing/batches">{label}</Link>,
          },
          { label: t('billing.provider.newBatch') },
        ]}
      />
      <PageHeader title={t('billing.provider.newBatch')} />
      <Card>
        <form
          onSubmit={submit}
          className="grid gap-3 sm:grid-cols-2"
          noValidate
          data-testid="batch-form"
        >
          <FormField
            label={t('billing.provider.periodFrom')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              type="date"
              name="periodFrom"
              value={form.periodFrom}
              onChange={(e) => set('periodFrom')(e.target.value)}
              required
            />
          </FormField>
          <FormField
            label={t('billing.provider.periodTo')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              type="date"
              name="periodTo"
              value={form.periodTo}
              min={form.periodFrom}
              onChange={(e) => set('periodTo')(e.target.value)}
              required
            />
          </FormField>
          <FormField
            label={t('billing.provider.domain')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Select
              name="domainCode"
              value={form.domainCode}
              onChange={(e) => set('domainCode')(e.target.value)}
              options={[
                { value: 'HEALTH', label: t('nav.health') },
                { value: 'ACCOMMODATION', label: t('nav.lodging') },
              ]}
            />
          </FormField>
          <FormField
            label={t('billing.provider.currency')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Select
              name="currencyCode"
              value={form.currencyCode}
              onChange={(e) => set('currencyCode')(e.target.value)}
              options={[{ value: 'TRY', label: 'TRY' }]}
            />
          </FormField>
          <div className="sm:col-span-2">
            <ProblemAlert
              problem={create.isError ? problemOf(create.error) : null}
              className="mb-3"
            />
            <Button type="submit" loading={create.isPending} disabled={!providerId}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

/**
 * One batch: while a draft, its membership picked from the provider's submitted invoices and
 * submitted with a step-up; afterwards, each invoice's decision with its reason and the
 * totals line the server computed.
 */
export function BatchPage() {
  const { batchId } = useParams({ from: '/app/billing/batches/$batchId' });
  const { t } = useTranslation();
  const toast = useToast();
  const stepUp = useStepUp();
  const batch = useBatch(batchId);
  const record = batch.data?.data ?? null;
  const etag = batch.data?.etag ?? '';
  const put = usePutBatchInvoices(batchId);
  const submit = useSubmitBatch(batchId);
  const submitted = useInvoices({ status: 'SUBMITTED' });
  const [picked, setPicked] = useState<Set<string> | null>(null);

  if (batch.isPending) return <Spinner />;
  if (batch.isError) return <ProblemAlert problem={problemOf(batch.error)} />;
  if (!record) return null;
  const isDraft = record.status === 'DRAFT';
  const chosen = picked ?? new Set(record.invoices.map((i) => i.invoiceId));
  const candidates = [
    ...(submitted.data?.items ?? []).filter(
      (inv) =>
        inv.currencyCode === record.currencyCode &&
        !record.invoices.some((i) => i.invoiceId === inv.id),
    ),
  ];

  async function onSubmit() {
    try {
      const result = await stepUp.run(() => submit.mutateAsync(etag));
      if (result) toast.notify({ tone: 'success', title: t('billing.provider.batchSubmitted') });
    } catch {
      // The mutation keeps the problem; the alert above the buttons shows it.
    }
  }

  return (
    <div className="grid gap-4">
      <Breadcrumb
        items={[
          {
            label: t('billing.provider.batchesTitle'),
            render: (label) => <Link to="/billing/batches">{label}</Link>,
          },
          { label: record.reference },
        ]}
      />
      <PageHeader
        title={`${t('billing.provider.batch')} ${record.reference}`}
        actions={
          <Badge tone={batchTone(record.status)} data-testid="batch-status">
            {t(`billing.batchStatus.${record.status}`)}
          </Badge>
        }
      />
      <p className="text-fg-muted text-sm">
        {formatDate(record.periodFrom)} – {formatDate(record.periodTo)} · {record.currencyCode} ·{' '}
        {t('billing.provider.invoiceCount', { count: record.invoiceCount })}
      </p>

      {isDraft ? (
        <Card className="min-w-0">
          <h2 className="text-base font-semibold">{t('billing.provider.membership')}</h2>
          <p className="text-fg-muted mt-1 text-sm">{t('billing.provider.membershipHint')}</p>
          {record.invoices.length === 0 && candidates.length === 0 ? (
            <p className="text-fg-muted mt-3 text-sm">
              {t('billing.provider.noSubmittedInvoices')}
            </p>
          ) : (
            <ul className="mt-3 grid gap-1" data-testid="membership-list">
              {[
                ...record.invoices.map((i) => ({
                  id: i.invoiceId,
                  number: i.invoiceNumber,
                  date: i.invoiceDate,
                  amount: i.submittedAmount,
                })),
                ...candidates.map((i) => ({
                  id: i.id,
                  number: i.invoiceNumber,
                  date: i.invoiceDate,
                  amount: i.payableAmount,
                })),
              ].map((inv) => (
                <li key={inv.id} className="flex items-center gap-3 text-sm">
                  <input
                    type="checkbox"
                    id={`inv-${inv.id}`}
                    checked={chosen.has(inv.id)}
                    onChange={(e) => {
                      const next = new Set(chosen);
                      if (e.target.checked) next.add(inv.id);
                      else next.delete(inv.id);
                      setPicked(next);
                    }}
                  />
                  <label
                    htmlFor={`inv-${inv.id}`}
                    className="grid flex-1 grid-cols-[minmax(0,1fr)_6rem_8rem] items-baseline gap-3"
                  >
                    <span className="font-mono text-xs">{inv.number}</span>
                    <span className="text-fg-muted">{formatDate(inv.date)}</span>
                    <span className="text-right font-mono tabular-nums">
                      {formatMoney(inv.amount, record.currencyCode)}
                    </span>
                  </label>
                </li>
              ))}
            </ul>
          )}
          <ProblemAlert
            problem={
              put.isError ? problemOf(put.error) : submit.isError ? problemOf(submit.error) : null
            }
            className="mt-3"
          />
          <div className="mt-3 flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="secondary"
              onClick={() =>
                put.mutate(
                  { etag, body: { invoiceIds: Array.from(chosen) } },
                  {
                    onSuccess: () => {
                      setPicked(null);
                      toast.notify({ tone: 'success', title: t('billing.provider.saved') });
                    },
                  },
                )
              }
              loading={put.isPending}
            >
              {t('billing.provider.saveMembership')}
            </Button>
            <Button
              size="sm"
              onClick={() => void onSubmit()}
              loading={submit.isPending}
              disabled={record.invoiceCount === 0}
              data-testid="batch-submit"
            >
              {t('billing.provider.submitBatch')}
            </Button>
          </div>
          <StepUpDialog
            open={stepUp.required}
            action={t('billing.provider.submitBatch')}
            busy={stepUp.busy}
            problem={stepUp.error}
            onConfirm={(password) => void stepUp.confirm(password)}
            onCancel={stepUp.cancel}
          />
        </Card>
      ) : (
        <DecisionsCard record={record} />
      )}
    </div>
  );
}

function DecisionsCard({ record }: { record: Batch }) {
  const { t } = useTranslation();
  const decided =
    record.status === 'DECIDED' || record.status === 'SETTLING' || record.status === 'CLOSED';
  return (
    <Card className="min-w-0">
      <h2 className="text-base font-semibold">{t('billing.provider.decisions')}</h2>
      <div className="relative mt-3 overflow-x-auto">
        <Table data-testid="decision-table">
          <THead>
            <TR>
              <TH>{t('billing.provider.number')}</TH>
              <TH>{t('billing.provider.status')}</TH>
              <TH className="text-right">{t('billing.totals.submitted')}</TH>
              <TH>{t('billing.provider.decision')}</TH>
              <TH className="text-right">{t('billing.provider.approvedAmount')}</TH>
              <TH>{t('billing.provider.reason')}</TH>
            </TR>
          </THead>
          <TBody>
            {record.invoices.map((inv) => (
              <TR key={inv.id} data-testid="decision-row">
                <TD>
                  <Link
                    to="/billing/invoices/$invoiceId"
                    params={{ invoiceId: inv.invoiceId }}
                    search={{ claims: '' }}
                    className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                  >
                    {inv.invoiceNumber}
                  </Link>
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
                  <Badge tone={decisionTone(inv.decision)}>
                    {inv.decision
                      ? t(`billing.decision.${inv.decision}`)
                      : t('billing.decision.pending')}
                  </Badge>
                </TD>
                <TD className="text-right font-mono tabular-nums">
                  {inv.approvedAmount ? formatMoney(inv.approvedAmount, inv.currencyCode) : '—'}
                </TD>
                <TD className="text-sm">
                  {inv.reasonCode ? (
                    <>
                      <span>
                        {t(`billing.cutReasons.${inv.reasonCode}`, {
                          defaultValue: inv.reasonCode,
                        })}
                      </span>
                      {inv.reasonText ? (
                        <span className="text-fg-muted block text-xs">{inv.reasonText}</span>
                      ) : null}
                    </>
                  ) : (
                    '—'
                  )}
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </div>
      {decided ? (
        <dl
          className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm"
          data-testid="batch-totals"
        >
          <dt className="text-fg-muted">{t('billing.totals.submitted')}</dt>
          <dd className="font-mono tabular-nums">
            {formatMoney(record.submittedTotal, record.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.totals.approved')}</dt>
          <dd className="font-mono tabular-nums">
            {formatMoney(record.approvedTotal, record.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.totals.cut')}</dt>
          <dd className="font-mono tabular-nums">
            {formatMoney(record.cutTotal, record.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.totals.returned')}</dt>
          <dd className="font-mono tabular-nums">
            {formatMoney(record.returnedTotal, record.currencyCode)}
          </dd>
          <dt className="text-fg-muted">{t('billing.totals.rejected')}</dt>
          <dd className="font-mono tabular-nums">
            {formatMoney(record.rejectedTotal, record.currencyCode)}
          </dd>
        </dl>
      ) : null}
    </Card>
  );
}
