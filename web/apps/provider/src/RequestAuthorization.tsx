import { useTenantId } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import { Badge, Button, Card, ProblemAlert, Spinner } from '@kapsora/ui';
import { useQuery } from '@tanstack/react-query';

import { problemOf } from './problems';
import { useOps } from './services';

export function RequestAuthorization({ requestId }: { requestId: string }) {
  const { t } = useTranslation();
  const ops = useOps();
  const tenantId = useTenantId();
  const query = useQuery({
    queryKey: ['provider', tenantId, 'authorizations', requestId],
    queryFn: () => ops.authorizations.list(tenantId, { requestId, limit: 100 }),
  });
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
      ) : query.data?.items.length ? (
        <ul className="divide-line mt-3 divide-y">
          {query.data.items.map((authorization) => (
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
        <p className="text-fg-muted mt-2 text-sm">{t('authorization.providerEmpty')}</p>
      )}
    </Card>
  );
}
