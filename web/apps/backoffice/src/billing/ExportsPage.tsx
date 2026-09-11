import type { Export, ExportKind } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Card,
  Dialog,
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
import { useMemo, useState, type FormEvent } from 'react';

import { useProviderOrganizations } from '../lodging/queries';
import { problemOf } from '../problems';
import { BillingNav } from './BillingNav';
import { useCreateExport, useDownloadExport, useExports } from './queries';
import { exportTone } from './reportStatus';

const KINDS: ExportKind[] = [
  'PROVIDER_STATEMENT',
  'BATCH',
  'SETTLEMENTS',
  'CLAIMS',
  'RECONCILIATION',
];

/**
 * Why a file leaves the building. The audit row takes the code as written, so the list is the
 * export's own rather than the clinical access purposes: nobody downloads a settlement list for
 * a medical review.
 */
const EXPORT_PURPOSES = [
  'RECONCILIATION',
  'BILLING_DISPUTE',
  'AUDIT',
  'MANAGEMENT_REPORT',
] as const;
type ExportPurpose = (typeof EXPORT_PURPOSES)[number];

/** The kinds whose file is bounded by a period; the others carry every row in scope. */
const PERIOD_KINDS: ExportKind[] = ['PROVIDER_STATEMENT', 'BATCH', 'RECONCILIATION'];

function monthStart(): string {
  const d = new Date();
  return new Date(Date.UTC(d.getFullYear(), d.getMonth(), 1)).toISOString().slice(0, 10);
}
function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/**
 * Exports: asked for here, rendered by the worker, listed with their state and what they
 * cover, and opened through a link minted per download. The download is the access that is
 * audited, so it asks why, and the watermark the file carries is shown beside it.
 */
