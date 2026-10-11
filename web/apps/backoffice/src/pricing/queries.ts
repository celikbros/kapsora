import type { CreatePriceQuoteRequest, PriceOptionQuery } from '@kapsora/api-client';
import { useSession, useTenantId } from '@kapsora/auth';
import { useInfiniteQuery, useMutation, useQuery } from '@tanstack/react-query';

import { useOps } from '../api';

export const pricingKeys = {
  quote: (tenantId: string, context: string, id: string) =>
    ['pricing', tenantId, context, 'quote', id] as const,
};

/** A quote is a command, never a cached query: it is recorded when it is asked for. */
export function useCreateQuote() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: ({
      body,
      idempotencyKey,
    }: {
      body: CreatePriceQuoteRequest;
      idempotencyKey: string;
    }) => ops.pricing.createQuote(tenantId, body, idempotencyKey),
  });
}

export function useQuote(quoteId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const context = usePricingContext();
  const canQuote = useSession(
    (s) => s.activeTenant?.permissions.includes('pricing.quote') ?? false,
  );
  return useQuery({
    queryKey: pricingKeys.quote(tenantId, context, quoteId),
    queryFn: () => ops.pricing.getQuote(tenantId, quoteId),
    enabled: quoteId !== '' && canQuote,
  });
}

/** Keep pricing inputs isolated by actor and the active tenant grant/scope snapshot. */
function usePricingContext() {
  return useSession(
    (s) =>
      `${s.activeTenant?.tenant.id ?? ''}:${s.session?.actorId ?? ''}:${JSON.stringify(s.activeTenant ?? null)}`,
  );
}

export function useQuotePeople(q: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const context = usePricingContext();
  return useQuery({
    queryKey: ['pricing', tenantId, context, 'people', q],
    queryFn: () => ops.people.list(tenantId, { ...(q ? { q } : {}), status: 'ACTIVE', limit: 20 }),
  });
}

export function usePriceProviderOptions(q: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const context = usePricingContext();
  return useInfiniteQuery({
    queryKey: ['pricing', tenantId, context, 'provider-options', q],
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) =>
      ops.pricing.listProviderOptions(tenantId, {
        ...(q ? { q } : {}),
        ...(pageParam ? { cursor: pageParam } : {}),
        limit: 50,
      } satisfies PriceOptionQuery),
    getNextPageParam: (lastPage, pages, lastPageParam) => {
      const cursor = lastPage.nextCursor;
      return !cursor ||
        cursor === lastPageParam ||
        pages.slice(0, -1).some((page) => page.nextCursor === cursor)
        ? undefined
        : cursor;
    },
  });
}

export function usePriceServiceOptions(q: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const context = usePricingContext();
  return useInfiniteQuery({
    queryKey: ['pricing', tenantId, context, 'service-options', q],
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) =>
      ops.pricing.listServiceOptions(tenantId, {
        ...(q ? { q } : {}),
        ...(pageParam ? { cursor: pageParam } : {}),
        limit: 50,
      } satisfies PriceOptionQuery),
    getNextPageParam: (lastPage, pages, lastPageParam) => {
      const cursor = lastPage.nextCursor;
      return !cursor ||
        cursor === lastPageParam ||
        pages.slice(0, -1).some((page) => page.nextCursor === cursor)
        ? undefined
        : cursor;
    },
  });
}
