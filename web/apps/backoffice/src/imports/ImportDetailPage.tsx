import type { ImportRowDecision, ImportRowStatus, MemberImportRow } from '@kapsora/api-client';
import { usePermission, useStepUp } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, formatNumber, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
  Dialog,
  EmptyState,
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
  type BadgeTone,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState } from 'react';

import { problemOf } from '../problems';
import { batchTone } from './ImportListPage';
import { useImportBatch, useImportCommands, useImportRows, useReviewImportRow } from './queries';

const PAGE_SIZE = 25;

const ROW_STATUSES: ImportRowStatus[] = [
  'PENDING',
  'VALID',
  'INVALID',
  'MATCHED',
  'CONFLICT',
  'APPLIED',
  'SKIPPED',
];

/** Row status as a badge tone; a conflict or an error is the only thing that shouts. */
function rowTone(status: ImportRowStatus): BadgeTone {
  switch (status) {
    case 'INVALID':
      return 'danger';
    case 'CONFLICT':
      return 'warning';
    case 'APPLIED':
      return 'success';
    case 'VALID':
    case 'MATCHED':
      return 'info';
    default:
      return 'neutral';
  }
}

/**
 * The decisions the server accepts for a row in this state. An INVALID row can only be
 * skipped, a CONFLICT row needs a human to say which person it is, and an APPLIED or
 * SKIPPED row is finished.
 */
function decisionsFor(status: ImportRowStatus): ImportRowDecision[] {
  switch (status) {
    case 'INVALID':
      return ['SKIP'];
    case 'CONFLICT':
      return ['CREATE', 'UPDATE', 'SKIP'];
    case 'PENDING':
    case 'VALID':
    case 'MATCHED':
      return ['SKIP'];
    default:
      return [];
  }
}

/**
 * One staged batch: what the file holds, what validation and matching made of it, the
 * rows a human still has to decide, and the two commands that end it. The counters sit
 * above the table so the operator sees what apply will do before scrolling anywhere, and
 * the row filter opens on whatever needs attention.
 */
