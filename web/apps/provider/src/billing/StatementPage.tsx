import type { BatchDecision, BatchStatus, Export } from '@kapsora/api-client';
import { formatDate, formatDateTime, formatMoney, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Card,
  FormField,
  HelpHint,
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
  useMinWidth,
  useToast,
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { useState } from 'react';

import { problemOf } from '../problems';
import { useProviderOrganizationId } from '../queries';
import {
  useCreateStatementExport,
  useDownloadStatementExport,
  useStatement,
  useStatementExport,
} from './queries';
import { batchTone, decisionTone, invoiceTone, settlementTone } from './status';

function monthStart(): string {
  const d = new Date();
  return new Date(Date.UTC(d.getFullYear(), d.getMonth(), 1)).toISOString().slice(0, 10);
}
function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/**
 * Cari ekstre: what the provider invoiced in a period, what was decided on it, what was
 * settled and what was paid — the figures a provider disputes from, so every one is the
 * database's to the kuruş and the screen adds nothing. "Dışa aktar" queues the same
 * statement as a watermarked file and shows its state until it can be downloaded.
 */
export function StatementPage() {
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const providerId = useProviderOrganizationId();
  const [periodFrom, setFrom] = useState(monthStart);
  const [periodTo, setTo] = useState(today);
  const statement = useStatement(providerId, { periodFrom, periodTo });
  const data = statement.data;

  return (
    <div className="grid gap-4">
      <PageHeader
        title={t('billing.report.statementTitle')}
        actions={
          <Link
            to="/billing"
            className="text-primary self-center text-sm underline-offset-4 hover:underline"
          >
            {t('billing.provider.earningsTitle')}
          </Link>
        }
      />
      <p className="text-fg-muted text-sm">{t('billing.report.statementIntro')}</p>
      <Card>
        <div className="grid gap-3 sm:grid-cols-2">
          <FormField label={t('billing.report.periodFrom')}>
            <Input
              type="date"
              name="periodFrom"
              value={periodFrom}
              onChange={(e) => setFrom(e.target.value)}
            />
          </FormField>
          <FormField label={t('billing.report.periodTo')}>
            <Input
              type="date"
              name="periodTo"
              value={periodTo}
              min={periodFrom}
              onChange={(e) => setTo(e.target.value)}
            />
          </FormField>
        </div>
        {providerId ? (
          <StatementExport providerId={providerId} periodFrom={periodFrom} periodTo={periodTo} />
        ) : null}
      </Card>

      {statement.isPending && providerId ? (
        <p className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('billing.report.statementLoading')}
        </p>
      ) : statement.isError ? (
        <ProblemAlert problem={problemOf(statement.error)} />
      ) : !data ? null : (
        <>
          <Card>
            <h2 className="text-base font-semibold">
              {data.providerName} · {formatDate(data.periodFrom)} – {formatDate(data.periodTo)} ·{' '}
              {data.currencyCode}
            </h2>
            <dl
              className="mt-3 grid max-w-sm grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm"
              data-testid="statement-totals"
            >
              <dt className="text-fg-muted">{t('billing.report.totals.invoiced')}</dt>
              <dd className="text-right font-mono tabular-nums" data-testid="total-invoiced">
                {formatMoney(data.totals.invoicedTotal, data.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('billing.report.totals.approved')}</dt>
              <dd className="text-right font-mono tabular-nums">
                {formatMoney(data.totals.approvedTotal, data.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('billing.report.totals.cut')}</dt>
              <dd className="text-right font-mono tabular-nums">
                {formatMoney(data.totals.cutTotal, data.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('billing.report.totals.returned')}</dt>
              <dd className="text-right font-mono tabular-nums">
                {formatMoney(data.totals.returnedTotal, data.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('billing.report.totals.rejected')}</dt>
              <dd className="text-right font-mono tabular-nums">
                {formatMoney(data.totals.rejectedTotal, data.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('billing.report.totals.settled')}</dt>
              <dd className="text-right font-mono tabular-nums">
                {formatMoney(data.totals.settledTotal, data.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('billing.report.totals.paid')}</dt>
              <dd className="text-right font-mono tabular-nums">
                {formatMoney(data.totals.paidTotal, data.currencyCode)}
              </dd>
              <dt className="font-medium">{t('billing.report.totals.open')}</dt>
              <dd
                className="text-right font-mono text-base font-semibold tabular-nums"
                data-testid="total-open"
              >
                {formatMoney(data.totals.openBalance, data.currencyCode)}
              </dd>
            </dl>
            <p className="text-fg-muted mt-2 text-xs">
              {t('billing.report.totals.invoiceCount', { count: data.totals.invoiceCount })} ·{' '}
              {t('billing.report.totals.settlementCount', {
                count: data.totals.settlementCount,
              })}{' '}
              · {t('billing.totals.arithmetic')}
            </p>
          </Card>

          <Card className="min-w-0">
            <h2 className="text-base font-semibold">{t('billing.report.invoices')}</h2>
            {data.invoices.length === 0 ? (
              <p className="text-fg-muted mt-2 text-sm">{t('billing.report.statementEmpty')}</p>
            ) : !wide ? (
              <ul className="mt-3 grid gap-2" data-testid="statement-invoices">
                {data.invoices.map((inv) => (
                  <li
                    key={inv.id}
                    className="bg-surface-raised border-line rounded-lg border p-3"
                    data-testid="statement-invoice"
                  >
                    <div className="flex items-baseline justify-between gap-3">
                      <Link
                        to="/billing/invoices/$invoiceId"
                        params={{ invoiceId: inv.id }}
                        search={{ claims: '' }}
                        className="text-primary font-mono text-xs underline-offset-4 hover:underline"
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
                    <p className="text-fg-muted mt-1 flex items-center justify-between gap-3 text-sm">
                      <span className="flex items-center gap-2">
                        {t('billing.totals.approved')}
                        <Badge tone={decisionTone(inv.batchDecision as BatchDecision | null)}>
                          {inv.batchDecision
                            ? t(`billing.decision.${inv.batchDecision}`)
                            : t('billing.decision.pending')}
                        </Badge>
                      </span>
                      <span className="text-fg font-mono tabular-nums">
                        {inv.batchDecision
                          ? formatMoney(inv.approvedAmount, inv.currencyCode)
                          : '—'}
                      </span>
                    </p>
                  </li>
                ))}
              </ul>
            ) : (
              <div className="relative mt-3 overflow-x-auto">
                <Table data-testid="statement-invoices">
                  <THead>
                    <TR>
                      <TH>{t('billing.report.invoice')}</TH>
                      <TH>{t('billing.provider.status')}</TH>
                      <TH className="text-right whitespace-nowrap">
                        {t('billing.report.payable')}
                      </TH>
                      <TH>{t('billing.report.batch')}</TH>
                      <TH>{t('billing.report.decision')}</TH>
                      <TH className="text-right">{t('billing.totals.approved')}</TH>
                      <TH>{t('billing.report.settlement')}</TH>
                      <TH className="text-right">{t('billing.report.paid')}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {data.invoices.map((inv) => (
                      <TR key={inv.id} data-testid="statement-invoice">
                        <TD className="whitespace-nowrap">
                          <Link
                            to="/billing/invoices/$invoiceId"
                            params={{ invoiceId: inv.id }}
                            search={{ claims: '' }}
                            className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                          >
                            {inv.invoiceNumber}
                          </Link>
                          <span className="text-fg-muted block text-xs">
                            {formatDate(inv.invoiceDate)}
                          </span>
                        </TD>
                        <TD>
                          <Badge tone={invoiceTone(inv.status)}>
                            {t(`billing.invoiceStatus.${inv.status}`)}
                          </Badge>
                        </TD>
                        <TD className="text-right font-mono whitespace-nowrap tabular-nums">
                          {formatMoney(inv.payableAmount, inv.currencyCode)}
                        </TD>
                        <TD className="whitespace-nowrap">
                          {inv.batchId ? (
                            <Link
                              to="/billing/batches/$batchId"
                              params={{ batchId: inv.batchId }}
                              className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                            >
                              {inv.batchReference}
                            </Link>
                          ) : (
                            '—'
                          )}
                          {inv.batchStatus ? (
                            <span className="block">
                              <Badge tone={batchTone(inv.batchStatus as BatchStatus)}>
                                {t(`billing.batchStatus.${inv.batchStatus}`)}
                              </Badge>
                            </span>
                          ) : null}
                        </TD>
                        <TD>
                          <Badge tone={decisionTone(inv.batchDecision as BatchDecision | null)}>
                            {inv.batchDecision
                              ? t(`billing.decision.${inv.batchDecision}`)
                              : t('billing.decision.pending')}
                          </Badge>
                        </TD>
                        <TD className="text-right font-mono whitespace-nowrap tabular-nums">
                          {inv.batchDecision
                            ? formatMoney(inv.approvedAmount, inv.currencyCode)
                            : '—'}
                        </TD>
                        <TD className="font-mono text-xs whitespace-nowrap">
                          {inv.settlementReference ?? '—'}
                          {inv.dueDate ? (
                            <span className="text-fg-muted block font-sans">
                              {t('billing.report.due')} {formatDate(inv.dueDate)}
                            </span>
                          ) : null}
                        </TD>
                        <TD className="text-right font-mono whitespace-nowrap tabular-nums">
                          {formatMoney(inv.settlementPaidAmount, inv.currencyCode)}
                        </TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </div>
            )}
          </Card>

          <Card className="min-w-0">
            {/* The mark sits beside the heading, not inside it: it is not part of the name. */}
            <div className="flex items-baseline gap-1.5">
              <h2 className="text-base font-semibold">{t('billing.report.settlements')}</h2>
              <HelpHint term="mutabakat" />
            </div>
            {data.settlements.length === 0 ? (
              <p className="text-fg-muted mt-2 text-sm">{t('billing.report.statementEmpty')}</p>
            ) : !wide ? (
              <ul className="mt-3 grid gap-2" data-testid="statement-settlements">
                {data.settlements.map((s) => (
                  <li
                    key={s.id}
                    className="bg-surface-raised border-line rounded-lg border p-3"
                    data-testid="statement-settlement"
                  >
                    <div className="flex items-baseline justify-between gap-3">
                      <span className="font-mono text-xs">{s.reference}</span>
                      <Badge tone={settlementTone(s.status)}>
                        {t(`billing.settlementStatus.${s.status}`)}
                      </Badge>
                    </div>
                    <p className="text-fg-muted mt-1 flex justify-between text-sm">
                      <span>
                        {t('billing.report.due')} {formatDate(s.dueDate)}
                      </span>
                      <span className="text-fg font-mono tabular-nums">
                        {formatMoney(s.payableAmount, s.currencyCode)}
                      </span>
                    </p>
                    <p className="text-fg-muted flex justify-between text-sm">
                      <span>{t('billing.report.openAmount')}</span>
                      <span className="text-fg font-mono tabular-nums">
                        {formatMoney(s.openAmount, s.currencyCode)}
                      </span>
                    </p>
                  </li>
                ))}
              </ul>
            ) : (
              <div className="relative mt-3 overflow-x-auto">
                <Table data-testid="statement-settlements">
                  <THead>
                    <TR>
                      <TH>{t('billing.report.settlement')}</TH>
                      <TH>{t('billing.provider.status')}</TH>
                      <TH>{t('billing.report.batch')}</TH>
                      <TH>{t('billing.report.due')}</TH>
                      <TH className="text-right">{t('billing.report.payable')}</TH>
                      <TH className="text-right">{t('billing.report.paid')}</TH>
                      <TH className="text-right">{t('billing.report.openAmount')}</TH>
                      <TH>{t('billing.report.lastPaidAt')}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {data.settlements.map((s) => (
                      <TR key={s.id} data-testid="statement-settlement">
                        <TD className="font-mono text-xs whitespace-nowrap">{s.reference}</TD>
                        <TD>
                          <Badge tone={settlementTone(s.status)}>
                            {t(`billing.settlementStatus.${s.status}`)}
                          </Badge>
                        </TD>
                        <TD className="whitespace-nowrap">
                          <Link
                            to="/billing/batches/$batchId"
                            params={{ batchId: s.batchId }}
                            className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                          >
                            {s.batchReference}
                          </Link>
                        </TD>
                        <TD className="whitespace-nowrap">{formatDate(s.dueDate)}</TD>
                        <TD className="text-right font-mono whitespace-nowrap tabular-nums">
                          {formatMoney(s.payableAmount, s.currencyCode)}
                        </TD>
                        <TD className="text-right font-mono whitespace-nowrap tabular-nums">
                          {formatMoney(s.paidAmount, s.currencyCode)}
                        </TD>
                        <TD className="text-right font-mono whitespace-nowrap tabular-nums">
                          {formatMoney(s.openAmount, s.currencyCode)}
                        </TD>
                        <TD className="whitespace-nowrap">
                          {s.lastPaidAt ? formatDateTime(s.lastPaidAt) : '—'}
                        </TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </div>
            )}
          </Card>
        </>
      )}
    </div>
  );
}

/**
 * The statement as a file: queued, rendered by the worker, then downloaded through a link
 * minted per download. The provider downloads its own statement, so the purpose is that and
 * nothing else; the watermark the file carries is shown beside the button.
 */
function StatementExport({
  providerId,
  periodFrom,
  periodTo,
}: {
  providerId: string;
  periodFrom: string;
  periodTo: string;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const create = useCreateStatementExport();
  const [exportId, setExportId] = useState<string | null>(null);
  const current = useStatementExport(exportId);
  const download = useDownloadStatementExport(exportId);
  const record: Export | null = current.data?.data ?? create.data?.data ?? null;

  return (
    <div className="border-line mt-3 grid gap-2 border-t pt-3" data-testid="statement-export">
      <div className="flex flex-wrap items-center gap-3">
        <Button
          size="sm"
          variant="secondary"
          loading={create.isPending}
          onClick={() =>
            create.mutate(
              {
                kind: 'PROVIDER_STATEMENT',
                format: 'CSV',
                providerOrganizationId: providerId,
                periodFrom,
                periodTo,
              },
              {
                onSuccess: (created) => {
                  setExportId(created.data.id);
                  toast.notify({ tone: 'info', title: t('billing.report.requested') });
                },
              },
            )
          }
          data-testid="statement-export-request"
        >
          {t('billing.report.request')}
        </Button>
        {record ? (
          <span
            className="text-fg-muted text-sm"
            role="status"
            data-testid="statement-export-status"
          >
            {t(`billing.report.exportStatus.${record.status}`)}
            {record.status === 'READY'
              ? ` · ${t('billing.report.expiresAt')} ${formatDateTime(record.expiresAt)}`
              : ''}
          </span>
        ) : null}
        {record?.status === 'READY' ? (
          <Button
            size="sm"
            variant="secondary"
            loading={download.isPending}
            onClick={() =>
              download.mutate(
                { purposeCode: 'OWN_STATEMENT' },
                {
                  onSuccess: (link) => {
                    window.open(link.url, '_blank', 'noopener');
                    toast.notify({ tone: 'info', title: t('billing.report.opened') });
                  },
                },
              )
            }
            data-testid="statement-export-download"
          >
            {t('billing.report.download')}
          </Button>
        ) : null}
      </div>
      {download.data ? (
        <span className="text-fg-muted text-xs" data-testid="statement-export-watermark">
          {t('billing.report.watermark')}: {download.data.watermark}
        </span>
      ) : null}
      <ProblemAlert
        problem={
          create.isError
            ? problemOf(create.error)
            : download.isError
              ? problemOf(download.error)
              : null
        }
      />
    </div>
  );
}
