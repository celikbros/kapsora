import type { ServiceDefinition, ServiceDomain } from '@kapsora/api-client';
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
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useCategoryTree, useServiceDefinitions } from './queries';
import { SERVICE_DOMAINS } from './schema';

const PAGE_SIZE = 50;

interface Filters {
  q: string;
  categoryId: string;
  domain: string;
  active: string;
}

const NO_FILTERS: Filters = { q: '', categoryId: '', domain: '', active: '' };

/**
 * The service definitions of the tenant. Every later transaction — a request, a price, a
 * claim, a rule — points at one of these rows, so the list is built for finding one fast:
 * a name or code search, the category and domain it lives under, and whether it is still
 * offered.
 */
export function DefinitionListPage() {
  const { t } = useTranslation();
  const canManage = usePermission('catalog.manage');
  const categories = useCategoryTree();

  const [draftQ, setDraftQ] = useState('');
  const [filters, setFilters] = useState<Filters>(NO_FILTERS);
  const [cursor, setCursor] = useState<string | null>(null);
  // Cursors are opaque, so going back means remembering the ones already used.
  const [trail, setTrail] = useState<(string | null)[]>([]);

  const query = useServiceDefinitions({
    limit: PAGE_SIZE,
    ...(filters.q ? { q: filters.q } : {}),
    ...(filters.categoryId ? { categoryId: filters.categoryId } : {}),
    ...(filters.domain ? { domain: filters.domain as ServiceDomain } : {}),
    ...(filters.active ? { active: filters.active === 'true' } : {}),
    ...(cursor ? { cursor } : {}),
  });

  function apply(next: Filters) {
    setFilters(next);
    setCursor(null);
    setTrail([]);
  }

  function search(event: FormEvent) {
    event.preventDefault();
    apply({ ...filters, q: draftQ.trim() });
  }

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, cursor]);
    setCursor(next);
  }

  function goPrevious() {
    setCursor(trail[trail.length - 1] ?? null);
    setTrail((rest) => rest.slice(0, -1));
  }

  const rows: ServiceDefinition[] = query.data?.items ?? [];
  const categoryOptions = (categories.data ?? []).map((category) => ({
    value: category.id,
    label: `${category.code} · ${category.name}`,
  }));
  const filtered =
    filters.q !== '' || filters.categoryId !== '' || filters.domain !== '' || filters.active !== '';

  return (
    <>
      <PageHeader
        title={t('catalog.definitions.title')}
        description={t('catalog.intro')}
        actions={
          canManage ? (
            <Link
              to="/catalog/definitions/new"
              className="bg-primary text-primary-fg hover:bg-primary-strong inline-flex h-10 items-center rounded-md px-4 text-sm font-medium"
            >
              {t('catalog.definitions.new')}
            </Link>
          ) : null
        }
      />

      <form onSubmit={search} className="mb-4 flex flex-wrap items-end gap-2" role="search">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('catalog.search')}</span>
          <Input
            name="q"
            value={draftQ}
            onChange={(event) => setDraftQ(event.target.value)}
            className="w-64"
          />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('catalog.fields.category')}</span>
          <Select
            name="categoryId"
            value={filters.categoryId}
            onChange={(event) => apply({ ...filters, categoryId: event.target.value })}
            placeholder={t('catalog.allStatuses')}
            options={categoryOptions}
            className="w-64"
          />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('catalog.fields.domain')}</span>
          <Select
            name="domain"
            value={filters.domain}
            onChange={(event) => apply({ ...filters, domain: event.target.value })}
            placeholder={t('catalog.allStatuses')}
            options={SERVICE_DOMAINS.map((domain) => ({
              value: domain,
              label: t(`catalog.domains.${domain}`),
            }))}
            className="w-48"
          />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('catalog.statusFilter')}</span>
          <Select
            name="active"
            value={filters.active}
            onChange={(event) => apply({ ...filters, active: event.target.value })}
            placeholder={t('catalog.allStatuses')}
            options={[
              { value: 'true', label: t('catalog.active') },
              { value: 'false', label: t('catalog.inactive') },
            ]}
            className="w-40"
          />
        </label>
        <Button type="submit" variant="secondary">
          {t('common.search')}
        </Button>
        {filtered ? (
          <Button
            variant="ghost"
            onClick={() => {
              setDraftQ('');
              apply(NO_FILTERS);
            }}
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
        <EmptyState
          title={t('catalog.definitions.empty')}
          description={t('catalog.definitions.emptyHint')}
        />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="definition-table">
            <THead>
              <TR>
                <TH>{t('catalog.fields.code')}</TH>
                <TH>{t('catalog.fields.name')}</TH>
                <TH>{t('catalog.fields.category')}</TH>
                <TH>{t('catalog.fields.domain')}</TH>
                <TH>{t('catalog.fields.fulfillmentMode')}</TH>
                <TH>{t('catalog.fields.active')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((definition) => (
                <TR key={definition.id}>
                  <TD>
                    <code className="font-mono text-xs">{definition.code}</code>
                  </TD>
                  <TD>
                    <Link
                      to="/catalog/definitions/$definitionId"
                      params={{ definitionId: definition.id }}
                      className="font-medium"
                    >
                      {definition.name}
                    </Link>
                  </TD>
                  <TD>
                    <code className="font-mono text-xs">{definition.categoryCode}</code>
                  </TD>
                  <TD>{t(`catalog.domains.${definition.domain}`)}</TD>
                  <TD>{t(`catalog.fulfillment.${definition.fulfillmentMode}`)}</TD>
                  <TD>
                    <Badge tone={definition.active ? 'success' : 'neutral'}>
                      {definition.active ? t('catalog.active') : t('catalog.inactive')}
                    </Badge>
                  </TD>
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
