import type { TenantMembershipStatus, TenantUser } from '@kapsora/api-client';
import { formatDate, useTranslation } from '@kapsora/i18n';
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
  statusTone,
  useMinWidth,
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { useSession } from '@kapsora/auth';
import { useState } from 'react';
import { problemOf } from '../problems';
import { useTenantUsers } from './queries';

const statuses: TenantMembershipStatus[] = ['PENDING', 'ACTIVE', 'SUSPENDED', 'REVOKED'];
const pageSize = 50;

export function AdminUsersPage() {
  const { t } = useTranslation();
  const canManageRoles = useSession((s) => s.activeTenant?.canManageTenantRoles === true);
  const wide = useMinWidth(768);
  const [status, setStatus] = useState<TenantMembershipStatus | ''>('');
  const [cursors, setCursors] = useState<(string | null)[]>([null]);
  const [index, setIndex] = useState(0);
  const cursor = cursors[index] ?? null;
  const query = useTenantUsers({
    limit: pageSize,
    ...(status ? { status } : {}),
    ...(cursor ? { cursor } : {}),
  });
  const rows = query.data?.items ?? [];
  const nextCursor = query.data?.nextCursor ?? null;
  const canNext = !!nextCursor && !cursors.slice(0, index + 1).includes(nextCursor);

  function changeStatus(value: string) {
    setStatus(value as TenantMembershipStatus | '');
    setCursors([null]);
    setIndex(0);
  }
  function next() {
    if (!nextCursor || !canNext) return;
    setCursors((current) => [...current.slice(0, index + 1), nextCursor]);
    setIndex(index + 1);
  }

  function membership(row: TenantUser) {
    return (
      <Link
        to="/admin/users/$membershipId"
        params={{ membershipId: row.id }}
        className="text-primary font-medium hover:underline"
      >
        {row.displayName}
      </Link>
    );
  }

  return (
    <section data-testid="admin-users-page">
      <PageHeader title={t('adminUsers.title')} description={t('adminUsers.intro')} />
      <Link
        to="/admin/invitations"
        className="text-primary mb-4 inline-block text-sm font-medium hover:underline"
      >
        {t('adminInvitations.title')} →
      </Link>
      {canManageRoles && (
        <Link
          to="/admin/role-change-requests"
          className="text-primary mb-4 ml-4 inline-block text-sm font-medium hover:underline"
        >
          {t('roleChanges.queueTitle')} →
        </Link>
      )}
      <label className="mb-4 grid max-w-56 gap-1 text-sm">
        <span className="font-medium">{t('adminUsers.statusFilter')}</span>
        <Select
          value={status}
          onChange={(event) => changeStatus(event.target.value)}
          placeholder={t('adminUsers.allStatuses')}
          options={statuses.map((value) => ({
            value,
            label: t(`adminUsers.membershipStatus.${value}`),
          }))}
        />
      </label>
      {query.isPending ? (
        <p className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </p>
      ) : query.isError ? (
        <ProblemAlert
          page
          problem={problemOf(query.error)}
          actions={
            <Button variant="secondary" onClick={() => void query.refetch()}>
              {t('common.retry')}
            </Button>
          }
        />
      ) : rows.length === 0 ? (
        <EmptyState title={t('adminUsers.empty')} />
      ) : !wide ? (
        <ul className="grid gap-2">
          {rows.map((row) => (
            <li
              key={row.id}
              data-testid="admin-user-row"
              className="bg-surface-raised border-line grid gap-1 rounded-lg border p-3 text-sm"
            >
              {membership(row)}
              <span>
                {t('adminUsers.actorType')}: {t(`adminUsers.actorTypeValues.${row.actorType}`)}
              </span>
              <span>
                {t('adminUsers.membership')}:{' '}
                <Badge tone={statusTone(row.membershipStatus)}>
                  {t(`adminUsers.membershipStatus.${row.membershipStatus}`)}
                </Badge>
              </span>
              <span>
                {t('adminUsers.actorStatus')}:{' '}
                {t(`adminUsers.actorStatusValues.${row.actorStatus}`)}
              </span>
              <span>
                {t('adminUsers.validity')}:{' '}
                {row.validityEmpty ? (
                  t('adminUsers.emptyValidity')
                ) : (
                  <>
                    {row.validFrom ? formatDate(row.validFrom) : t('adminUsers.noStart')} —{' '}
                    {row.validTo ? formatDate(row.validTo) : t('adminUsers.noEnd')}
                  </>
                )}
              </span>
            </li>
          ))}
        </ul>
      ) : (
        <div className="overflow-x-auto">
          <Table>
            <THead>
              <TR>
                <TH>{t('adminUsers.name')}</TH>
                <TH>{t('adminUsers.actorType')}</TH>
                <TH>{t('adminUsers.actorStatus')}</TH>
                <TH>{t('adminUsers.membership')}</TH>
                <TH>{t('adminUsers.validity')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((row) => (
                <TR key={row.id} data-testid="admin-user-row">
                  <TD>{membership(row)}</TD>
                  <TD>{t(`adminUsers.actorTypeValues.${row.actorType}`)}</TD>
                  <TD>{t(`adminUsers.actorStatusValues.${row.actorStatus}`)}</TD>
                  <TD>
                    <Badge tone={statusTone(row.membershipStatus)}>
                      {t(`adminUsers.membershipStatus.${row.membershipStatus}`)}
                    </Badge>
                  </TD>
                  <TD>
                    {row.validityEmpty ? (
                      t('adminUsers.emptyValidity')
                    ) : (
                      <>
                        {row.validFrom ? formatDate(row.validFrom) : t('adminUsers.noStart')} —{' '}
                        {row.validTo ? formatDate(row.validTo) : t('adminUsers.noEnd')}
                      </>
                    )}
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
      {index > 0 || canNext ? (
        <nav className="mt-4 flex gap-2" aria-label={t('adminUsers.pages')}>
          <Button
            variant="secondary"
            disabled={index === 0 || query.isPending}
            onClick={() => setIndex(index - 1)}
          >
            {t('adminUsers.previous')}
          </Button>
          <Button variant="secondary" disabled={!canNext || query.isPending} onClick={next}>
            {t('adminUsers.next')}
          </Button>
        </nav>
      ) : null}
    </section>
  );
}
