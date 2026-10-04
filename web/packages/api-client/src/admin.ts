import type { components } from './generated/kapsora-v1';
import type { KapsoraClient } from './client';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type TenantUser = components['schemas']['TenantUser'];
export type TenantUserPage = components['schemas']['TenantUserPage'];
export type TenantUserDetail = components['schemas']['TenantUserDetail'];
export type TenantMembershipStatus = components['schemas']['TenantMembershipStatus'];
export type TenantUserQuery = { cursor?: string; limit?: number; status?: TenantMembershipStatus };
export type SuspendTenantUserReasonCode =
  components['schemas']['SuspendTenantUserRequest']['reasonCode'];

export function adminOperations(client: KapsoraClient) {
  return {
    async listUsers(tenantId: string, query: TenantUserQuery = {}): Promise<TenantUserPage> {
      return (
        await unwrap(
          client.GET('/api/v1/admin/users', {
            params: { header: { 'X-Tenant-ID': tenantId }, query },
          }),
        )
      ).data;
    },
    async getUser(tenantId: string, membershipId: string): Promise<TenantUserDetail> {
      return (
        await unwrap(
          client.GET('/api/v1/admin/users/{membershipId}', {
            params: { header: { 'X-Tenant-ID': tenantId }, path: { membershipId } },
          }),
        )
      ).data;
    },
    async getUserVersioned(
      tenantId: string,
      membershipId: string,
    ): Promise<Versioned<TenantUserDetail>> {
      const result = await unwrap(
        client.GET('/api/v1/admin/users/{membershipId}', {
          params: { header: { 'X-Tenant-ID': tenantId }, path: { membershipId } },
        }),
      );
      return versioned(result.data, result.response);
    },
    async suspendUser(
      tenantId: string,
      membershipId: string,
      body: { reasonCode: SuspendTenantUserReasonCode },
      etag: string,
      idempotencyKey: string,
    ): Promise<Versioned<TenantUserDetail>> {
      const result = await unwrap(
        client.POST('/api/v1/admin/users/{membershipId}/suspend', {
          params: {
            header: {
              'X-Tenant-ID': tenantId,
              'If-Match': etag,
              'Idempotency-Key': idempotencyKey,
            },
            path: { membershipId },
          },
          body,
        }),
      );
      return versioned(result.data, result.response);
    },
  };
}
