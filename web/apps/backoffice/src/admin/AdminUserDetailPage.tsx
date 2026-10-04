import { formatDate, formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  EmptyState,
  PageHeader,
  ProblemAlert,
  Spinner,
  statusTone,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { problemOf } from '../problems';
import { SuspendMembership } from './SuspendMembership';
import { useTenantUser } from './queries';

export function AdminUserDetailPage() {
  const { t } = useTranslation();
  const { membershipId } = useParams({ from: '/app/admin/users/$membershipId' });
  const query = useTenantUser(membershipId);
  const detail = query.data?.data;
  return (
    <section data-testid="admin-user-detail-page">
      <Link to="/admin" className="text-primary mb-4 inline-block text-sm hover:underline">
        ← {t('adminUsers.back')}
      </Link>
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
      ) : !detail ? (
        <EmptyState title={t('adminUsers.notFound')} />
      ) : (
        <>
          <div className="min-w-0 break-words">
            <PageHeader
              title={detail.membership.displayName}
              description={t('adminUsers.detailIntro')}
            />
          </div>
          <dl className="border-line bg-surface grid gap-3 rounded-lg border p-4 text-sm sm:grid-cols-2">
            <div>
              <dt className="text-fg-muted">{t('adminUsers.actorType')}</dt>
              <dd>{t(`adminUsers.actorTypeValues.${detail.membership.actorType}`)}</dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('adminUsers.actorStatus')}</dt>
              <dd>{t(`adminUsers.actorStatusValues.${detail.membership.actorStatus}`)}</dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('adminUsers.membership')}</dt>
              <dd>
                <Badge tone={statusTone(detail.membership.membershipStatus)}>
                  {t(`adminUsers.membershipStatus.${detail.membership.membershipStatus}`)}
                </Badge>
              </dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('adminUsers.validFrom')}</dt>
              <dd>
                {detail.membership.validityEmpty
                  ? t('adminUsers.emptyValidity')
                  : detail.membership.validFrom
                    ? formatDate(detail.membership.validFrom)
                    : t('adminUsers.noStart')}
              </dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('adminUsers.validTo')}</dt>
              <dd>
                {detail.membership.validityEmpty
                  ? t('adminUsers.emptyValidity')
                  : detail.membership.validTo
                    ? formatDate(detail.membership.validTo)
                    : t('adminUsers.noEnd')}
              </dd>
            </div>
          </dl>
          <SuspendMembership
            membershipId={membershipId}
            detail={detail}
            etag={query.data?.etag ?? ''}
            onReload={async () => (await query.refetch()).isSuccess}
          />
          <h2 className="mt-6 text-lg font-semibold">{t('adminUsers.roles')}</h2>
          <p className="text-fg-muted mb-3 text-sm">{t('adminUsers.rolesHint')}</p>
          {detail.assignedRoles.length === 0 ? (
            <EmptyState title={t('adminUsers.noRoles')} />
          ) : (
            <ul className="grid gap-2">
              {detail.assignedRoles.map((role, index) => (
                <li
                  key={`${role.code}:${role.scopeType}:${role.validFrom ?? ''}:${index}`}
                  data-testid="admin-assigned-role"
                  className="border-line bg-surface grid min-w-0 gap-2 rounded-lg border p-4 text-sm sm:grid-cols-2"
                >
                  <div className="min-w-0 break-words">
                    <strong>{role.name}</strong>{' '}
                    <code className="text-fg-muted break-all text-xs">{role.code}</code>
                  </div>
                  <div className="min-w-0 break-words">
                    {t('adminUsers.scope')}: {t(`adminUsers.scopes.${role.scopeType}`)}
                  </div>
                  <div className="min-w-0 break-words">
                    {t('adminUsers.roleKind')}:{' '}
                    {role.isSystemRole ? t('adminUsers.systemRole') : t('adminUsers.localRole')}
                  </div>
                  <div className="min-w-0 break-words">
                    {t('adminUsers.validity')}:{' '}
                    {role.validityEmpty ? (
                      t('adminUsers.emptyValidity')
                    ) : (
                      <>
                        {role.validFrom ? formatDateTime(role.validFrom) : t('adminUsers.noStart')}{' '}
                        — {role.validTo ? formatDateTime(role.validTo) : t('adminUsers.noEnd')}
                      </>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </section>
  );
}
