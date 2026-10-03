import type { components } from './generated/kapsora-v1';
import type { KapsoraClient } from './client';
import { unwrap } from './problem';

export type TenantUser = components['schemas']['TenantUser'];
export type TenantUserPage = components['schemas']['TenantUserPage'];
export type TenantUserDetail = components['schemas']['TenantUserDetail'];
export type TenantMembershipStatus = components['schemas']['TenantMembershipStatus'];
export type TenantUserQuery = { cursor?: string; limit?: number; status?: TenantMembershipStatus };

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
  };
}
