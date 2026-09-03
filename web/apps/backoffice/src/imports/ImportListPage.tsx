import type { ImportBatchStatus } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDateTime, formatNumber, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  EmptyState,
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
  type BadgeTone,
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { useState } from 'react';

import { problemOf } from '../problems';
import { useImportList } from './queries';

const PAGE_SIZE = 25;

const STATUSES: ImportBatchStatus[] = [
  'RECEIVED',
  'VALIDATING',
  'REVIEW',
  'READY',
  'APPLYING',
  'APPLIED',
  'FAILED',
  'CANCELLED',
];

/**
 * Batch status as a badge tone. Only the two states that ask something of a human get a
 * colour: REVIEW wants decisions, FAILED wants a new file. The rest stay quiet.
 */
export function batchTone(status: ImportBatchStatus): BadgeTone {
  switch (status) {
    case 'APPLIED':
      return 'success';
    case 'REVIEW':
      return 'warning';
    case 'FAILED':
      return 'danger';
    case 'READY':
    case 'VALIDATING':
    case 'APPLYING':
      return 'info';
    default:
      return 'neutral';
  }
}

/**
 * The batches this tenant has uploaded, newest first, filtered by status and paged with
 * the server's opaque cursor. The filter and the cursor live in component state so no
 * batch identifier or file name ends up in a shared URL.
 */
export function ImportListPage() {
  const { t } = useTranslation();
  const canRead = usePermission('import.execute');
  const canManage = usePermission('import.execute');
  const [status, setStatus] = useState<ImportBatchStatus | ''>('');
  const [cursor, setCursor] = useState<string | null>(null);
  const [trail, setTrail] = useState<(string | null)[]>([]);

  const query = useImportList(
    {
      limit: PAGE_SIZE,
      ...(status ? { status } : {}),
      ...(cursor ? { cursor } : {}),
    },
    { enabled: canRead },
  );

  function filterBy(next: string) {
    setStatus(next as ImportBatchStatus | '');
    setCursor(null);
    setTrail([]);
  }

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, cursor]);
    setCursor(next);
  }

  function goPrevious() {
    setCursor(trail[trail.length - 1] ?? null);
    setTrail((previous) => previous.slice(0, -1));
  }

  const header = (
    <PageHeader
      title={t('imports.title')}
      description={t('imports.intro')}
      actions={
        canManage ? (
          <Link
            to="/imports/new"
            className="bg-primary text-primary-fg hover:bg-primary-strong inline-flex h-10 items-center rounded-md px-4 text-sm font-medium"
          >
            {t('imports.new')}
          </Link>
        ) : null
      }
    />
  );

  if (!canRead) {
    return (
      <>
        {header}
        <EmptyState title={t('imports.notAllowed')} />
      </>
    );
  }

  const rows = query.data?.items ?? [];

  return (
    <>
      {header}

      <div className="mb-4 flex flex-wrap items-end gap-2">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('imports.statusFilter')}</span>
          <Select
            name="status"
            value={status}
            onChange={(e) => filterBy(e.target.value)}
            placeholder={t('imports.allStatuses')}
            options={STATUSES.map((entry) => ({
              value: entry,
              label: t(`imports.statuses.${entry}`),
            }))}
            className="w-56"
          />
        </label>
        {status ? (
          <Button variant="ghost" onClick={() => filterBy('')}>
            {t('common.clear')}
          </Button>
        ) : null}
      </div>

      <ProblemAlert
        problem={query.error ? problemOf(query.error) : null}
        className="mb-4"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />

      {query.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <EmptyState title={t('imports.empty')} />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="import-table">
            <THead>
              <TR>
                <TH>{t('imports.columns.file')}</TH>
                <TH>{t('imports.columns.source')}</TH>
                <TH>{t('imports.columns.rows')}</TH>
                <TH>{t('imports.columns.status')}</TH>
                <TH>{t('imports.columns.created')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((batch) => (
                <TR key={batch.id}>
                  <TD>
                    <Link
                      to="/imports/$importId"
                      params={{ importId: batch.id }}
                      className="font-medium underline-offset-2 hover:underline"
                    >
                      {batch.fileName}
                    </Link>
                  </TD>
                  <TD>
                    <code className="font-mono text-xs">
                      {batch.sourceSystem} · {batch.sourceVersion}
                    </code>
                  </TD>
                  <TD className="font-mono">{formatNumber(batch.rowCount)}</TD>
                  <TD>
                    <Badge tone={batchTone(batch.status)}>
                      {t(`imports.statuses.${batch.status}`)}
                    </Badge>
                  </TD>
                  <TD>{formatDateTime(batch.createdAt)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
          <nav
            className="mt-3 flex items-center justify-between text-sm"
            aria-label={t('imports.title')}
          >
            <span className="text-fg-muted">
              {t('organizations.page', { n: trail.length + 1 })}
            </span>
            <div className="flex gap-2">
              <Button
                variant="secondary"
                size="sm"
                onClick={goPrevious}
                disabled={trail.length === 0}
              >
                {t('organizations.prevPage')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={goNext}
                disabled={!query.data?.nextCursor}
              >
                {t('organizations.nextPage')}
              </Button>
            </div>
          </nav>
        </>
      )}
    </>
  );
}
