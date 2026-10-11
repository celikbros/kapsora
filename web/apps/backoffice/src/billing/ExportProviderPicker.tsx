import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Button, Select, Spinner } from '@kapsora/ui';
import { useInfiniteQuery } from '@tanstack/react-query';
import { useMemo } from 'react';
import { useOps } from '../api';

type Source = 'invoices' | 'settlements' | 'reconciliation';
type ProviderChoice = { id: string; name: string };
type ProviderPage = { items: ProviderChoice[]; nextCursor: string | null };

function sourceFor(permissions: readonly string[]): Source | null {
  if (permissions.includes('invoice.read')) return 'invoices';
  if (permissions.includes('settlement.read')) return 'settlements';
  if (permissions.includes('report.read')) return 'reconciliation';
  return null;
}

/** Read providers only from records this actor is already allowed to list. */
export function useExportProviderChoices(enabled: boolean) {
  const ops = useOps();
  const tenantId = useSession((s) => s.activeTenant?.tenant.id ?? null);
  const actorId = useSession((s) => s.session?.actorId ?? null);
  const permissions = useSession((s) => s.activeTenant?.permissions);
  const source = sourceFor(permissions ?? []);
  const query = useInfiniteQuery({
    queryKey: [
      'billing',
      tenantId,
      actorId,
      'export-provider-choices',
      source,
      permissions?.join('|') ?? '',
    ],
    enabled: enabled && tenantId !== null && actorId !== null && source !== null,
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }): Promise<ProviderPage> => {
      const currentTenantId = tenantId!;
      const cursor = pageParam ?? undefined;
      if (source === 'invoices') {
        const page = await ops.billing.listInvoices(currentTenantId, {
          limit: 100,
          ...(cursor ? { cursor } : {}),
        });
        return {
          items: page.items.map((row) => ({
            id: row.providerOrganizationId,
            name: row.providerName?.trim() || row.providerOrganizationId,
          })),
          nextCursor: page.nextCursor,
        };
      }
      if (source === 'settlements') {
        const page = await ops.billing.listSettlements(currentTenantId, {
          limit: 100,
          ...(cursor ? { cursor } : {}),
        });
        return {
          items: page.items.map((row) => ({
            id: row.providerOrganizationId,
            name: row.providerName?.trim() || row.providerOrganizationId,
          })),
          nextCursor: page.nextCursor,
        };
      }
      if (source === 'reconciliation') {
        const page = await ops.report.listRuns(currentTenantId, {
          limit: 100,
          scope: 'PROVIDER',
          ...(cursor ? { cursor } : {}),
        });
        return {
          items: page.items.flatMap((row) =>
            row.providerOrganizationId
              ? [
                  {
                    id: row.providerOrganizationId,
                    name: row.providerName?.trim() || row.providerOrganizationId,
                  },
                ]
              : [],
          ),
          nextCursor: page.nextCursor,
        };
      }
      return { items: [], nextCursor: null };
    },
    getNextPageParam: (lastPage, pages, lastPageParam) => {
      const cursor = lastPage.nextCursor;
      if (
        !cursor ||
        cursor === lastPageParam ||
        pages.slice(0, -1).some((page) => page.nextCursor === cursor)
      )
        return undefined;
      return cursor;
    },
  });
  const choices = useMemo(() => {
    const names = new Map<string, string>();
    for (const page of query.data?.pages ?? []) {
      for (const item of page.items) {
        if (!names.has(item.id) || names.get(item.id) === item.id) names.set(item.id, item.name);
      }
    }
    return [...names].map(([id, name]) => ({ id, name }));
  }, [query.data]);
  return { ...query, choices, source };
}

export function ExportProviderPicker({
  value,
  onChange,
  placeholder,
  selectedName,
}: {
  value: string;
  onChange: (id: string) => void;
  placeholder: string;
  selectedName?: string | undefined;
}) {
  const { t } = useTranslation();
  const providers = useExportProviderChoices(true);
  const options = providers.choices.map((item) => ({ value: item.id, label: item.name }));
  if (value && !options.some((item) => item.value === value)) {
    options.push({ value, label: selectedName || value });
  }
  return (
    <div className="grid gap-2">
      <Select
        data-testid="export-provider-select"
        name="providerOrganizationId"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        options={options}
        placeholder={placeholder}
      />
      <p className="text-fg-muted text-xs">{t('billing.report.providerSourceHint')}</p>
      {providers.source !== null && providers.isPending ? (
        <p className="text-fg-muted flex items-center gap-2 text-xs" aria-busy="true">
          <Spinner />
          {t('common.loading')}
        </p>
      ) : null}
      {providers.isError ? (
        <Button
          type="button"
          size="sm"
          variant="secondary"
          onClick={() =>
            void (providers.isFetchNextPageError ? providers.fetchNextPage() : providers.refetch())
          }
          data-testid="retry-export-providers"
        >
          {t('billing.report.retryProviders')}
        </Button>
      ) : null}
      {(providers.source === null || providers.isSuccess) && options.length === 0 ? (
        <p className="text-fg-muted text-xs">{t('billing.report.providersEmpty')}</p>
      ) : null}
      {providers.hasNextPage && !providers.isError ? (
        <Button
          type="button"
          size="sm"
          variant="secondary"
          loading={providers.isFetchingNextPage}
          onClick={() => void providers.fetchNextPage()}
          data-testid="load-more-export-providers"
        >
          {t('billing.report.loadMoreProviders')}
        </Button>
      ) : null}
    </div>
  );
}
