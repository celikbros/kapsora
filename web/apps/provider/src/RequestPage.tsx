import { formatDate, formatDateTime, useTranslation } from '@kapsora/i18n';
import { Badge, Breadcrumb, Button, Card, PageHeader, ProblemAlert, Spinner } from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';

import { DocumentsPanel } from './documents';
import { usePersonName, useRequest, useServiceName } from './queries';
import { problemOf } from './problems';

/**
 * One of the provider's requests: what it is, what it is waiting for, and — when it is
 * waiting for a document — the place to attach it. The state a file is in is shown as
 * the server reports it; nothing here is optimistic.
 */
export function RequestPage() {
  const { t } = useTranslation();
  const { requestId } = useParams({ from: '/app/requests/$requestId' });
  const query = useRequest(requestId);
  const personName = usePersonName(query.data?.data.personId);
  const serviceName = useServiceName(query.data?.data.items[0]?.serviceDefinitionId);

  if (query.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (query.error || !query.data) {
    return (
      <ProblemAlert
        problem={problemOf(query.error)}
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
    );
  }
  const request = query.data.data;
  const waiting = request.status === 'PENDING_DOCUMENT';
  const closed =
    request.status === 'REJECTED' ||
    request.status === 'CANCELLED' ||
    request.status === 'EXPIRED' ||
    request.status === 'CLOSED';

  return (
    <>
      <PageHeader
        title={request.reference}
        description={[personName, serviceName].filter(Boolean).join(' · ') || undefined}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('provider.myRequests.title'),
                render: (label) => <Link to="/requests">{label}</Link>,
              },
              { label: request.reference },
            ]}
          />
        }
        actions={
          <Badge tone={waiting ? 'warning' : request.status === 'REJECTED' ? 'danger' : 'info'}>
            {t(`requests.status.${request.status}`)}
          </Badge>
        }
      />
      <div className="grid gap-4">
        <Card>
          <dl className="grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-1 text-sm">
            <dt className="text-fg-muted">{t('provider.myRequests.waitingFor')}</dt>
            <dd className={waiting ? 'font-medium' : undefined}>
              {t(`provider.myRequests.waiting.${request.status}`)}
            </dd>
            <dt className="text-fg-muted">{t('requests.columns.serviceDate')}</dt>
            <dd>{formatDate(request.serviceDate)}</dd>
            {request.submittedAt ? (
              <>
                <dt className="text-fg-muted">{t('requests.columns.submittedAt')}</dt>
                <dd>{formatDateTime(request.submittedAt)}</dd>
              </>
            ) : null}
            {request.returnReasonCode ? (
              <>
                <dt className="text-fg-muted">{t('requests.reasonCode')}</dt>
                <dd>
                  <code className="font-mono text-xs">{request.returnReasonCode}</code>
                </dd>
              </>
            ) : null}
            {request.rejectReasonCode ? (
              <>
                <dt className="text-fg-muted">{t('requests.reasonCode')}</dt>
                <dd>
                  <code className="font-mono text-xs">{request.rejectReasonCode}</code>
                </dd>
              </>
            ) : null}
          </dl>
        </Card>
        <Card>
          <h2 className="text-base font-semibold">{t('provider.upload.title')}</h2>
          {waiting ? (
            <p className="text-fg-muted mb-3 mt-1 text-sm">{t('provider.upload.intro')}</p>
          ) : null}
          {closed ? (
            <p className="text-fg-muted mb-3 mt-1 text-sm">{t('documents.readOnly')}</p>
          ) : null}
          <DocumentsPanel
            aggregateType="SERVICE_REQUEST"
            aggregateId={request.id}
            requiredTypes={request.requiredDocumentTypes}
            readOnly={closed}
          />
        </Card>
      </div>
    </>
  );
}
