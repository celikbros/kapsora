import { etagOf, ApiError, type WorkItem, type WorkItemListQuery } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Dialog,
  EmptyState,
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
  useToast,
  type BadgeTone,
} from '@kapsora/ui';
import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useActorLabel, useWorkItemCommands, useWorkItems, useWorkQueues } from './queries';

export type WorklistView = 'mine' | 'unassigned' | 'overdue' | 'all';
export interface WorklistSearch {
  view?: WorklistView;
  queueId?: string;
  cursor?: string;
}

const PAGE_SIZE = 50;
const VIEWS: WorklistView[] = ['mine', 'unassigned', 'overdue', 'all'];
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;

function itemTone(status: WorkItem['status']): BadgeTone {
  switch (status) {
    case 'CLAIMED':
      return 'info';
    case 'COMPLETED':
      return 'success';
    case 'ESCALATED':
      return 'warning';
    case 'CANCELLED':
      return 'neutral';
    default:
      return 'neutral';
  }
}

function queryFor(view: WorklistView, queueId?: string, cursor?: string): WorkItemListQuery {
  const q: WorkItemListQuery = { limit: PAGE_SIZE };
  if (queueId) q.queueId = queueId;
  if (cursor) q.cursor = cursor;
  switch (view) {
    case 'mine':
      q.assignedToMe = true;
      break;
    case 'unassigned':
      q.status = 'OPEN';
      break;
    case 'overdue':
      q.overdue = true;
      break;
    default:
      break;
  }
  return q;
}

/** Where a work item's aggregate lives, when the product has a screen for it. */
function AggregateLink({ item }: { item: WorkItem }) {
  if (item.aggregateType === 'SERVICE_REQUEST') {
    return (
      <Link
        to="/requests/$requestId"
        params={{ requestId: item.aggregateId }}
        className="font-medium underline-offset-2 hover:underline"
      >
        {item.title}
      </Link>
    );
  }
  return <span className="font-medium">{item.title}</span>;
}

/**
 * The list an operator opens in the morning: what is waiting, what is mine, what is late.
 * Dense and boring on purpose — it is read for hours (DESIGN.md). Claiming is one click,
 * and a claim somebody else won says who has it rather than failing silently. Lateness is
 * judged by the item's own `dueAt`, never recomputed from the queue's SLA today.
 */
