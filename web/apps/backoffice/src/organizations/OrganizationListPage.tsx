import { ApiError, type OrganizationSummary, type RelationshipRole } from '@kapsora/api-client';
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
import {
  createColumnHelper,
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table';
import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { useMemo, useState, type FormEvent } from 'react';
import { RELATIONSHIP_ROLES } from './schema';
import { useOrganizationList } from './queries';

export interface OrganizationListSearch {
  role?: RelationshipRole;
  q?: string;
  cursor?: string;
}

const PAGE_SIZE = 50;

/** Organization list with server-side keyset paging, role filter and search. */
export function OrganizationListPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const search = useSearch({ from: '/app/organizations' });
  const canManage = usePermission('organization.manage');
  const [q, setQ] = useState(search.q ?? '');
  // Cursors of the pages visited so far; index = page number - 1.
  const [trail, setTrail] = useState<string[]>([]);

  const query = useOrganizationList({
    ...(search.role ? { role: search.role } : {}),
    ...(search.q ? { q: search.q } : {}),
    ...(search.cursor ? { cursor: search.cursor } : {}),
    limit: PAGE_SIZE,
  });

  const columnHelper = createColumnHelper<OrganizationSummary>();
  const columns = useMemo(
    () => [
      columnHelper.accessor('displayName', {
        header: t('organizations.columns.displayName'),
        cell: (info) => (
          <Link
            to="/organizations/$organizationId"
            params={{ organizationId: info.row.original.id }}
            className="font-medium underline-offset-2 hover:underline"
          >
            {info.getValue()}
          </Link>
        ),
      }),
      columnHelper.accessor('organizationKind', {
        header: t('organizations.columns.kind'),
        cell: (info) => t(`organizations.kinds.${info.getValue()}`),
      }),
      columnHelper.accessor('relationshipRole', {
        header: t('organizations.columns.role'),
        cell: (info) => t(`organizations.roles.${info.getValue()}`),
      }),
      columnHelper.accessor('relationshipStatus', {
        header: t('organizations.columns.status'),
        cell: (info) => (
          <Badge tone={statusTone(info.getValue())}>
            {t(`organizations.statuses.${info.getValue()}`)}
          </Badge>
        ),
      }),
    ],
    [columnHelper, t],
  );

  const table = useReactTable({
    data: query.data?.items ?? [],
    columns,
    getCoreRowModel: getCoreRowModel(),
  });

  function applyFilters(e: FormEvent) {
    e.preventDefault();
    setTrail([]);
    void navigate({
      to: '/organizations',
      search: {
        ...(search.role ? { role: search.role } : {}),
        ...(q.trim() ? { q: q.trim() } : {}),
      },
    });
  }

  function setRole(role: string) {
    setTrail([]);
    void navigate({
      to: '/organizations',
      search: {
        ...(role ? { role: role as RelationshipRole } : {}),
        ...(search.q ? { q: search.q } : {}),
      },
    });
  }

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((tr) => [...tr, search.cursor ?? '']);
    void navigate({ to: '/organizations', search: { ...search, cursor: next } });
  }

  function goPrev() {
    const prev = trail[trail.length - 1];
    setTrail((tr) => tr.slice(0, -1));
    const { cursor: _c, ...rest } = search;
    void navigate({ to: '/organizations', search: prev ? { ...rest, cursor: prev } : rest });
  }

  const problem = query.error instanceof ApiError ? query.error.problem : null;

  return (
    <>
      <PageHeader
        title={t('organizations.title')}
        description={t('organizations.intro')}
        actions={
          canManage ? (
            <Link
              to="/organizations/new"
              className="bg-primary text-primary-fg hover:bg-primary-strong inline-flex h-10 items-center rounded-md px-4 text-sm font-medium"
            >
              {t('organizations.new')}
            </Link>
          ) : null
        }
      />
      <form onSubmit={applyFilters} className="mb-4 flex flex-wrap items-end gap-2" role="search">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('organizations.search')}</span>
          <Input name="q" value={q} onChange={(e) => setQ(e.target.value)} className="w-64" />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('organizations.roleFilter')}</span>
          <Select
            name="role"
            value={search.role ?? ''}
            onChange={(e) => setRole(e.target.value)}
            placeholder={t('organizations.allRoles')}
            options={RELATIONSHIP_ROLES.map((r) => ({
              value: r,
              label: t(`organizations.roles.${r}`),
            }))}
            className="w-48"
          />
        </label>
        <Button type="submit" variant="secondary">
          {t('common.search')}
        </Button>
        {search.q || search.role ? (
          <Button
            variant="ghost"
            onClick={() => {
              setQ('');
              setTrail([]);
              void navigate({ to: '/organizations', search: {} });
            }}
          >
            {t('common.clear')}
          </Button>
        ) : null}
      </form>

      <ProblemAlert
        page
        problem={problem}
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
        className="mb-4"
      />

      {query.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : query.data && query.data.items.length === 0 ? (
        <EmptyState title={t('organizations.empty')} description={t('organizations.emptyHint')} />
      ) : query.data ? (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="organization-table">
            <THead>
              {table.getHeaderGroups().map((hg) => (
                <TR key={hg.id}>
                  {hg.headers.map((h) => (
                    <TH key={h.id}>{flexRender(h.column.columnDef.header, h.getContext())}</TH>
                  ))}
                </TR>
              ))}
            </THead>
            <TBody>
              {table.getRowModel().rows.map((row) => (
                <TR key={row.id}>
                  {row.getVisibleCells().map((cell) => (
                    <TD key={cell.id}>
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </TD>
                  ))}
                </TR>
              ))}
            </TBody>
          </Table>
          <nav className="mt-3 flex items-center justify-between text-sm" aria-label="Sayfalama">
            <span className="text-fg-muted">
              {t('organizations.page', { n: trail.length + 1 })}
            </span>
            <div className="flex gap-2">
              <Button variant="secondary" size="sm" onClick={goPrev} disabled={trail.length === 0}>
                {t('organizations.prevPage')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={goNext}
                disabled={!query.data.nextCursor}
              >
                {t('organizations.nextPage')}
              </Button>
            </div>
          </nav>
        </>
      ) : null}
    </>
  );
}