export function ExportsPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const wide = useMinWidth(768);
  const canExport = usePermission('report.export');
  const canExportSensitive = usePermission('report.export.sensitive');
  const [mine, setMine] = useState(true);
  const exports = useExports({ mine });
  const create = useCreateExport();
  const providers = useProviderOrganizations();
  const providerNames = useMemo(
    () => new Map((providers.data?.items ?? []).map((o) => [o.id, o.displayName] as const)),
    [providers.data],
  );
  const [kind, setKind] = useState<ExportKind>('SETTLEMENTS');
  const [providerId, setProviderId] = useState('');
  const [periodFrom, setFrom] = useState(monthStart);
  const [periodTo, setTo] = useState(today);
  const [providerMissing, setProviderMissing] = useState(false);
  const rows = exports.data?.items ?? [];
  const hasPeriod = PERIOD_KINDS.includes(kind);
  // The download dialog lives here, not in a row: below 768 the table becomes a list and the
  // rows are rebuilt, and a dialog held by a row would close under the reader's hand.
  const download = useDownloadExport();
  const [downloadFor, setDownloadFor] = useState<string | null>(null);
  // No purpose is chosen for the reader: a pre-selected one would be recorded unchosen.
  const [purpose, setPurpose] = useState<ExportPurpose | ''>('');
  const [purposeMissing, setPurposeMissing] = useState(false);
  const [reason, setReason] = useState('');
  const [watermarks, setWatermarks] = useState<Record<string, string>>({});

  function ask(exportId: string) {
    setPurpose('');
    setReason('');
    setPurposeMissing(false);
    setDownloadFor(exportId);
  }

  function confirmDownload() {
    if (downloadFor === null) return;
    if (purpose === '') {
      setPurposeMissing(true);
      return;
    }
    const exportId = downloadFor;
    setDownloadFor(null);
    download.mutate(
      { exportId, purposeCode: purpose, ...(reason.trim() ? { reasonText: reason.trim() } : {}) },
      {
        onSuccess: (link) => {
          setWatermarks((w) => ({ ...w, [exportId]: link.watermark }));
          window.open(link.url, '_blank', 'noopener');
          toast.notify({ tone: 'info', title: t('billing.report.opened') });
        },
      },
    );
  }

  function downloadCell(exportId: string) {
    return (
      <DownloadButton
        onAsk={() => ask(exportId)}
        busy={download.isPending && download.variables?.exportId === exportId}
        watermark={watermarks[exportId] ?? null}
        problem={
          download.isError && download.variables?.exportId === exportId
            ? problemOf(download.error)
            : null
        }
      />
    );
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (kind === 'PROVIDER_STATEMENT' && providerId === '') {
      setProviderMissing(true);
      return;
    }
    setProviderMissing(false);
    create.mutate(
      {
        kind,
        format: 'CSV',
        ...(providerId ? { providerOrganizationId: providerId } : {}),
        ...(hasPeriod ? { periodFrom, periodTo } : {}),
      },
      { onSuccess: () => toast.notify({ tone: 'info', title: t('billing.report.requested') }) },
    );
  }

  /** What one export covers, in words: the provider if it names one, the period if it has one. */
  function scopeOf(x: Export): string {
    const parts: string[] = [];
    if (x.providerOrganizationId) {
      parts.push(providerNames.get(x.providerOrganizationId) ?? '…');
    }
    if (x.periodFrom && x.periodTo) {
      parts.push(`${formatDate(x.periodFrom)} – ${formatDate(x.periodTo)}`);
    }
    return parts.length > 0 ? parts.join(' · ') : t('billing.report.allRows');
  }

  return (
    <div className="grid gap-4">
      <PageHeader title={t('billing.report.exportsTitle')} />
      <BillingNav />
      <p className="text-fg-muted text-sm">{t('billing.report.exportsIntro')}</p>

      {canExport ? (
        <Card>
          <form
            onSubmit={submit}
            className="grid gap-3 md:grid-cols-4"
            noValidate
            data-testid="export-form"
          >
            <FormField
              label={t('billing.report.kindLabel')}
              required
              requiredLabel={t('common.requiredMark')}
              {...(kind === 'CLAIMS' ? { hint: t('billing.report.sensitiveHint') } : {})}
            >
              <Select
                name="kind"
                value={kind}
                onChange={(e) => setKind(e.target.value as ExportKind)}
                options={KINDS.filter((k) => k !== 'CLAIMS' || canExportSensitive).map((k) => ({
                  value: k,
                  label: t(`billing.report.exportKinds.${k}`),
                }))}
              />
            </FormField>
            <FormField
              label={t('billing.report.provider')}
              {...(kind === 'PROVIDER_STATEMENT' ? { required: true } : {})}
              requiredLabel={t('common.requiredMark')}
              {...(providerMissing ? { error: t('billing.report.providerRequired') } : {})}
            >
              <Select
                name="providerOrganizationId"
                value={providerId}
                onChange={(e) => {
                  setProviderId(e.target.value);
                  setProviderMissing(false);
                }}
                options={(providers.data?.items ?? []).map((o) => ({
                  value: o.id,
                  label: o.displayName,
                }))}
                placeholder={t('billing.report.allProviders')}
              />
            </FormField>
            {hasPeriod ? (
              <>
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
              </>
            ) : null}
            <div className="md:col-span-4">
              <ProblemAlert
                problem={create.isError ? problemOf(create.error) : null}
                className="mb-3"
              />
              <Button type="submit" loading={create.isPending} data-testid="request-export">
                {t('billing.report.request')}
              </Button>
            </div>
          </form>
        </Card>
      ) : null}

      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          className="accent-primary"
          checked={mine}
          onChange={(e) => setMine(e.target.checked)}
        />
        {t('billing.report.mine')}
      </label>

      {exports.isPending ? (
        <p className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('billing.report.exportsLoading')}
        </p>
      ) : exports.isError ? (
        <ProblemAlert problem={problemOf(exports.error)} />
      ) : rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('billing.report.exportsEmpty')}</p>
      ) : !wide ? (
        <ul className="grid gap-2" data-testid="export-table">
          {rows.map((x) => (
            <li
              key={x.id}
              className="bg-surface-raised border-line rounded-lg border p-3"
              data-testid="export-row"
            >
              <div className="flex items-baseline justify-between gap-3">
                <span className="text-sm font-medium">
                  {t(`billing.report.exportKinds.${x.kind}`)}
                </span>
                <Badge tone={exportTone(x.status)} data-testid="export-status">
                  {t(`billing.report.exportStatus.${x.status}`)}
                </Badge>
              </div>
              <p className="mt-1 text-sm" data-testid="export-scope">
                {scopeOf(x)}
              </p>
              <p className="text-fg-muted mt-1 text-sm">
                {formatDateTime(x.requestedAt)}
                {x.rowCount !== null && x.rowCount !== undefined
                  ? ` · ${t('billing.report.rows', { count: x.rowCount })}`
                  : ''}
              </p>
              <p className="text-fg-muted mt-1 text-xs">
                {t('billing.report.expiresAt')} {formatDateTime(x.expiresAt)} ·{' '}
                {t('billing.report.downloadCount', { count: x.downloadCount })}
              </p>
              {x.status === 'READY' && canExport ? (
                <div className="mt-2">{downloadCell(x.id)}</div>
              ) : null}
            </li>
          ))}
        </ul>
      ) : (
        <div className="relative overflow-x-auto">
          <Table data-testid="export-table">
            <THead>
              <TR>
                <TH>{t('billing.report.kindLabel')}</TH>
                <TH>{t('billing.office.status')}</TH>
                <TH>{t('billing.report.scopeLabel')}</TH>
                <TH>{t('billing.report.requestedAt')}</TH>
                <TH className="text-right">{t('billing.report.rowsLabel')}</TH>
                <TH>{t('billing.report.expiresAt')}</TH>
                <TH className="text-right">{t('billing.report.downloadsLabel')}</TH>
                <TH />
              </TR>
            </THead>
            <TBody>
              {rows.map((x) => (
                <TR key={x.id} data-testid="export-row">
                  <TD>
                    {t(`billing.report.exportKinds.${x.kind}`)}
                    <span className="text-fg-muted block font-mono text-xs">{x.format}</span>
                  </TD>
                  <TD>
                    <Badge tone={exportTone(x.status)} data-testid="export-status">
                      {t(`billing.report.exportStatus.${x.status}`)}
                    </Badge>
                  </TD>
                  <TD className="text-sm" data-testid="export-scope">
                    {scopeOf(x)}
                  </TD>
                  <TD className="whitespace-nowrap">{formatDateTime(x.requestedAt)}</TD>
                  <TD className="text-right font-mono tabular-nums">{x.rowCount ?? '—'}</TD>
                  <TD className="whitespace-nowrap">{formatDateTime(x.expiresAt)}</TD>
                  <TD className="text-right font-mono tabular-nums">{x.downloadCount}</TD>
                  <TD>{x.status === 'READY' && canExport ? downloadCell(x.id) : null}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
      <Dialog
        open={downloadFor !== null}
        onOpenChange={(next) => {
          if (!next) setDownloadFor(null);
        }}
        title={t('billing.report.purposeTitle')}
        description={t('billing.report.purposeIntro')}
        actions={
          <>
            <Button variant="secondary" onClick={() => setDownloadFor(null)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={confirmDownload} data-testid="download-confirm">
              {t('billing.report.download')}
            </Button>
          </>
        }
      >
        <div className="grid gap-3" data-testid="download-purpose">
          <FormField
            label={t('billing.report.downloadPurpose')}
            required
            requiredLabel={t('common.requiredMark')}
            {...(purposeMissing ? { error: t('billing.report.purposeRequired') } : {})}
          >
            <Select
              name="purpose"
              value={purpose}
              onChange={(e) => {
                setPurpose(e.target.value as ExportPurpose);
                setPurposeMissing(false);
              }}
              options={EXPORT_PURPOSES.map((code) => ({
                value: code,
                label: t(`billing.report.purposes.${code}`),
              }))}
              placeholder="—"
            />
          </FormField>
          <FormField label={t('review.purpose.reason')}>
            <Textarea
              name="reason"
              rows={2}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
          </FormField>
        </div>
      </Dialog>
    </div>
  );
}

/** One download control in a row: the button that opens the page's dialog, and the watermark after. */
function DownloadButton({
  onAsk,
  busy,
  watermark,
  problem,
}: {
  onAsk: () => void;
  busy: boolean;
  watermark: string | null;
  problem: ReturnType<typeof problemOf> | null;
}) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-1">
      <Button
        size="sm"
        variant="secondary"
        loading={busy}
        onClick={onAsk}
        data-testid="download-export"
      >
        {t('billing.report.download')}
      </Button>
      {watermark ? (
        <span className="text-fg-muted text-xs" data-testid="export-watermark">
          {t('billing.report.watermark')}: {watermark}
        </span>
      ) : null}
      <ProblemAlert problem={problem} />
    </div>
  );
}
