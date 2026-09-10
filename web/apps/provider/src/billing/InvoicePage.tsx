import type { CreateInvoice, Invoice, PatchInvoiceDraft } from '@kapsora/api-client';
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
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { DocumentsPanel, useLinkedDocuments } from '../documents';
import { problemOf } from '../problems';
import { useProviderOrganizationId } from '../queries';
import {
  useCancelInvoice,
  useCreateInvoice,
  useEarnings,
  useInvoice,
  useInvoiceChain,
  usePatchInvoice,
  usePutAllocations,
  useSubmitInvoice,
} from './queries';
import { invoiceTone } from './status';
import { useClaimSummaries } from './claims';

interface HeaderForm {
  invoiceNumber: string;
  invoiceDate: string;
  lineExtensionAmount: string;
  taxAmount: string;
  payableAmount: string;
  vatRate: string;
  notes: string;
}

function headerOf(record: Invoice | null): HeaderForm {
  return {
    invoiceNumber: record?.invoiceNumber ?? '',
    invoiceDate: record?.invoiceDate ?? new Date().toISOString().slice(0, 10),
    lineExtensionAmount: record?.lineExtensionAmount ?? '',
    taxAmount: record?.taxAmount ?? '',
    payableAmount: record?.payableAmount ?? '',
    vatRate: record?.vatRate ?? '',
    notes: record?.notes ?? '',
  };
}

/**
 * The invoice as the provider enters it: the header as typed, the image, the allocation to
 * approved claims with the server's running total and difference beside the payable
 * amount, and the submit that the server gates. A returned invoice offers Düzelt, which
 * opens the correction with the old figures and leaves the old one in the chain.
 */
export function InvoiceNewPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const search = useSearch({ from: '/app/billing/invoices/new' });
  const providerId = useProviderOrganizationId();
  const create = useCreateInvoice();
  const [form, setForm] = useState<HeaderForm>(headerOf(null));
  const set = (key: keyof HeaderForm) => (value: string) =>
    setForm((f) => ({ ...f, [key]: value }));

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!providerId) return;
    const body: CreateInvoice = {
      providerOrganizationId: providerId,
      invoiceNumber: form.invoiceNumber.trim(),
      invoiceDate: form.invoiceDate,
      lineExtensionAmount: form.lineExtensionAmount.trim(),
      taxAmount: form.taxAmount.trim(),
      payableAmount: form.payableAmount.trim(),
      currencyCode: search.currency ?? 'TRY',
      ...(form.vatRate.trim() ? { vatRate: form.vatRate.trim() } : {}),
      ...(form.notes.trim() ? { notes: form.notes.trim() } : {}),
      ...(search.supersedes ? { supersedesInvoiceId: search.supersedes } : {}),
    };
    create.mutate(body, {
      onSuccess: (created) => {
        toast.notify({ tone: 'success', title: t('billing.provider.created') });
        void navigate({
          to: '/billing/invoices/$invoiceId',
          params: { invoiceId: created.data.id },
          search: { claims: search.claims ?? '' },
        });
      },
    });
  }

  return (
    <div className="grid gap-4">
      <Breadcrumb
        items={[
          {
            label: t('billing.provider.invoicesTitle'),
            render: (label) => <Link to="/billing/invoices">{label}</Link>,
          },
          { label: t('billing.provider.newInvoice') },
        ]}
      />
      <PageHeader title={t('billing.provider.newInvoice')} />
      <Card>
        <HeaderFields form={form} set={set} onSubmit={submit} />
        <ProblemAlert problem={create.isError ? problemOf(create.error) : null} className="mt-3" />
        <div className="mt-3">
          <Button
            onClick={(e) => submit(e as unknown as FormEvent)}
            loading={create.isPending}
            disabled={
              !providerId || form.invoiceNumber.trim() === '' || form.payableAmount.trim() === ''
            }
          >
            {t('common.save')}
          </Button>
        </div>
      </Card>
    </div>
  );
}

