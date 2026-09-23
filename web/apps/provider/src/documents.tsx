import type {
  Document,
  DocumentClassification,
  DocumentListQuery,
  DocumentScanStatus,
} from '@kapsora/api-client';
import { usePermission, useTenantId } from '@kapsora/auth';
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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';

import { useOps } from './services';
import { problemOf } from './problems';

/*
 * The document panel a provider attaches from. It is the backoffice panel's twin with
 * the provider app's own service context; the pipeline it drives is identical — reserve,
 * PUT to the signed quarantine URL, complete, link — and so are the rules: the state is
 * shown as it is, and the download exists only where the server says `downloadable`.
 * Sharing one component across the apps needs a services abstraction the packages do not
 * have yet; that extraction is noted in docs/plan/ROADMAP.md rather than faked here.
 */

async function sha256Hex(file: File): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', await bytesOf(file));
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('');
}

export function useLinkedDocuments(aggregateType: string, aggregateId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const query: DocumentListQuery = { aggregateType, aggregateId, limit: 100 };
  return useQuery({
    queryKey: ['provider', tenantId, 'documents', query],
    queryFn: () => ops.documents.list(tenantId, query),
    enabled: aggregateId !== '',
    refetchInterval: (q) =>
      q.state.data?.items.some((d) => d.scanStatus === 'PENDING' || d.scanStatus === 'SCANNING')
        ? 2_000
        : false,
  });
}

export function useUploadDocument() {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      file: File;
      documentTypeCode: string;
      classification: DocumentClassification;
      aggregateType: string;
      aggregateId: string;
    }) => {
      const reserved = await ops.documents.createUpload(tenantId, {
        originalFilename: input.file.name,
        contentType: input.file.type || 'application/octet-stream',
        byteSize: input.file.size,
        classification: input.classification,
      });
      const target = reserved.upload;
      if (target) {
        const put = await fetch(target.url, {
          method: target.method,
          headers: target.headers as Record<string, string>,
          body: input.file,
        });
        if (!put.ok) throw new Error(`upload refused by storage: ${put.status}`);
      }
      const completed = await ops.documents.completeUpload(tenantId, reserved.document.id, {
        byteSize: input.file.size,
        sha256: await sha256Hex(input.file),
      });
      await ops.documents.link(tenantId, completed.data.id, {
        aggregateType: input.aggregateType,
        aggregateId: input.aggregateId,
        documentTypeCode: input.documentTypeCode,
      });
      return completed.data;
    },
    onSuccess: () => client.invalidateQueries({ queryKey: ['provider', tenantId, 'documents'] }),
  });
}

export function useDownloadDocument() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (documentId: string) => ops.documents.download(tenantId, documentId),
  });
}

const CLASSIFICATIONS: DocumentClassification[] = [
  'PERSONAL',
  'HEALTH',
  'CONFIDENTIAL',
  'INTERNAL',
];

function scanTone(status: DocumentScanStatus): BadgeTone {
  return status === 'CLEAN'
    ? 'success'
    : status === 'INFECTED'
      ? 'danger'
      : status === 'FAILED'
        ? 'warning'
        : 'info';
}

export function DocumentsPanel({
  aggregateType,
  aggregateId,
  requiredTypes,
  readOnly = false,
}: {
  aggregateType: string;
  aggregateId: string;
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
    rows
      .filter((d) => d.downloadable)
      .flatMap((d) =>
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
      // The alert carries the problem.
    }
  }

  async function open(document: Document) {
    try {
      const ticket = await download.mutateAsync(document.id);
      window.open(ticket.url, '_blank', 'noopener');
    } catch {
      // The alert carries the refusal.
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
          className="border-line grid gap-3 rounded-md border p-3 md:grid-cols-[1fr_1fr_1fr_auto] md:items-end"
          noValidate
          data-testid="document-upload-form"
        >
          <FormField
            label={t('documents.choose')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <label className="bg-surface-raised border-line-strong hover:bg-surface-2 inline-flex h-10 cursor-pointer items-center gap-2 rounded-md border px-3 text-sm">
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

/** The file's bytes. jsdom's File has no arrayBuffer(), so a FileReader is the fallback. */
async function bytesOf(file: File): Promise<ArrayBuffer> {
  if (typeof file.arrayBuffer === 'function') return file.arrayBuffer();
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.onerror = () => reject(reader.error ?? new Error('read failed'));
    reader.readAsArrayBuffer(file);
  });
}