export function ImportDetailPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const canRead = usePermission('import.execute');
  const canManage = usePermission('import.execute');
  const stepUp = useStepUp();
  const params: Record<string, string | undefined> = useParams({ strict: false });
  const importId = params['importId'] ?? '';

  const [chosenStatus, setChosenStatus] = useState<ImportRowStatus | '' | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [trail, setTrail] = useState<(string | null)[]>([]);
  const [candidate, setCandidate] = useState<Record<string, string>>({});
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [confirmApply, setConfirmApply] = useState(false);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [reasonCode, setReasonCode] = useState('');
  const [reasonText, setReasonText] = useState('');

  const batchQuery = useImportBatch(importId, { enabled: canRead });
  const batch = batchQuery.data?.data;
  const counters = batch?.counters;

  // The filter opens on the rows that still need a decision and only follows the
  // operator's own choice afterwards.
  const suggested: ImportRowStatus | '' =
    counters && counters.conflict > 0
      ? 'CONFLICT'
      : counters && counters.invalid > 0
        ? 'INVALID'
        : '';
  const status = chosenStatus ?? suggested;

  const rowsQuery = useImportRows(
    importId,
    { limit: PAGE_SIZE, ...(status ? { status } : {}), ...(cursor ? { cursor } : {}) },
    { enabled: canRead },
  );
  const review = useReviewImportRow(importId);
  const commands = useImportCommands(importId);

  function filterBy(next: string) {
    setChosenStatus(next as ImportRowStatus | '');
    setCursor(null);
    setTrail([]);
  }

  function goNext() {
    const next = rowsQuery.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, cursor]);
    setCursor(next);
  }

  function goPrevious() {
    setCursor(trail[trail.length - 1] ?? null);
    setTrail((previous) => previous.slice(0, -1));
  }

  async function decide(row: MemberImportRow, decision: ImportRowDecision) {
    setProblem(null);
    const target = candidate[row.id] ?? row.matchedPersonId ?? '';
    try {
      await review.mutateAsync({
        rowId: row.id,
        etag: `"${row.rowVersion}"`,
        body: {
          decision,
          ...(decision === 'UPDATE' && target ? { matchedPersonId: target } : {}),
        },
      });
      toast.notify({ tone: 'success', title: t('imports.rows.reviewed') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function apply() {
    const etag = batchQuery.data?.etag;
    if (!etag) return;
    setProblem(null);
    try {
      const done = await stepUp.run(() => commands.apply.mutateAsync({ etag }));
      if (!done) return;
      setConfirmApply(false);
      toast.notify({ tone: 'success', title: t('imports.applied') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function cancelBatch() {
    const etag = batchQuery.data?.etag;
    if (!etag) return;
    setProblem(null);
    try {
      await commands.cancel.mutateAsync({
        etag,
        body: {
          reasonCode: reasonCode.trim(),
          ...(reasonText.trim() ? { reasonText: reasonText.trim() } : {}),
        },
      });
      setConfirmCancel(false);
      toast.notify({ tone: 'success', title: t('imports.cancelled') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  if (!canRead) {
    return (
      <>
        <PageHeader title={t('imports.detailTitle')} />
        <EmptyState title={t('imports.notAllowed')} />
      </>
    );
  }

  if (batchQuery.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }

  if (!batch || !counters) {
    return (
      <ProblemAlert
        page
        problem={problemOf(batchQuery.error)}
        actions={
          <Button size="sm" variant="secondary" onClick={() => void batchQuery.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
    );
  }

  const applicable = batch.status === 'REVIEW' || batch.status === 'READY';
  const cancellable = batch.status !== 'APPLIED' && batch.status !== 'CANCELLED';
  const rows = rowsQuery.data?.items ?? [];

  // Applied batches also report what the run wrote; before that the numbers are a plan.
  const summary: Array<{ key: string; value: number }> = [
    { key: 'rowCount', value: batch.rowCount },
    { key: 'valid', value: counters.valid },
    { key: 'matched', value: counters.matched },
    { key: 'conflict', value: counters.conflict },
    { key: 'invalid', value: counters.invalid },
    ...(batch.status === 'APPLIED'
      ? [
          { key: 'created', value: counters.created },
          { key: 'updated', value: counters.updated },
          { key: 'skipped', value: counters.skipped },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={batch.fileName}
        description={`${batch.sourceSystem} · ${batch.sourceVersion}`}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('imports.title'),
                render: (label) => <Link to="/imports">{label}</Link>,
              },
              { label: batch.fileName },
            ]}
          />
        }
        actions={
          <>
            <Badge tone={batchTone(batch.status)}>{t(`imports.statuses.${batch.status}`)}</Badge>
            {canManage && cancellable ? (
              <Button variant="secondary" onClick={() => setConfirmCancel(true)}>
                {t('imports.cancel')}
              </Button>
            ) : null}
            {canManage && applicable ? (
              <Button onClick={() => setConfirmApply(true)}>{t('imports.apply')}</Button>
            ) : null}
          </>
        }
      />

      {batch.errorSummary ? (
        <p role="status" className="text-fg-muted mb-4 text-sm">
          {batch.errorSummary}
        </p>
      ) : null}

      <Card className="mb-4">
        <h2 className="text-base font-semibold">{t('imports.counters.title')}</h2>
        <dl
          className="mt-3 grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-4 lg:grid-cols-8"
          data-testid="import-counters"
        >
          {summary.map((entry) => (
            <div key={entry.key}>
              <dt className="text-fg-muted text-xs font-medium">
                {t(`imports.counters.${entry.key}`)}
              </dt>
              <dd className="font-mono text-lg font-semibold">{formatNumber(entry.value)}</dd>
            </div>
          ))}
        </dl>
      </Card>

      <div className="mb-4 flex flex-wrap items-end justify-between gap-2">
        <h2 className="text-base font-semibold">{t('imports.rows.title')}</h2>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('imports.statusFilter')}</span>
          <Select
            name="rowStatus"
            value={status}
            onChange={(e) => filterBy(e.target.value)}
            placeholder={t('imports.allStatuses')}
            options={ROW_STATUSES.map((entry) => ({
              value: entry,
              label: t(`imports.rows.statuses.${entry}`),
            }))}
            className="w-56"
          />
        </label>
      </div>

      <ProblemAlert
        page
        problem={problem ?? (rowsQuery.error ? problemOf(rowsQuery.error) : null)}
        className="mb-4"
        actions={
          <Button
            size="sm"
            variant="secondary"
            onClick={() => {
              setProblem(null);
              void rowsQuery.refetch();
              void batchQuery.refetch();
            }}
          >
            {t('common.retry')}
          </Button>
        }
      />

      {rowsQuery.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <EmptyState title={t('imports.rows.empty')} />
      ) : (
        <>
          <Table aria-busy={rowsQuery.isFetching || undefined} data-testid="import-row-table">
            <THead>
              <TR>
                <TH>{t('imports.rows.columns.rowNo')}</TH>
                <TH>{t('imports.rows.columns.recordID')}</TH>
                <TH>{t('imports.rows.columns.name')}</TH>
                <TH>{t('imports.rows.columns.identifier')}</TH>
                <TH>{t('imports.rows.columns.status')}</TH>
                <TH>{t('imports.rows.columns.decision')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((row) => {
                const decisions = canManage ? decisionsFor(row.status) : [];
                const chosenCandidate = candidate[row.id] ?? row.matchedPersonId ?? '';
                return (
                  <TR key={row.id}>
                    <TD className="font-mono">{formatNumber(row.rowNo)}</TD>
                    <TD>
                      <code className="font-mono text-xs">{row.sourceRecordId}</code>
                    </TD>
                    <TD>
                      <span className="font-medium">{row.displayName}</span>
                      {row.birthDate ? (
                        <span className="text-fg-muted block text-xs">
                          {formatDate(row.birthDate)}
                        </span>
                      ) : null}
                    </TD>
                    <TD>
                      <code className="font-mono text-xs">
                        {row.identifiers.length > 0
                          ? row.identifiers.map((entry) => entry.maskedValue).join(' · ')
                          : t('common.none')}
                      </code>
                    </TD>
                    <TD>
                      <Badge tone={rowTone(row.status)}>
                        {t(`imports.rows.statuses.${row.status}`)}
                      </Badge>
                      {row.errors.length > 0 ? (
                        <ul className="text-danger mt-1 text-xs">
                          {row.errors.map((entry) => (
                            <li key={`${entry.field}-${entry.code}`}>
                              {fieldErrorMessage(t, entry.code, entry.message)}
                            </li>
                          ))}
                        </ul>
                      ) : null}
                    </TD>
                    <TD>
                      <div className="flex flex-wrap items-center gap-2">
                        {row.decision ? (
                          <span className="text-fg-muted text-xs">
                            {t(`imports.rows.decisions.${row.decision}`)}
                          </span>
                        ) : null}
                        {row.status === 'CONFLICT' && (row.candidatePersonIds ?? []).length > 0 ? (
                          <Select
                            aria-label={t('imports.rows.chooseCandidate')}
                            value={chosenCandidate}
                            onChange={(e) =>
                              setCandidate((current) => ({ ...current, [row.id]: e.target.value }))
                            }
                            placeholder={t('common.none')}
                            options={(row.candidatePersonIds ?? []).map((personId) => ({
                              value: personId,
                              label: personId,
                            }))}
                            className="w-56"
                          />
                        ) : null}
                        {decisions.map((decision) => (
                          <Button
                            key={decision}
                            size="sm"
                            variant="secondary"
                            loading={review.isPending}
                            disabled={decision === 'UPDATE' && chosenCandidate === ''}
                            onClick={() => void decide(row, decision)}
                          >
                            {t(`imports.rows.decisions.${decision}`)}
                          </Button>
                        ))}
                      </div>
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
          <nav
            className="mt-3 flex items-center justify-between text-sm"
            aria-label={t('imports.rows.title')}
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
                disabled={!rowsQuery.data?.nextCursor}
              >
                {t('organizations.nextPage')}
              </Button>
            </div>
          </nav>
        </>
      )}

      <Dialog
        open={confirmApply}
        onOpenChange={(open) => {
          if (!open) setConfirmApply(false);
        }}
        title={t('imports.applyTitle')}
        description={t('imports.applyConfirm')}
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => setConfirmApply(false)}
              disabled={commands.apply.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button loading={commands.apply.isPending} onClick={() => void apply()}>
              {t('imports.apply')}
            </Button>
          </>
        }
      >
        <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm" data-testid="apply-summary">
          {(['valid', 'matched', 'conflict', 'invalid'] as const).map((key) => (
            <div key={key} className="flex items-baseline justify-between gap-3">
              <dt className="text-fg-muted">{t(`imports.counters.${key}`)}</dt>
              <dd className="font-mono font-semibold">{formatNumber(counters[key])}</dd>
            </div>
          ))}
        </dl>
        <ProblemAlert problem={problem} className="mt-4" />
      </Dialog>

      <Dialog
        open={confirmCancel}
        onOpenChange={(open) => {
          if (!open) setConfirmCancel(false);
        }}
        title={t('imports.cancelTitle')}
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => setConfirmCancel(false)}
              disabled={commands.cancel.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button
              variant="danger"
              loading={commands.cancel.isPending}
              disabled={reasonCode.trim() === ''}
              onClick={() => void cancelBatch()}
            >
              {t('imports.cancel')}
            </Button>
          </>
        }
      >
        <div className="grid gap-4">
          <FormField
            label={t('imports.fields.cancelReason')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              name="reasonCode"
              value={reasonCode}
              onChange={(e) => setReasonCode(e.target.value)}
              autoComplete="off"
              required
            />
          </FormField>
          <FormField label={t('imports.fields.cancelReasonText')}>
            <Input
              name="reasonText"
              value={reasonText}
              onChange={(e) => setReasonText(e.target.value)}
              autoComplete="off"
            />
          </FormField>
          <ProblemAlert problem={problem} />
        </div>
      </Dialog>

      <StepUpDialog
        open={stepUp.required}
        action={t('imports.apply')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </>
  );
}
