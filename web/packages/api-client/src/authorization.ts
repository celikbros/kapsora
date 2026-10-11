import type { KapsoraClient } from './client';
import type { components, operations } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned } from './versioned';

export type Authorization = components['schemas']['Authorization'];
export type CreateAuthorization = components['schemas']['CreateAuthorization'];
export type AuthorizationListQuery = NonNullable<
  operations['listAuthorizations']['parameters']['query']
>;

export function authorizationOperations(client: KapsoraClient) {
  return {
    async list(tenantId: string, query: AuthorizationListQuery = {}) {
      return (
        await unwrap(
          client.GET('/api/v1/authorizations', {
            params: { header: { 'X-Tenant-ID': tenantId }, query },
          }),
        )
      ).data;
    },
    // The caller keeps the same key and body while retrying an uncertain outcome.
    async create(tenantId: string, body: CreateAuthorization, idempotencyKey: string) {
      const result = await unwrap(
        client.POST('/api/v1/authorizations', {
          params: { header: { 'X-Tenant-ID': tenantId, 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(result.data, result.response);
    },
  };
}
