import type { Document, DocumentClassification, DocumentScanStatus } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  FormField,
  Input,
  ProblemAlert,
  Select,
  Spinner,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  useToast,
  type BadgeTone,
} from '@kapsora/ui';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useDownloadDocument, useLinkedDocuments, useUploadDocument } from './queries';

const CLASSIFICATIONS: DocumentClassification[] = [
  'INTERNAL',
  'CONFIDENTIAL',
  'PERSONAL',
  'HEALTH',
];

function scanTone(status: DocumentScanStatus): BadgeTone {
  switch (status) {
    case 'CLEAN':
      return 'success';
    case 'INFECTED':
      return 'danger';
    case 'FAILED':
      return 'warning';
    default:
      return 'info';
  }
}

function formatBytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return '—';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/**
 * The documents on one record, and the place to add one.
 *
 * A file's state is shown as it is, never as what it is about to be (DESIGN.md): the
 * badge says PENDING, SCANNING, CLEAN, INFECTED or FAILED exactly as the server does, and
 * the download appears only where the server says `downloadable`. Where it is absent the
 * row says which state is in the way, so nobody clicks to find out.
 */
export function DocumentsPanel({
  aggregateType,
  aggregateId,
  requiredTypes,
  readOnly = false,
}: {
  aggregateType: string;
  aggregateId: string;
  /** Types a rule asked for; the ones not yet linked are named as missing. */
  requiredTypes?: string[] | null | undefined;
  /** A closed record takes no more documents; the form is absent rather than refused. */
  readOnly?: boolean | undefined;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const canUpload = usePermission('document.upload');
  const documents = useLinkedDocuments(aggregateType, aggregateId);
  const upload = useUploadDocument();
  const download = useDownloadDocument();
  const [file, setFile] = useState<File | null>(null);
  const [typeCode, setTypeCode] = useState('');
  const [classification, setClassification] = useState<DocumentClassification>('PERSONAL');

  const rows: Document[] = documents.data?.items ?? [];
  const linkedTypes = new Set(
    rows.flatMap((d) =>
      d.links.filter((l) => l.aggregateId === aggregateId).map((l) => l.documentTypeCode),
    ),
  );
  const missing = (requiredTypes ?? []).filter((code) => !linkedTypes.has(code));
  // What the type field offers follows the request, not the documents query. `missing`
  // shrinks as the linked documents arrive, and a field that turned from a list into a
  // text box under the cursor would be the screen changing its mind mid-gesture. The
  // callout above says what is still outstanding; this offers every type the request
  // deals with, an attached one included, because a corrected invoice is a second invoice.
  const offeredTypes = requiredTypes ?? [];

  async function submitUpload(event: FormEvent) {
    event.preventDefault();
    if (!file || !typeCode.trim()) return;
    try {
      await upload.mutateAsync({
        file,
        documentTypeCode: typeCode.trim(),
        classification,
        aggregateType,
        aggregateId,
      });
      setFile(null);
      setTypeCode('');
      toast.notify({ tone: 'success', title: t('documents.scan.PENDING') });
    } catch {
      // The alert below carries the problem; nothing else to do here.
    }
  }

  async function open(document: Document) {
    try {
      const ticket = await download.mutateAsync(document.id);
      window.open(ticket.url, '_blank', 'noopener');
    } catch {
      // The alert below carries the refusal.
    }
  }

  return (
    <div className="grid gap-4">
      {missing.length > 0 ? (
        <section
          aria-labelledby="documents-missing"
          className="bg-warning-soft border-warning/40 rounded-md border p-3 text-sm"
        >
          <h3 id="documents-missing" className="font-semibold">
            {t('documents.missing.title')}
          </h3>
          <p className="text-fg-muted mt-1">{t('documents.missing.body')}</p>
          <ul className="mt-1 flex flex-wrap gap-2">
            {missing.map((code) => (
              <li key={code}>
                <code className="bg-surface rounded px-1.5 py-0.5 font-mono text-xs">{code}</code>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      <ProblemAlert problem={documents.error ? problemOf(documents.error) : null} />
      <ProblemAlert problem={upload.error ? problemOf(upload.error) : null} />
      <ProblemAlert problem={download.error ? problemOf(download.error) : null} />

      {documents.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('documents.empty')}</p>
      ) : (
        <Table data-testid="documents-table">
          <THead>
            <TR>
              <TH>{t('documents.filename')}</TH>
              <TH>{t('documents.type')}</TH>
              <TH>{t('documents.classification')}</TH>
              <TH>{t('documents.size')}</TH>
              <TH>{t('documents.uploadedAt')}</TH>
              <TH>{t('documents.scan.CLEAN')}</TH>
              <TH>
                <span className="sr-only">{t('common.actions')}</span>
              </TH>
            </TR>
          </THead>
          <TBody>
            {rows.map((document) => {
              const link = document.links.find((l) => l.aggregateId === aggregateId);
              const help =
                document.scanStatus === 'SCANNING' ||
                document.scanStatus === 'INFECTED' ||
                document.scanStatus === 'FAILED'
                  ? t(`documents.scanHelp.${document.scanStatus}`)
                  : null;
              return (
                <TR key={document.id}>
                  <TD className="font-medium">{document.originalFilename}</TD>
                  <TD>
                    <code className="font-mono text-xs">{link?.documentTypeCode ?? '—'}</code>
                  </TD>
                  <TD>{t(`documents.classifications.${document.classification}`)}</TD>
                  <TD className="text-right font-mono text-xs tabular-nums">
                    {formatBytes(document.byteSize)}
                  </TD>
                  <TD>{formatDateTime(document.createdAt)}</TD>
                  <TD>
                    <div className="flex items-center gap-2">
                      {document.scanStatus === 'PENDING' || document.scanStatus === 'SCANNING' ? (
                        <Spinner />
                      ) : null}
                      <Badge tone={scanTone(document.scanStatus)}>
                        {t(`documents.scan.${document.scanStatus}`)}
                      </Badge>
                    </div>
                    {help ? <p className="text-fg-muted mt-1 text-xs">{help}</p> : null}
                  </TD>
                  <TD>
                    {document.downloadable ? (
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => void open(document)}
                        loading={download.isPending && download.variables === document.id}
                      >
                        {t('documents.download')}
                      </Button>
                    ) : null}
                    {document.downloadable && document.classification === 'HEALTH' ? (
                      <p className="text-fg-muted mt-1 text-xs">{t('documents.healthWarning')}</p>
                    ) : null}
                  </TD>
                </TR>
              );
            })}
          </TBody>
        </Table>
      )}

      {canUpload && !readOnly ? (
        <form
          onSubmit={submitUpload}
          className="border-border grid gap-3 rounded-md border p-3 md:grid-cols-[1fr_1fr_1fr_auto] md:items-end"
          noValidate
          data-testid="document-upload-form"
        >
          <FormField
            label={t('documents.choose')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <label className="bg-surface border-border-strong hover:bg-surface-2 inline-flex h-10 cursor-pointer items-center gap-2 rounded-md border px-3 text-sm">
              <span className="font-medium">{t('documents.choose')}</span>
              <span className="text-fg-muted truncate">
                {file ? file.name : t('documents.noFile')}
              </span>
              <input
                type="file"
                name="file"
                className="sr-only"
                onChange={(e) => setFile(e.target.files?.[0] ?? null)}
              />
            </label>
          </FormField>
          <FormField label={t('documents.type')} required requiredLabel={t('common.requiredMark')}>
            {offeredTypes.length > 0 ? (
              <Select
                name="documentTypeCode"
                value={typeCode}
                onChange={(e) => setTypeCode(e.target.value)}
                placeholder={t('common.none')}
                options={offeredTypes.map((code) => ({ value: code, label: code }))}
              />
            ) : (
              <Input
                name="documentTypeCode"
                value={typeCode}
                onChange={(e) => setTypeCode(e.target.value.toUpperCase())}
                className="font-mono"
                autoComplete="off"
                spellCheck={false}
              />
            )}
          </FormField>
          <FormField label={t('documents.classification')}>
            <Select
              name="classification"
              value={classification}
              onChange={(e) => setClassification(e.target.value as DocumentClassification)}
              options={CLASSIFICATIONS.map((c) => ({
                value: c,
                label: t(`documents.classifications.${c}`),
              }))}
            />
          </FormField>
          {file && typeCode.trim() ? (
            <Button type="submit" variant="secondary" loading={upload.isPending}>
              {upload.isPending ? t('documents.uploading') : t('documents.upload')}
            </Button>
          ) : null}
        </form>
      ) : null}
    </div>
  );
}
