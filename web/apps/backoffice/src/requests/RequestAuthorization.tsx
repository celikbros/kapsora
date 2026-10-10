import { ApiError, type CreateAuthorization, type ServiceRequest } from '@kapsora/api-client';
import { usePermission, useTenantId } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import { Badge, Button, Card, FormField, Input, ProblemAlert, Spinner } from '@kapsora/ui';
import { useMutation, useQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';

import { useOps } from '../api';
import { problemOf } from '../problems';

/** Medical approval and reserving the member's entitlement are separate decisions. */
export function RequestAuthorization({ request }: { request: ServiceRequest }) {
  const { t } = useTranslation();
  const ops = useOps();
  const tenantId = useTenantId();
  const canManage = usePermission('authorization.manage');
  const [validTo, setValidTo] = useState('');
  const [dateError, setDateError] = useState(false);
  const [attempt, setAttempt] = useState<CreateAuthorization | null>(null);
  const query = useQuery({
    queryKey: ['authorizations', tenantId, request.id],
    queryFn: () => ops.authorizations.list(tenantId, { requestId: request.id, limit: 100 }),
  });
  const create = useMutation({
    mutationFn: (body: CreateAuthorization) =>
      ops.authorizations.create(
        tenantId,
        body,
        `request-authorization:${request.id}:v${request.currentVersionNo}`,
      ),
    onError: (error) => {
      // A rejected validation can be corrected; a network failure may have committed.
      if (error instanceof ApiError && error.status === 422) setAttempt(null);
    },
    onSettled: () => query.refetch(),
  });
  const listed = query.data?.items ?? [];
  const confirmed = create.data?.data;
  // A successful command is authoritative even if a following read is temporarily stale.
  const authorizations =
    confirmed && !listed.some((a) => a.id === confirmed.id) ? [confirmed, ...listed] : listed;
  const approved = request.status === 'APPROVED' || request.status === 'PARTIALLY_APPROVED';

  function submit(event: FormEvent) {
    event.preventDefault();
    const deadline = new Date(validTo);
    if (!attempt && (!Number.isFinite(deadline.getTime()) || deadline.getTime() <= Date.now())) {
      setDateError(true);
      return;
    }
    // Freeze an uncertain submission, including across the automatic list refresh.
    // The deterministic key also prevents a refresh/double click creating another hold.
    const body = attempt ?? { requestId: request.id, validTo: deadline.toISOString() };
    setAttempt(body);
    setDateError(false);
    create.mutate(body);
  }

  return (
    <Card data-testid="request-authorization">
      <h2 className="text-base font-semibold">{t('authorization.title')}</h2>
      {query.isPending ? (
        <p className="text-fg-muted mt-3 flex items-center gap-2 text-sm" role="status">
          <Spinner /> {t('common.loading')}
        </p>
      ) : query.error ? (
        <div className="mt-3">
          <ProblemAlert
            problem={problemOf(query.error)}
            actions={
              <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
                {t('common.retry')}
              </Button>
            }
          />
        </div>
      ) : authorizations.length ? (
        <ul className="divide-line mt-3 divide-y">
          {authorizations.map((authorization) => (
            <li
              key={authorization.id}
              className="flex flex-wrap items-start justify-between gap-3 py-3 first:pt-0 last:pb-0"
            >
              <div className="min-w-0">
                <p className="break-words font-mono text-sm">{authorization.reference}</p>
                <p className="text-fg-muted mt-1 text-sm">
                  {t('authorization.validUntil', { date: formatDateTime(authorization.validTo) })}
                </p>
              </div>
              <Badge
                tone={
                  authorization.status === 'CANCELLED' || authorization.status === 'EXPIRED'
                    ? 'neutral'
                    : 'info'
                }
              >
                {t(`authorization.status.${authorization.status}`)}
              </Badge>
            </li>
          ))}
        </ul>
      ) : (
        <>
          <p className="text-fg-muted mt-2 text-sm">
            {t(approved ? 'authorization.empty' : 'authorization.none')}
          </p>
          {approved && canManage ? (
            <form
              className="mt-4 grid gap-3 sm:grid-cols-[minmax(0,20rem)_max-content] sm:items-end"
              onSubmit={submit}
            >
              <FormField
                label={t('authorization.deadline')}
                required
                error={dateError ? t('authorization.futureDate') : undefined}
              >
                <Input
                  type="datetime-local"
                  required
                  value={validTo}
                  disabled={create.isPending || attempt !== null}
                  onChange={(event) => {
                    setValidTo(event.target.value);
                    setDateError(false);
                  }}
                />
              </FormField>
              <Button type="submit" disabled={create.isPending || query.isFetching}>
                {create.isPending
                  ? t('authorization.reserving')
                  : attempt
                    ? t('authorization.retry')
                    : t('authorization.reserve')}
              </Button>
              <p className="text-fg-muted text-sm sm:col-span-2">{t('authorization.effect')}</p>
              {create.error ? (
                <div className="sm:col-span-2">
                  <ProblemAlert problem={problemOf(create.error)} />
                </div>
              ) : null}
            </form>
          ) : null}
        </>
      )}
    </Card>
  );
}
