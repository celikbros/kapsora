import type { RoleChangeRequest } from '@kapsora/api-client';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  EmptyState,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  statusTone,
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { useState } from 'react';
import { problemOf } from '../problems';
import { RoleChangePerson } from './RoleChangePerson';
import { roleTitle } from './RoleChangeCopy';
import { useRoleChanges } from './queries';

type Status = RoleChangeRequest['status'];
const history: Status[] = ['APPROVED', 'REJECTED', 'CANCELLED'];

export function RoleChangeQueuePage() {
  const { t } = useTranslation();
  const [status, setStatus] = useState<Status>('PENDING');
  const [cursors, setCursors] = useState<string[]>(['']);
  const [index, setIndex] = useState(0);
  const cursor = cursors[index] ?? '';
  const query = useRoleChanges({ status, limit: 25, ...(cursor ? { cursor } : {}) });
  const next = query.data?.nextCursor;
  const canNext = !!next && !cursors.slice(0, index + 1).includes(next);
  function changeStatus(value: Status) {
    setStatus(value);
    setCursors(['']);
    setIndex(0);
  }
  return (
    <section data-testid="role-change-queue-page" className="min-w-0">
      <Link
        to="/admin"
        activeOptions={{ exact: true }}
        className="text-primary mb-4 inline-block text-sm hover:underline"
      >
        ← {t('adminUsers.back')}
      </Link>
      <PageHeader title={t('roleChanges.queueTitle')} description={t('roleChanges.queueIntro')} />
      <div
        className="mb-4 flex flex-wrap gap-2"
        role="group"
        aria-label={t('roleChanges.queueView')}
      >
        <Button
          variant={status === 'PENDING' ? 'primary' : 'secondary'}
          onClick={() => changeStatus('PENDING')}
        >
          {t('roleChanges.pendingView')}
        </Button>
        <label className="grid min-w-40 gap-1 text-sm">
          <span>{t('roleChanges.historyView')}</span>
          <Select
            value={status === 'PENDING' ? '' : status}
            onChange={(event) => changeStatus(event.target.value as Status)}
            placeholder={t('roleChanges.chooseHistory')}
            options={history.map((item) => ({
              value: item,
              label: t(`roleChanges.status.${item}`),
            }))}
          />
        </label>
      </div>
      {query.isPending ? (
        <p aria-busy="true">
          <Spinner /> {t('common.loading')}
        </p>
      ) : query.isError ? (
        <ProblemAlert
          page
          problem={problemOf(query.error)}
          actions={<Button onClick={() => void query.refetch()}>{t('common.retry')}</Button>}
        />
      ) : query.data.items.length === 0 ? (
        <EmptyState
          title={
            status === 'PENDING' ? t('roleChanges.emptyPending') : t('roleChanges.emptyHistory')
          }
        />
      ) : (
        <ul className="grid gap-2 lg:grid-cols-2">
          {query.data.items.map((row) => (
            <li
              key={row.id}
              data-testid="role-change-row"
              className="border-line bg-surface grid min-w-0 gap-2 rounded-lg border p-4 text-sm"
            >
              <div className="flex flex-wrap items-start justify-between gap-2">
                <Link
                  to="/admin/role-change-requests/$requestId"
                  params={{ requestId: row.id }}
                  className="text-primary min-w-0 font-semibold hover:underline"
                >
                  {t(`roleChanges.operation.${row.operation}`)} · {roleTitle(row.roleCode, t)}
                </Link>
                <Badge tone={statusTone(row.status)}>{t(`roleChanges.status.${row.status}`)}</Badge>
              </div>
              <p>
                {t('roleChanges.target')}:{' '}
                <RoleChangePerson membershipId={row.targetMembershipId} />
              </p>
              <p>
                {t('roleChanges.maker')}: <RoleChangePerson membershipId={row.makerMembershipId} />
              </p>
              <p className="text-fg-muted">{formatDateTime(row.createdAt)}</p>
            </li>
          ))}
        </ul>
      )}
      {(index > 0 || canNext) && (
        <nav className="mt-4 flex gap-2" aria-label={t('roleChanges.pages')}>
          <Button
            variant="secondary"
            disabled={index === 0 || query.isPending}
            onClick={() => setIndex(index - 1)}
          >
            {t('adminUsers.previous')}
          </Button>
          <Button
            variant="secondary"
            disabled={!canNext || query.isPending}
            onClick={() => {
              if (next) {
                setCursors((old) => [...old.slice(0, index + 1), next]);
                setIndex(index + 1);
              }
            }}
          >
            {t('adminUsers.next')}
          </Button>
        </nav>
      )}
    </section>
  );
}
