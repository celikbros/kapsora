import { ApiError, type PersonStatus, type PersonSummary } from '@kapsora/api-client';
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
import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { IdentifierSearchDialog } from './IdentifierSearchDialog';
import { usePersonList } from './queries';

export interface PersonListSearch {
  q?: string;
  status?: PersonStatus;
  cursor?: string;
}

const PAGE_SIZE = 50;
const STATUSES: PersonStatus[] = ['ACTIVE', 'INACTIVE', 'DECEASED', 'MERGED'];

/** The member list: name search, status filter and the audited identifier search. */
export function PersonListPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const search = useSearch({ from: '/app/people' });
  const canManage = usePermission('member.manage');
  const canSearchIdentifier = usePermission('member.identifier.search');
  const [q, setQ] = useState(search.q ?? '');
  const [identifierOpen, setIdentifierOpen] = useState(false);
  const [trail, setTrail] = useState<string[]>([]);

  const query = usePersonList({
    ...(search.q ? { q: search.q } : {}),
    ...(search.status ? { status: search.status } : {}),
    ...(search.cursor ? { cursor: search.cursor } : {}),
    limit: PAGE_SIZE,
  });

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    setTrail([]);
    void navigate({
      to: '/people',
      search: {
        ...(q.trim() ? { q: q.trim() } : {}),
        ...(search.status ? { status: search.status } : {}),
      },
    });
  }

  function setStatus(status: string) {
    setTrail([]);
    void navigate({
      to: '/people',
      search: {
        ...(search.q ? { q: search.q } : {}),
        ...(status ? { status: status as PersonStatus } : {}),
      },
    });
  }

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, search.cursor ?? '']);
    void navigate({ to: '/people', search: { ...search, cursor: next } });
  }

  function goPrevious() {
    const previous = trail[trail.length - 1];
    setTrail((rest) => rest.slice(0, -1));
    const { cursor: _cursor, ...others } = search;
    void navigate({
      to: '/people',
      search: previous ? { ...others, cursor: previous } : others,
    });
  }

  const problem = query.error instanceof ApiError ? query.error.problem : null;
  const rows: PersonSummary[] = query.data?.items ?? [];

  return (
    <>
      <PageHeader
        title={t('people.title')}
        description={t('people.intro')}
        actions={
          <>
            {canSearchIdentifier ? (
              <Button variant="secondary" onClick={() => setIdentifierOpen(true)}>
                {t('people.identifierSearch')}
              </Button>
            ) : null}
            {canManage ? (
              <Link
                to="/people/new"
                className="bg-primary text-primary-fg hover:bg-primary-strong inline-flex h-10 items-center rounded-md px-4 text-sm font-medium"
              >
                {t('people.new')}
              </Link>
            ) : null}
          </>
        }
      />

      <form onSubmit={applyFilters} className="mb-4 flex flex-wrap items-end gap-2" role="search">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('people.search')}</span>
          <Input
            name="q"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            className="w-64"
            aria-describedby="people-search-hint"
          />
          <span id="people-search-hint" className="text-fg-muted text-xs">
            {t('people.searchHint')}
          </span>
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('people.statusFilter')}</span>
          <Select
            name="status"
            value={search.status ?? ''}
            onChange={(e) => setStatus(e.target.value)}
            placeholder={t('people.allStatuses')}
            options={STATUSES.map((status) => ({
              value: status,
              label: t(`people.statuses.${status}`),
            }))}
            className="w-48"
          />
        </label>
        <Button type="submit" variant="secondary">
          {t('common.search')}
        </Button>
        {search.q || search.status ? (
          <Button
            variant="ghost"
            onClick={() => {
              setQ('');
              setTrail([]);
              void navigate({ to: '/people', search: {} });
            }}
          >
            {t('common.clear')}
          </Button>
        ) : null}
      </form>

      <ProblemAlert
        page
        problem={problem}
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
        <EmptyState title={t('people.empty')} description={t('people.emptyHint')} />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="person-table">
            <THead>
              <TR>
                <TH>{t('people.columns.displayName')}</TH>
                <TH>{t('people.columns.identifier')}</TH>
                <TH>{t('people.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((person) => (
                <TR key={person.id}>
                  <TD>
                    <Link
                      to="/people/$personId"
                      params={{ personId: person.id }}
                      className="font-medium underline-offset-2 hover:underline"
                    >
                      {person.displayName}
                    </Link>
                  </TD>
                  <TD>
                    <code className="font-mono text-xs">
                      {person.maskedPrimaryIdentifier ?? t('common.none')}
                    </code>
                  </TD>
                  <TD>
                    <Badge tone={statusTone(person.status)}>
                      {t(`people.statuses.${person.status}`)}
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

      <IdentifierSearchDialog open={identifierOpen} onClose={() => setIdentifierOpen(false)} />
    </>
  );
}
