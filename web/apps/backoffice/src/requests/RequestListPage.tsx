import type {
  ServiceRequest,
  ServiceRequestChannel,
  ServiceRequestStatus,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
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
} from '@kapsora/ui';
import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { useState } from 'react';

import { problemOf } from '../problems';
import { useRequests } from './queries';
import { requestTone } from './status';

export interface RequestListSearch {
  status?: ServiceRequestStatus;
  channel?: ServiceRequestChannel;
  cursor?: string;
}

const PAGE_SIZE = 50;
export const REQUEST_STATUSES: ServiceRequestStatus[] = [
  'DRAFT',
  'PENDING_DOCUMENT',
  'PENDING_REVIEW',
  'APPROVED',
  'PARTIALLY_APPROVED',
  'ELIGIBILITY_FAILED',
  'REJECTED',
  'CANCELLED',
  'EXPIRED',
  'CLOSED',
];
export const REQUEST_CHANNELS: ServiceRequestChannel[] = [
  'BACKOFFICE',
  'PROVIDER_PORTAL',
  'MEMBER_PORTAL',
  'API',
  'BATCH_IMPORT',
  'CALL_CENTER',
];

/**
 * The two name cells. Both read the name the wire now carries (WP-I5-05 section 2.6)
 * rather than resolving it per row: fifty rows used to be fifty extra reads, and a list
 * that named nobody until they all came back.
 *
 * The cell semantics are unchanged, and are still worth keeping: a name that has not
 * arrived is '…' and one the caller may not see is '—'. On this screen the first no longer
 * happens, because the name arrives with the row.
 */
function NameCell({ displayName }: { displayName: string | null | undefined }) {
  return <>{displayName === undefined ? '…' : (displayName ?? '—')}</>;
}

function ProviderCell({
  organizationId,
  displayName,
}: {
  organizationId: string | null | undefined;
  displayName: string | null | undefined;
}) {
  const { t } = useTranslation();
  if (!organizationId) return <span className="text-fg-muted">{t('common.none')}</span>;
  return <>{displayName === undefined ? '…' : (displayName ?? '—')}</>;
}

/** Requests, as a list an operator filters down to what needs them today. */
export function RequestListPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const search = useSearch({ from: '/app/requests' });
  const canCreate = usePermission('service_request.create');
  const [trail, setTrail] = useState<string[]>([]);

  const query = useRequests({
    ...(search.status ? { status: search.status } : {}),
    ...(search.channel ? { channel: search.channel } : {}),
    ...(search.cursor ? { cursor: search.cursor } : {}),
    limit: PAGE_SIZE,
  });

  function setFilter(next: { [K in keyof RequestListSearch]?: RequestListSearch[K] | undefined }) {
    setTrail([]);
    const merged = { ...search, ...next };
    const { cursor: _cursor, ...rest } = merged;
    void navigate({
      to: '/requests',
      search: Object.fromEntries(Object.entries(rest).filter(([, v]) => v)) as RequestListSearch,
    });
  }

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, search.cursor ?? '']);
    void navigate({ to: '/requests', search: { ...search, cursor: next } });
  }

  function goPrevious() {
    const previous = trail[trail.length - 1];
    setTrail((rest) => rest.slice(0, -1));
    const { cursor: _cursor, ...others } = search;
    void navigate({
      to: '/requests',
      search: previous ? { ...others, cursor: previous } : others,
    });
  }

  const rows: ServiceRequest[] = query.data?.items ?? [];

  return (
    <>
      <PageHeader
        title={t('requests.title')}
        description={t('requests.intro')}
        actions={
          canCreate ? (
            <Link
              to="/requests/new"
              className="bg-primary text-primary-fg hover:bg-primary-strong inline-flex h-10 items-center rounded-md px-4 text-sm font-medium"
            >
              {t('requests.new')}
            </Link>
          ) : null
        }
      />

      <form
        className="mb-4 flex flex-wrap items-end gap-2"
        role="search"
        onSubmit={(e) => e.preventDefault()}
      >
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('requests.filters.status')}</span>
          <Select
            name="status"
            value={search.status ?? ''}
            onChange={(e) =>
              setFilter({
                status: (e.target.value || undefined) as ServiceRequestStatus | undefined,
              })
            }
            placeholder={t('requests.filters.all')}
            options={REQUEST_STATUSES.map((s) => ({ value: s, label: t(`requests.status.${s}`) }))}
            className="w-52"
          />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('requests.filters.channel')}</span>
          <Select
            name="channel"
            value={search.channel ?? ''}
            onChange={(e) =>
              setFilter({
                channel: (e.target.value || undefined) as ServiceRequestChannel | undefined,
              })
            }
            placeholder={t('requests.filters.all')}
            options={REQUEST_CHANNELS.map((c) => ({ value: c, label: t(`requests.channel.${c}`) }))}
            className="w-52"
          />
        </label>
        {search.status || search.channel ? (
          <Button
            variant="ghost"
            onClick={() => setFilter({ status: undefined, channel: undefined })}
          >
            {t('common.clear')}
          </Button>
        ) : null}
      </form>

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
        <EmptyState title={t('requests.empty')} />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="request-table">
            <THead>
              <TR>
                <TH>{t('requests.columns.reference')}</TH>
                <TH>{t('requests.columns.status')}</TH>
                <TH>{t('requests.columns.person')}</TH>
                <TH>{t('requests.columns.provider')}</TH>
                <TH>{t('requests.columns.type')}</TH>
                <TH>{t('requests.columns.channel')}</TH>
                <TH>{t('requests.columns.serviceDate')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((request) => (
                <TR key={request.id}>
                  <TD>
                    <Link
                      to="/requests/$requestId"
                      params={{ requestId: request.id }}
                      className="font-mono text-xs font-medium underline-offset-2 hover:underline"
                    >
                      {request.reference}
                    </Link>
                  </TD>
                  <TD>
                    <Badge tone={requestTone(request.status)}>
                      {t(`requests.status.${request.status}`)}
                    </Badge>
                  </TD>
                  <TD>
                    <NameCell displayName={request.personDisplayName} />
                  </TD>
                  <TD>
                    <ProviderCell
                      organizationId={request.providerOrganizationId}
                      displayName={request.providerDisplayName}
                    />
                  </TD>
                  <TD>{t(`requests.type.${request.requestType}`)}</TD>
                  <TD>{t(`requests.channel.${request.channel}`)}</TD>
                  <TD>{formatDate(request.serviceDate)}</TD>
                </TR>
              ))}
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
