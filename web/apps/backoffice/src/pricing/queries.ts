import type { CreatePriceQuoteRequest } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery } from '@tanstack/react-query';

import { useOps } from '../api';

export const pricingKeys = {
  quote: (tenantId: string, id: string) => ['pricing', tenantId, 'quote', id] as const,
  providers: (tenantId: string) => ['pricing', tenantId, 'providerOptions'] as const,
};

/** A quote is a command, never a cached query: it is recorded when it is asked for. */
export function useCreateQuote() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (body: CreatePriceQuoteRequest) => ops.pricing.createQuote(tenantId, body),
  });
}

export function useQuote(quoteId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: pricingKeys.quote(tenantId, quoteId),
    queryFn: () => ops.pricing.getQuote(tenantId, quoteId),
    enabled: quoteId !== '',
  });
}

/** Active providers as picker options. */
export function useProviderOptions() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: pricingKeys.providers(tenantId),
    queryFn: async () => {
      const page = await ops.providers.list(tenantId, { status: 'ACTIVE', limit: 100 });
      return page.items.map((provider) => ({
        value: provider.id,
        label: provider.organizationName,
      }));
    },
  });
}