function HeaderFields({
  form,
  set,
  onSubmit,
  disabled = false,
}: {
  form: HeaderForm;
  set: (key: keyof HeaderForm) => (value: string) => void;
  onSubmit: (e: FormEvent) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <form
      onSubmit={onSubmit}
      className="grid gap-3 md:grid-cols-3"
      noValidate
      data-testid="invoice-header"
    >
      <FormField
        label={t('billing.provider.number')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="invoiceNumber"
          value={form.invoiceNumber}
          onChange={(e) => set('invoiceNumber')(e.target.value)}
          className="font-mono"
          disabled={disabled}
          required
        />
      </FormField>
      <FormField
        label={t('billing.provider.date')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          type="date"
          name="invoiceDate"
          value={form.invoiceDate}
          onChange={(e) => set('invoiceDate')(e.target.value)}
          disabled={disabled}
          required
        />
      </FormField>
      <FormField label={t('billing.provider.vatRate')}>
        <Input
          name="vatRate"
          inputMode="decimal"
          value={form.vatRate}
          onChange={(e) => set('vatRate')(e.target.value)}
          className="font-mono"
          disabled={disabled}
        />
      </FormField>
      <FormField
        label={t('billing.provider.lineExtension')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="lineExtensionAmount"
          inputMode="decimal"
          value={form.lineExtensionAmount}
          onChange={(e) => set('lineExtensionAmount')(e.target.value)}
          className="font-mono text-right"
          disabled={disabled}
          required
        />
      </FormField>
      <FormField
        label={t('billing.provider.tax')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="taxAmount"
          inputMode="decimal"
          value={form.taxAmount}
          onChange={(e) => set('taxAmount')(e.target.value)}
          className="font-mono text-right"
          disabled={disabled}
          required
        />
      </FormField>
      <FormField
        label={t('billing.provider.payable')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="payableAmount"
          inputMode="decimal"
          value={form.payableAmount}
          onChange={(e) => set('payableAmount')(e.target.value)}
          className="font-mono text-right"
          disabled={disabled}
          required
        />
      </FormField>
      <div className="md:col-span-3">
        <FormField label={t('billing.provider.notes')}>
          <Textarea
            name="notes"
            rows={2}
            value={form.notes}
            onChange={(e) => set('notes')(e.target.value)}
            disabled={disabled}
          />
        </FormField>
      </div>
    </form>
  );
}

export function InvoicePage() {
  const { invoiceId } = useParams({ from: '/app/billing/invoices/$invoiceId' });
  const search = useSearch({ from: '/app/billing/invoices/$invoiceId' });
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const invoice = useInvoice(invoiceId);
  const chain = useInvoiceChain(invoiceId);
  const patch = usePatchInvoice(invoiceId);
  const putAllocations = usePutAllocations(invoiceId);
  const submit = useSubmitInvoice(invoiceId);
  const cancel = useCancelInvoice(invoiceId);
  const create = useCreateInvoice();
  const record = invoice.data?.data ?? null;
  const etag = invoice.data?.etag ?? '';
  const providerId = useProviderOrganizationId();
  const documents = useLinkedDocuments('INVOICE', invoiceId);
  const [form, setForm] = useState<HeaderForm | null>(null);
  const opened = form ?? headerOf(record);
  const set = (key: keyof HeaderForm) => (value: string) => setForm({ ...opened, [key]: value });

  if (invoice.isPending) return <Spinner />;
  if (invoice.isError) return <ProblemAlert problem={problemOf(invoice.error)} />;
  if (!record) return null;
  const isDraft = record.status === 'DRAFT';
  const cleanImage = (documents.data?.items ?? []).find((d) => d.scanStatus === 'CLEAN');

  function saveHeader(e: FormEvent) {
    e.preventDefault();
    if (!record) return;
    const body: PatchInvoiceDraft = {
      invoiceNumber: opened.invoiceNumber.trim(),
      invoiceDate: opened.invoiceDate,
      lineExtensionAmount: opened.lineExtensionAmount.trim(),
      taxAmount: opened.taxAmount.trim(),
      payableAmount: opened.payableAmount.trim(),
      notes: opened.notes.trim() || null,
      ...(opened.vatRate.trim() ? { vatRate: opened.vatRate.trim() } : {}),
    };
    patch.mutate(
      { etag, body },
      {
        onSuccess: () => {
          setForm(null);
          toast.notify({ tone: 'success', title: t('billing.provider.saved') });
        },
      },
    );
  }

  function attachImage() {
    if (!cleanImage) return;
    patch.mutate(
      { etag, body: { documentId: cleanImage.id } },
      { onSuccess: () => toast.notify({ tone: 'success', title: t('billing.provider.saved') }) },
    );
  }

  function correct() {
    if (!record || !providerId) return;
    create.mutate(
      {
        providerOrganizationId: providerId,
        invoiceNumber: record.invoiceNumber,
        invoiceDate: record.invoiceDate,
        lineExtensionAmount: record.lineExtensionAmount,
        taxAmount: record.taxAmount,
        payableAmount: record.payableAmount,
        ...(record.vatRate ? { vatRate: record.vatRate } : {}),
        currencyCode: record.currencyCode,
        supersedesInvoiceId: record.id,
      },
      {
        onSuccess: (created) =>
          void navigate({
            to: '/billing/invoices/$invoiceId',
            params: { invoiceId: created.data.id },
            search: { claims: '' },
          }),
      },
    );
  }

  return (
    <div className="grid gap-4">
      <Breadcrumb
        items={[
          {
            label: t('billing.provider.invoicesTitle'),
            render: (label) => <Link to="/billing/invoices">{label}</Link>,
          },
          { label: record.invoiceNumber },
        ]}
      />
      <PageHeader
        title={`${t('billing.provider.invoice')} ${record.invoiceNumber}`}
        actions={
          <Badge tone={invoiceTone(record.status)} data-testid="invoice-status">
            {t(`billing.invoiceStatus.${record.status}`)}
          </Badge>
        }
      />

      <Card>
        <h2 className="text-base font-semibold">{t('billing.provider.invoice')}</h2>
        <div className="mt-3">
          <HeaderFields form={opened} set={set} onSubmit={saveHeader} disabled={!isDraft} />
        </div>
        <ProblemAlert problem={patch.isError ? problemOf(patch.error) : null} className="mt-3" />
        {isDraft ? (
          <div className="mt-3">
            <Button
              size="sm"
              variant="secondary"
              onClick={(e) => saveHeader(e as unknown as FormEvent)}
              loading={patch.isPending}
            >
              {t('billing.provider.saveHeader')}
            </Button>
          </div>
        ) : null}
      </Card>

      <Card>
        <h2 className="text-base font-semibold">{t('billing.provider.image')}</h2>
        <p className="text-fg-muted mt-1 text-sm">{t('billing.provider.imageHint')}</p>
        <div className="mt-3">
          <DocumentsPanel
            aggregateType="INVOICE"
            aggregateId={record.id}
            requiredTypes={['INVOICE']}
            readOnly={!isDraft}
          />
        </div>
        {isDraft && cleanImage && record.documentId !== cleanImage.id ? (
          <div className="mt-2">
            <Button size="sm" variant="secondary" onClick={attachImage} loading={patch.isPending}>
              {t('common.save')}
            </Button>
          </div>
        ) : null}
      </Card>

      <AllocationsCard
        record={record}
        etag={etag}
        editable={isDraft}
        candidateIds={(search.claims ?? '').split(',').filter(Boolean)}
        onSave={(allocations) =>
          putAllocations.mutate(
            { etag, body: { allocations } },
            {
              onSuccess: () =>
                toast.notify({ tone: 'success', title: t('billing.provider.saved') }),
            },
          )
        }
        saving={putAllocations.isPending}
        problem={putAllocations.isError ? problemOf(putAllocations.error) : null}
      />

      <Card>
        <ProblemAlert
          problem={
            submit.isError
              ? problemOf(submit.error)
              : cancel.isError
                ? problemOf(cancel.error)
                : create.isError
                  ? problemOf(create.error)
                  : null
          }
          className="mb-3"
        />
        <div className="flex flex-wrap gap-2">
          {isDraft ? (
            <Button
              onClick={() =>
                submit.mutate(etag, {
                  onSuccess: () =>
                    toast.notify({ tone: 'success', title: t('billing.provider.submitted') }),
                })
              }
              loading={submit.isPending}
              data-testid="invoice-submit"
            >
              {t('billing.provider.submit')}
            </Button>
          ) : null}
          {record.status === 'RETURNED' ? (
            <Button onClick={correct} loading={create.isPending} data-testid="invoice-correct">
              {t('billing.provider.correct')}
            </Button>
          ) : null}
          {isDraft || record.status === 'RETURNED' ? (
            <Button
              variant="secondary"
              onClick={() =>
                cancel.mutate(etag, {
                  onSuccess: () =>
                    toast.notify({ tone: 'info', title: t('billing.provider.cancelled') }),
                })
              }
              loading={cancel.isPending}
            >
              {t('billing.provider.cancel')}
            </Button>
          ) : null}
        </div>
        {record.status === 'RETURNED' ? (
          <p className="text-fg-muted mt-2 text-sm">{t('billing.provider.correctHint')}</p>
        ) : null}
      </Card>

      {chain.data && chain.data.length > 1 ? (
        <Card>
          <h2 className="text-base font-semibold">{t('billing.provider.chain')}</h2>
          <ol className="mt-2 grid gap-1 text-sm" data-testid="invoice-chain">
            {chain.data.map((item) => (
              <li key={item.id} className="flex flex-wrap items-baseline gap-x-3">
                <Link
                  to="/billing/invoices/$invoiceId"
                  params={{ invoiceId: item.id }}
                  search={{ claims: '' }}
                  className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                >
                  {item.invoiceNumber}
                </Link>
                <span className="text-fg-muted">{formatDate(item.invoiceDate)}</span>
                <Badge tone={invoiceTone(item.status)}>
                  {t(`billing.invoiceStatus.${item.status}`)}
                </Badge>
              </li>
            ))}
          </ol>
        </Card>
      ) : null}
    </div>
  );
}

/**
 * The allocation table: the invoiceable claims the earnings view named plus whatever this
 * invoice already carries, one amount per claim, and the server's running total and
 * difference beside the payable amount after every save.
 */
function AllocationsCard({
  record,
  etag,
  editable,
  candidateIds,
  onSave,
  saving,
  problem,
}: {
  record: Invoice;
  etag: string;
  editable: boolean;
  candidateIds: string[];
  onSave: (allocations: { claimId: string; allocatedAmount: string }[]) => void;
  saving: boolean;
  problem: ReturnType<typeof problemOf> | null;
}) {
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const providerId = useProviderOrganizationId();
  const earnings = useEarnings(editable ? providerId : null, { currency: record.currencyCode });
  const invoiceable =
    earnings.data?.currencies.find((c) => c.currencyCode === record.currencyCode)
      ?.invoiceableClaimIds ?? [];
  const ids = Array.from(
    new Set([...record.allocations.map((a) => a.claimId), ...candidateIds, ...invoiceable]),
  );
  const summaries = useClaimSummaries(
    ids.filter((id) => !record.allocations.some((a) => a.claimId === id)),
  );
  const [amounts, setAmounts] = useState<Record<string, string>>(() =>
    Object.fromEntries(record.allocations.map((a) => [a.claimId, a.allocatedAmount])),
  );
  void etag;

  const rows = ids.map((id) => {
    const allocated = record.allocations.find((a) => a.claimId === id);
    const summary = summaries.get(id);
    return {
      claimId: id,
      reference: allocated?.claimReference ?? summary?.reference ?? '…',
      approvedTotal: allocated?.approvedTotal ?? summary?.approvedTotal ?? null,
      status: allocated?.claimStatus ?? summary?.status ?? null,
      amount: amounts[id] ?? '',
    };
  });

  return (
    <Card className="min-w-0">
      <h2 className="text-base font-semibold">{t('billing.provider.allocations')}</h2>
      <p className="text-fg-muted mt-1 text-sm">{t('billing.provider.pickHint')}</p>
      {rows.length === 0 ? (
        <p className="text-fg-muted mt-3 text-sm">{t('billing.provider.noInvoiceable')}</p>
      ) : !wide ? (
        <ul className="mt-3 grid gap-2" data-testid="allocation-table">
          {rows.map((row) => (
            <li
              key={row.claimId}
              className="bg-surface-raised border-line rounded-lg border p-3"
              data-testid="allocation-row"
            >
              <div className="flex items-baseline justify-between gap-3">
                <span className="font-mono text-xs">{row.reference}</span>
                <span className="text-fg-muted text-sm">
                  {row.status ? t(`claims.status.${row.status}`) : '…'}
                </span>
              </div>
              <p className="text-fg-muted mt-1 flex justify-between text-sm">
                <span>{t('billing.provider.approvedTotal')}</span>
                <span className="text-fg font-mono tabular-nums">
                  {row.approvedTotal === null
                    ? '…'
                    : formatMoney(row.approvedTotal, record.currencyCode)}
                </span>
              </p>
              {editable ? (
                <div className="mt-2">
                  <Input
                    inputMode="decimal"
                    value={row.amount}
                    onChange={(e) => setAmounts((a) => ({ ...a, [row.claimId]: e.target.value }))}
                    aria-label={`${t('billing.provider.allocation')} ${row.reference}`}
                    className="text-right font-mono"
                  />
                </div>
              ) : (
                <p className="mt-1 flex justify-between text-sm">
                  <span className="text-fg-muted">{t('billing.provider.allocation')}</span>
                  <span className="font-mono tabular-nums">
                    {row.amount ? formatMoney(row.amount, record.currencyCode) : '—'}
                  </span>
                </p>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <div className="relative mt-3 overflow-x-auto">
          <Table data-testid="allocation-table">
            <THead>
              <TR>
                <TH>{t('claims.title')}</TH>
                <TH>{t('billing.provider.status')}</TH>
                <TH className="text-right">{t('billing.provider.approvedTotal')}</TH>
                <TH className="text-right">{t('billing.provider.allocation')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((row) => (
                <TR key={row.claimId} data-testid="allocation-row">
                  <TD className="font-mono text-xs">{row.reference}</TD>
                  <TD>{row.status ? t(`claims.status.${row.status}`) : '…'}</TD>
                  <TD className="text-right font-mono tabular-nums">
                    {row.approvedTotal === null
                      ? '…'
                      : formatMoney(row.approvedTotal, record.currencyCode)}
                  </TD>
                  <TD className="text-right">
                    {editable ? (
                      <Input
                        inputMode="decimal"
                        value={row.amount}
                        onChange={(e) =>
                          setAmounts((a) => ({ ...a, [row.claimId]: e.target.value }))
                        }
                        aria-label={`${t('billing.provider.allocation')} ${row.reference}`}
                        className="w-32 text-right font-mono"
                      />
                    ) : (
                      <span className="font-mono tabular-nums">
                        {row.amount ? formatMoney(row.amount, record.currencyCode) : '—'}
                      </span>
                    )}
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
      <dl
        className="mt-3 max-w-sm grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm"
        data-testid="allocation-totals"
      >
        <dt className="text-fg-muted">{t('billing.provider.payable')}</dt>
        <dd className="text-right font-mono tabular-nums">
          {formatMoney(record.payableAmount, record.currencyCode)}
        </dd>
        <dt className="text-fg-muted">{t('billing.provider.allocationTotal')}</dt>
        <dd className="text-right font-mono tabular-nums">
          {formatMoney(record.allocationTotal, record.currencyCode)}
        </dd>
        <dt className="text-fg-muted">{t('billing.provider.difference')}</dt>
        <dd className="text-right font-mono tabular-nums" data-testid="allocation-difference">
          {formatMoney(record.allocationDifference, record.currencyCode)}
        </dd>
      </dl>
      <p className="text-fg-muted mt-1 text-xs">{t('billing.provider.differenceHint')}</p>
      <ProblemAlert problem={problem} className="mt-3" />
      {editable ? (
        <div className="mt-3">
          <Button
            size="sm"
            variant="secondary"
            onClick={() =>
              onSave(
                rows
                  .filter((r) => r.amount.trim() !== '')
                  .map((r) => ({ claimId: r.claimId, allocatedAmount: r.amount.trim() })),
              )
            }
            loading={saving}
          >
            {t('billing.provider.saveAllocations')}
          </Button>
        </div>
      ) : null}
    </Card>
  );
}