export function WorklistPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const search = useSearch({ from: '/app/worklist' });
  const view: WorklistView = search.view ?? 'mine';
  const canClaim = usePermission('worklist.claim');
  const canReassign = usePermission('worklist.reassign');
  const label = useActorLabel();
  const [trail, setTrail] = useState<string[]>([]);
  const [lost, setLost] = useState<{ id: string; assignee: string } | null>(null);
  const [completing, setCompleting] = useState<WorkItem | null>(null);
  const [reassigning, setReassigning] = useState<WorkItem | null>(null);
  const [outcome, setOutcome] = useState('');
  const [comment, setComment] = useState('');
  const [assignee, setAssignee] = useState('');

  const queues = useWorkQueues();
  const items = useWorkItems(queryFor(view, search.queueId, search.cursor));
  const commands = useWorkItemCommands();
  // Read once per mount: lateness is a fact about the row, and a re-render must not move it.
  const [now] = useState(() => Date.now());

  const queueName = (id: string) => queues.data?.find((q) => q.id === id)?.name ?? id;

  function setView(next: WorklistView) {
    setTrail([]);
    setLost(null);
    void navigate({
      to: '/worklist',
      search: { view: next, ...(search.queueId ? { queueId: search.queueId } : {}) },
    });
  }

  function setQueue(queueId: string) {
    setTrail([]);
    void navigate({ to: '/worklist', search: { view, ...(queueId ? { queueId } : {}) } });
  }

  async function claim(item: WorkItem, etag: string) {
    setLost(null);
    try {
      await commands.claim.mutateAsync({ id: item.id, etag });
      toast.notify({ tone: 'success', title: t('worklist.status.CLAIMED') });
    } catch (err) {
      const problem = problemOf(err);
      if (err instanceof ApiError && problem.code === 'WORK_ITEM_ALREADY_CLAIMED') {
        // The server names the holder in the refusal; the screen owes the operator that.
        const id = UUID.exec(problem.detail ?? '')?.[0] ?? null;
        setLost({ id: item.id, assignee: label(id) });
      }
    }
  }

  async function submitComplete(event: FormEvent) {
    event.preventDefault();
    if (!completing || !outcome.trim()) return;
    await commands.complete.mutateAsync({
      id: completing.id,
      etag: etagOf(completing.rowVersion),
      body: { outcomeCode: outcome.trim(), ...(comment.trim() ? { comment: comment.trim() } : {}) },
    });
    setCompleting(null);
    setOutcome('');
    setComment('');
  }

  async function submitReassign(event: FormEvent) {
    event.preventDefault();
    if (!reassigning || !assignee.trim()) return;
    await commands.reassign.mutateAsync({
      id: reassigning.id,
      etag: etagOf(reassigning.rowVersion),
      body: { assigneeActorId: assignee.trim() },
    });
    setReassigning(null);
    setAssignee('');
  }

  const rows: WorkItem[] = items.data?.items ?? [];

  return (
    <>
      <PageHeader title={t('worklist.title')} description={t('worklist.intro')} />

      <div className="mb-4 flex flex-wrap items-end gap-3">
        <nav aria-label={t('worklist.title')} className="border-border flex border-b">
          {VIEWS.map((v) => (
            <button
              key={v}
              type="button"
              onClick={() => setView(v)}
              aria-current={view === v ? 'page' : undefined}
              className={
                view === v
                  ? 'border-primary text-fg -mb-px border-b-2 px-3 py-2 text-sm font-medium'
                  : 'text-fg-muted hover:text-fg -mb-px border-b-2 border-transparent px-3 py-2 text-sm'
              }
            >
              {t(`worklist.tabs.${v}`)}
            </button>
          ))}
        </nav>
        <label className="ml-auto grid gap-1 text-sm">
          <span className="font-medium">{t('worklist.columns.queue')}</span>
          <Select
            name="queueId"
            value={search.queueId ?? ''}
            onChange={(e) => setQueue(e.target.value)}
            placeholder={t('requests.filters.all')}
            options={(queues.data ?? []).map((q) => ({ value: q.id, label: q.name }))}
            className="w-56"
          />
        </label>
      </div>

      <ProblemAlert
        problem={items.error ? problemOf(items.error) : null}
        className="mb-4"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void items.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
      <ProblemAlert
        problem={
          commands.claim.error && !(lost && commands.claim.error instanceof ApiError)
            ? problemOf(commands.claim.error)
            : null
        }
        className="mb-4"
      />
      {lost ? (
        <p role="status" className="bg-info-soft border-info/40 mb-4 rounded-md border p-3 text-sm">
          {t('worklist.lostClaim', { assignee: lost.assignee })}
        </p>
      ) : null}

      {items.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <EmptyState title={t('worklist.empty')} />
      ) : (
        <>
          <Table aria-busy={items.isFetching || undefined} data-testid="worklist-table">
            <THead>
              <TR>
                <TH>{t('worklist.columns.title')}</TH>
                <TH>
                  <span className="sr-only">{t('common.actions')}</span>
                </TH>
                <TH>{t('worklist.columns.status')}</TH>
                <TH>{t('worklist.columns.dueAt')}</TH>
                <TH>{t('worklist.columns.assignee')}</TH>
                <TH>{t('worklist.columns.queue')}</TH>
                <TH className="text-right">{t('worklist.columns.priority')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((item) => {
                const late =
                  Boolean(item.dueAt) &&
                  new Date(item.dueAt!).getTime() < now &&
                  (item.status === 'OPEN' ||
                    item.status === 'CLAIMED' ||
                    item.status === 'ESCALATED');
                const open = item.status === 'OPEN' || item.status === 'ESCALATED';
                return (
                  <TR key={item.id} data-late={late || undefined}>
                    <TD>
                      <AggregateLink item={item} />
                      {/* Below md the TERMİN column scrolls out of view; the clock stays here. */}
                      {item.dueAt ? (
                        <p
                          className={`mt-0.5 text-xs md:hidden ${late ? 'text-danger font-medium' : 'text-fg-muted'}`}
                        >
                          {formatDateTime(item.dueAt)}
                          {late ? ` · ${t('worklist.overdue')}` : ''}
                        </p>
                      ) : null}
                      {item.escalatedFromQueueId ? (
                        <p className="text-fg-muted mt-0.5 text-xs">
                          {t('worklist.escalatedFrom', {
                            queue: queueName(item.escalatedFromQueueId),
                          })}
                        </p>
                      ) : null}
                    </TD>
                    <TD>
                      <div className="flex flex-wrap justify-start gap-1">
                        {canClaim && open ? (
                          <Button
                            size="sm"
                            onClick={() => void claim(item, etagOf(item.rowVersion))}
                            loading={
                              commands.claim.isPending && commands.claim.variables?.id === item.id
                            }
                          >
                            {t('worklist.claim')}
                          </Button>
                        ) : null}
                        {canClaim &&
                        item.status === 'CLAIMED' &&
                        label(item.assigneeActorId) === t('worklist.me') ? (
                          <>
                            <Button
                              size="sm"
                              variant="secondary"
                              onClick={() =>
                                void commands.release.mutateAsync({
                                  id: item.id,
                                  etag: etagOf(item.rowVersion),
                                })
                              }
                            >
                              {t('worklist.release')}
                            </Button>
                            <Button
                              size="sm"
                              variant="secondary"
                              onClick={() => setCompleting(item)}
                            >
                              {t('worklist.complete')}
                            </Button>
                          </>
                        ) : null}
                        {canReassign && (item.status === 'CLAIMED' || open) ? (
                          <Button size="sm" variant="ghost" onClick={() => setReassigning(item)}>
                            {t('worklist.reassign')}
                          </Button>
                        ) : null}
                      </div>
                    </TD>
                    <TD>
                      <Badge tone={itemTone(item.status)}>
                        {t(`worklist.status.${item.status}`)}
                      </Badge>
                    </TD>
                    <TD>
                      {item.dueAt ? (
                        <span className={late ? 'text-danger font-medium' : undefined}>
                          {formatDateTime(item.dueAt)}
                          {late ? ` · ${t('worklist.overdue')}` : ''}
                        </span>
                      ) : (
                        <span className="text-fg-muted">{t('common.none')}</span>
                      )}
                    </TD>
                    <TD>
                      {item.assigneeActorId ? (
                        <code className="font-mono text-xs">{label(item.assigneeActorId)}</code>
                      ) : (
                        <span className="text-fg-muted">{t('worklist.unassigned')}</span>
                      )}
                    </TD>
                    <TD>{queueName(item.queueId)}</TD>
                    <TD className="text-right font-mono text-xs tabular-nums">{item.priority}</TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
          <nav className="mt-3 flex items-center justify-between text-sm" aria-label="Sayfalama">
            <span className="text-fg-muted">
              {t('organizations.page', { n: trail.length + 1 })}
            </span>
            <div className="flex gap-2">
              <Button
                variant="secondary"
                size="sm"
                disabled={trail.length === 0}
                onClick={() => {
                  const previous = trail[trail.length - 1];
                  setTrail((rest) => rest.slice(0, -1));
                  const { cursor: _cursor, ...others } = search;
                  void navigate({
                    to: '/worklist',
                    search: previous ? { ...others, cursor: previous } : others,
                  });
                }}
              >
                {t('organizations.prevPage')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={!items.data?.nextCursor}
                onClick={() => {
                  const next = items.data?.nextCursor;
                  if (!next) return;
                  setTrail((previous) => [...previous, search.cursor ?? '']);
                  void navigate({ to: '/worklist', search: { ...search, cursor: next } });
                }}
              >
                {t('organizations.nextPage')}
              </Button>
            </div>
          </nav>
        </>
      )}

      <Dialog
        open={completing !== null}
        onOpenChange={(open) => {
          if (!open) setCompleting(null);
        }}
        title={t('worklist.complete')}
        description={completing?.title}
      >
        <form onSubmit={submitComplete} className="grid gap-4" noValidate>
          <ProblemAlert
            problem={commands.complete.error ? problemOf(commands.complete.error) : null}
          />
          <FormField
            label={t('requests.reasonCode')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              name="outcomeCode"
              value={outcome}
              onChange={(e) => setOutcome(e.target.value.toUpperCase())}
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
            />
          </FormField>
          <FormField label={t('worklist.comments.add')}>
            <Textarea
              name="comment"
              rows={3}
              value={comment}
              onChange={(e) => setComment(e.target.value)}
            />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setCompleting(null)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={commands.complete.isPending} disabled={!outcome.trim()}>
              {t('worklist.complete')}
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog
        open={reassigning !== null}
        onOpenChange={(open) => {
          if (!open) setReassigning(null);
        }}
        title={t('worklist.reassign')}
        description={reassigning?.title}
      >
        <form onSubmit={submitReassign} className="grid gap-4" noValidate>
          <ProblemAlert
            problem={commands.reassign.error ? problemOf(commands.reassign.error) : null}
          />
          <FormField
            label={t('worklist.columns.assignee')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              name="assigneeActorId"
              value={assignee}
              onChange={(e) => setAssignee(e.target.value)}
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
            />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setReassigning(null)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={commands.reassign.isPending} disabled={!assignee.trim()}>
              {t('worklist.reassign')}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}
