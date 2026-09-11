import type {
  Provider,
  ProviderListQuery,
  ProviderStatus,
  ProviderType,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  EmptyState,
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
  statusTone,
} from '@kapsora/ui';
import { useSearch } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useProviderList } from './queries';
import {
  ProviderLink,
  providerListSearch,
  useProviderNavigate,
  type ProviderListSearch,
} from './routes';
import { PROVIDER_STATUSES, PROVIDER_TYPES } from './schema';

export type { ProviderListSearch } from './routes';

const PAGE_SIZE = 50;

/** The contracted network: who the tenant works with, of what kind and in what state. */
export function ProviderListPage() {
  const { t } = useTranslation();
  const navigate = useProviderNavigate();
  const raw: Record<string, unknown> = useSearch({ strict: false });
  const search = providerListSearch(raw);
  const canManage = usePermission('provider.manage');
  const [q, setQ] = useState(search.q ?? '');
  const [trail, setTrail] = useState<string[]>([]);

  const listQuery: ProviderListQuery = {
    ...(search.q ? { q: search.q } : {}),
    ...(search.providerType ? { providerType: search.providerType } : {}),
    ...(search.networkTier ? { networkTier: search.networkTier } : {}),
    ...(search.status ? { status: search.status } : {}),
    ...(search.cursor ? { cursor: search.cursor } : {}),
    limit: PAGE_SIZE,
  };
  const query = useProviderList(listQuery);

  /** Every filter change starts a new page run, so the cursor trail is dropped. */
  function apply(next: ProviderListSearch) {
    setTrail([]);
    void navigate({ to: '/providers', search: next });
  }

  function filtersOnly(): ProviderListSearch {
    const { cursor: _cursor, ...rest } = search;
    return rest;
  }

  function submitSearch(event: FormEvent) {
    event.preventDefault();
    const trimmed = q.trim();
    const { q: _q, ...rest } = filtersOnly();
    apply(trimmed ? { ...rest, q: trimmed } : rest);
  }

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, search.cursor ?? '']);
    void navigate({ to: '/providers', search: { ...search, cursor: next } });
  }

  function goPrevious() {
    const previous = trail[trail.length - 1];
    setTrail((rest) => rest.slice(0, -1));
    const others = filtersOnly();
    void navigate({
      to: '/providers',
      search: previous ? { ...others, cursor: previous } : others,
    });
  }

  const rows: Provider[] = query.data?.items ?? [];
  const filtered = Boolean(search.q ?? search.providerType ?? search.networkTier ?? search.status);

  return (
    <>
      <PageHeader
        title={t('providers.title')}
        description={t('providers.intro')}
        actions={
          canManage ? (
            <ProviderLink
              target={{ to: '/providers/new' }}
              className="bg-primary text-primary-fg hover:bg-primary-strong inline-flex h-10 items-center rounded-md px-4 text-sm font-medium"
            >
              {t('providers.new')}
            </ProviderLink>
          ) : null
        }
      />

      <form onSubmit={submitSearch} className="mb-4 flex flex-wrap items-end gap-2" role="search">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('providers.search')}</span>
          <Input name="q" value={q} onChange={(e) => setQ(e.target.value)} className="w-64" />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('providers.typeFilter')}</span>
          <Select
            name="providerType"
            value={search.providerType ?? ''}
            onChange={(e) => {
              const { providerType: _type, ...rest } = filtersOnly();
              apply(
                e.target.value ? { ...rest, providerType: e.target.value as ProviderType } : rest,
              );
            }}
            placeholder={t('providers.allTypes')}
            options={PROVIDER_TYPES.map((type) => ({
              value: type,
              label: t(`providers.types.${type}`),
            }))}
            className="w-48"
          />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('providers.fields.networkTier')}</span>
          <Input
            name="networkTier"
            value={search.networkTier ?? ''}
            onChange={(e) => {
              const { networkTier: _tier, ...rest } = filtersOnly();
              apply(e.target.value ? { ...rest, networkTier: e.target.value } : rest);
            }}
            className="w-24 font-mono"
          />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('providers.statusFilter')}</span>
          <Select
            name="status"
            value={search.status ?? ''}
            onChange={(e) => {
              const { status: _status, ...rest } = filtersOnly();
              apply(e.target.value ? { ...rest, status: e.target.value as ProviderStatus } : rest);
            }}
            placeholder={t('providers.allStatuses')}
            options={PROVIDER_STATUSES.map((status) => ({
              value: status,
              label: t(`providers.statuses.${status}`),
            }))}
            className="w-48"
          />
        </label>
        <Button type="submit" variant="secondary">
          {t('common.search')}
        </Button>
        {filtered ? (
          <Button
            variant="ghost"
            onClick={() => {
              setQ('');
              apply({});
            }}
          >
            {t('common.clear')}
          </Button>
        ) : null}
      </form>

      <ProblemAlert
        page
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
        <EmptyState title={t('providers.empty')} description={t('providers.emptyHint')} />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="provider-table">
            <THead>
              <TR>
                <TH>{t('providers.columns.name')}</TH>
                <TH>{t('providers.columns.type')}</TH>
                <TH>{t('providers.columns.tier')}</TH>
                <TH>{t('providers.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((provider) => (
                <TR key={provider.id}>
                  <TD>
                    <ProviderLink
                      target={{ to: '/providers/$providerId', params: { providerId: provider.id } }}
                      className="font-medium underline-offset-2 hover:underline"
                    >
                      {provider.organizationName}
                    </ProviderLink>
                  </TD>
                  <TD>{t(`providers.types.${provider.providerType}`)}</TD>
                  <TD>
                    <code className="font-mono text-xs">
                      {provider.networkTier ?? t('common.none')}
                    </code>
                  </TD>
                  <TD>
                    <Badge tone={statusTone(provider.status)}>
                      {t(`providers.statuses.${provider.status}`)}
                    </Badge>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
          <nav
            className="mt-3 flex items-center justify-between text-sm"
            aria-labelledby="provider-page-label"
          >
            <span id="provider-page-label" className="text-fg-muted">
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
