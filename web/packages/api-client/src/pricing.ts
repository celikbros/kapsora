import type { KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';

export type PriceQuote = components['schemas']['PriceQuote'];
export type PriceQuoteItem = components['schemas']['PriceQuoteItem'];
export type PriceQuoteOutcome = components['schemas']['PriceQuoteOutcome'];
export type PriceQuoteExplanation = components['schemas']['PriceQuoteExplanation'];
export type PriceQuoteRequestItem = components['schemas']['PriceQuoteRequestItem'];
export type CreatePriceQuoteRequest = components['schemas']['CreatePriceQuoteRequest'];

/**
 * Price quotes.
 *
 * A quote says what a service costs, how much of it the plan carries and what the member
 * pays out of pocket. It reserves nothing, moves no balance and authorizes nothing — the
 * `disclaimer` on the response says exactly that, and a screen must show it. Quantities
 * and every amount are exact decimal strings; format them, never parse them.
 *
 * `Idempotency-Key` is optional here and replays the stored quote unchanged, so a retry
 * after a dropped connection does not produce a second one.
 */
export function pricingOperations(client: KapsoraClient) {
  return {
    async createQuote(
      tenantId: string,
      body: CreatePriceQuoteRequest,
      idempotencyKey?: string,
    ): Promise<PriceQuote> {
      const header: { 'X-Tenant-ID': string; 'Idempotency-Key'?: string } = {
        'X-Tenant-ID': tenantId,
      };
      if (idempotencyKey) header['Idempotency-Key'] = idempotencyKey;
      return (await unwrap(client.POST('/api/v1/pricing/quotes', { params: { header }, body })))
        .data;
    },

    /** An expired quote still reads back, with `expired` true and its `expiresAt`. */
    async getQuote(tenantId: string, priceQuoteId: string): Promise<PriceQuote> {
      return (
        await unwrap(
          client.GET('/api/v1/pricing/quotes/{priceQuoteId}', {
            params: { header: { 'X-Tenant-ID': tenantId }, path: { priceQuoteId } },
          }),
        )
      ).data;
    },
  };
}
