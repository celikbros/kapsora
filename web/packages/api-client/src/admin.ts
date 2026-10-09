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
export type TenantInvitation = components['schemas']['TenantInvitation'];
export type TenantInvitationPage = components['schemas']['TenantInvitationPage'];
export type CreateTenantInvitationRequest = components['schemas']['CreateTenantInvitationRequest'];
export type InspectInvitationResponse = components['schemas']['InspectInvitationResponse'];
export type AcceptExistingInvitationResponse =
  components['schemas']['AcceptExistingInvitationResponse'];
export type AcceptNewInvitationResponse = components['schemas']['AcceptNewInvitationResponse'];
// Fetch supplies Origin in browsers; script cannot set this forbidden header. The assertion
// satisfies the generated server requirement without putting a synthetic Origin on Request.
const anonymousHeaders = { 'X-Invitation-Request': '1' } as {
  Origin: string;
  'X-Invitation-Request': '1';
};
export type TenantInvitationQuery = {
  cursor?: string;
  limit?: number;
  status?: TenantInvitation['status'];
};
export type RoleAssignmentOption = components['schemas']['RoleAssignmentOption'];
export type RoleAssignmentOrganizationPage =
  components['schemas']['RoleAssignmentOrganizationPage'];
export type TenantRoleGrant = components['schemas']['TenantRoleGrant'];
export type TenantRoleGrantPage = components['schemas']['TenantRoleGrantPage'];
export type TenantRoleGrantResult = components['schemas']['TenantRoleGrantResult'];
export type AssignTenantRoleGrantRequest = components['schemas']['AssignTenantRoleGrantRequest'];
export type RevokeTenantRoleGrantRequest = components['schemas']['RevokeTenantRoleGrantRequest'];
export type PrivilegedRoleAssignmentOption =
  components['schemas']['PrivilegedRoleAssignmentOption'];
export type RoleChangeEligibility = components['schemas']['RoleChangeEligibility'];
export type CreateRoleChangeRequest = components['schemas']['CreateRoleChangeRequest'];
export type RoleChangeRequest = components['schemas']['RoleChangeRequest'];
export type RoleChangeRequestDetail = components['schemas']['RoleChangeRequestDetail'];
export type RoleChangeRequestPage = components['schemas']['RoleChangeRequestPage'];
export type RoleChangeCommandResult = components['schemas']['RoleChangeCommandResult'];
export type RoleChangeQuery = {
  cursor?: string;
  limit?: number;
  status?: RoleChangeRequest['status'];
  membershipId?: string;
};

export function adminOperations(client: KapsoraClient) {
  return {
    async privilegedRoleOptions(tenantId: string): Promise<PrivilegedRoleAssignmentOption[]> {
      return (
        await unwrap(
          client.GET('/api/v1/admin/privileged-role-assignment-options', {
            params: { header: { 'X-Tenant-ID': tenantId } },
          }),
        )
      ).data.items;
    },
    async roleChangeEligibility(
      tenantId: string,
      membershipId: string,
    ): Promise<Versioned<RoleChangeEligibility>> {
      const result = await unwrap(
        client.GET('/api/v1/admin/users/{membershipId}/role-change-eligibility', {
          params: { header: { 'X-Tenant-ID': tenantId }, path: { membershipId } },
        }),
      );
      return versioned(result.data, result.response);
    },
    async createRoleChange(
      tenantId: string,
      membershipId: string,
      body: CreateRoleChangeRequest,
      etag: string,
      key: string,
    ): Promise<Versioned<RoleChangeCommandResult>> {
      const result = await unwrap(
        client.POST('/api/v1/admin/users/{membershipId}/role-change-requests', {
          params: {
            header: { 'X-Tenant-ID': tenantId, 'If-Match': etag, 'Idempotency-Key': key },
            path: { membershipId },
          },
          body,
        }),
      );
      return versioned(result.data, result.response);
    },
    async listRoleChanges(
      tenantId: string,
      query: RoleChangeQuery = {},
    ): Promise<RoleChangeRequestPage> {
      return (
        await unwrap(
          client.GET('/api/v1/admin/role-change-requests', {
            params: { header: { 'X-Tenant-ID': tenantId }, query },
          }),
        )
      ).data;
    },
    async getRoleChange(
      tenantId: string,
      requestId: string,
    ): Promise<Versioned<RoleChangeRequestDetail>> {
      const result = await unwrap(
        client.GET('/api/v1/admin/role-change-requests/{requestId}', {
          params: { header: { 'X-Tenant-ID': tenantId }, path: { requestId } },
        }),
      );
      return versioned(result.data, result.response);
    },
    async decideRoleChange(
      tenantId: string,
      requestId: string,
      action: 'approve' | 'reject' | 'cancel',
      body:
        | Record<string, never>
        | { reasonCode: 'NOT_JUSTIFIED' | 'INCORRECT_ACCESS' | 'STALE_REQUEST' | 'WITHDRAWN' },
      etag: string,
      key: string,
    ): Promise<Versioned<RoleChangeCommandResult>> {
      const params = {
        header: { 'X-Tenant-ID': tenantId, 'If-Match': etag, 'Idempotency-Key': key },
        path: { requestId },
      };
      const result =
        action === 'approve'
          ? await unwrap(
              client.POST('/api/v1/admin/role-change-requests/{requestId}/approve', {
                params,
                body: {},
              }),
            )
          : action === 'reject'
            ? await unwrap(
                client.POST('/api/v1/admin/role-change-requests/{requestId}/reject', {
                  params,
                  body: body as {
                    reasonCode: 'NOT_JUSTIFIED' | 'INCORRECT_ACCESS' | 'STALE_REQUEST';
                  },
                }),
              )
            : await unwrap(
                client.POST('/api/v1/admin/role-change-requests/{requestId}/cancel', {
                  params,
                  body: { reasonCode: 'WITHDRAWN' },
                }),
              );
      return versioned(result.data, result.response);
    },
    async roleAssignmentOptions(tenantId: string): Promise<RoleAssignmentOption[]> {
      return (
        await unwrap(
          client.GET('/api/v1/admin/role-assignment-options', {
            params: { header: { 'X-Tenant-ID': tenantId } },
          }),
        )
      ).data.items;
    },
    async roleAssignmentOrganizations(
      tenantId: string,
      query: { cursor?: string; limit?: number } = {},
    ): Promise<RoleAssignmentOrganizationPage> {
      return (
        await unwrap(
          client.GET('/api/v1/admin/role-assignment-organizations', {
            params: { header: { 'X-Tenant-ID': tenantId }, query },
          }),
        )
      ).data;
    },
    async roleGrants(
      tenantId: string,
      membershipId: string,
      query: { cursor?: string; limit?: number } = {},
    ): Promise<Versioned<TenantRoleGrantPage>> {
      const result = await unwrap(
        client.GET('/api/v1/admin/users/{membershipId}/role-grants', {
          params: { header: { 'X-Tenant-ID': tenantId }, path: { membershipId }, query },
        }),
      );
      return versioned(result.data, result.response);
    },
    async assignRoleGrant(
      tenantId: string,
      membershipId: string,
      body: AssignTenantRoleGrantRequest,
      etag: string,
      idempotencyKey: string,
    ): Promise<Versioned<TenantRoleGrantResult>> {
      const result = await unwrap(
        client.POST('/api/v1/admin/users/{membershipId}/role-grants', {
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
    async revokeRoleGrant(
      tenantId: string,
      membershipId: string,
      grantId: string,
      body: RevokeTenantRoleGrantRequest,
      etag: string,
      idempotencyKey: string,
    ): Promise<Versioned<TenantRoleGrantResult>> {
      const result = await unwrap(
        client.POST('/api/v1/admin/users/{membershipId}/role-grants/{grantId}/revoke', {
          params: {
            header: {
              'X-Tenant-ID': tenantId,
              'If-Match': etag,
              'Idempotency-Key': idempotencyKey,
            },
            path: { membershipId, grantId },
          },
          body,
        }),
      );
      return versioned(result.data, result.response);
    },
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
    async listInvitations(
      tenantId: string,
      query: TenantInvitationQuery = {},
    ): Promise<TenantInvitationPage> {
      return (
        await unwrap(
          client.GET('/api/v1/admin/invitations', {
            params: { header: { 'X-Tenant-ID': tenantId }, query },
          }),
        )
      ).data;
    },
    async createInvitation(
      tenantId: string,
      email: string,
      idempotencyKey: string,
    ): Promise<Versioned<TenantInvitation>> {
      const result = await unwrap(
        client.POST('/api/v1/admin/invitations', {
          params: { header: { 'X-Tenant-ID': tenantId, 'Idempotency-Key': idempotencyKey } },
          body: { email },
        }),
      );
      return versioned(result.data, result.response);
    },
    async getInvitation(
      tenantId: string,
      invitationId: string,
    ): Promise<Versioned<TenantInvitation>> {
      const result = await unwrap(
        client.GET('/api/v1/admin/invitations/{invitationId}', {
          params: { header: { 'X-Tenant-ID': tenantId }, path: { invitationId } },
        }),
      );
      return versioned(result.data, result.response);
    },
    async cancelInvitation(
      tenantId: string,
      invitationId: string,
      etag: string,
      idempotencyKey: string,
    ): Promise<Versioned<TenantInvitation>> {
      const result = await unwrap(
        client.POST('/api/v1/admin/invitations/{invitationId}/cancel', {
          params: {
            header: {
              'X-Tenant-ID': tenantId,
              'If-Match': etag,
              'Idempotency-Key': idempotencyKey,
            },
            path: { invitationId },
          },
        }),
      );
      return versioned(result.data, result.response);
    },
    async inspectInvitation(code: string): Promise<InspectInvitationResponse> {
      return (await unwrap(client.POST('/api/v1/invitations/inspect', { body: { code } }))).data;
    },
    async acceptExistingInvitation(
      code: string,
      idempotencyKey: string,
    ): Promise<AcceptExistingInvitationResponse> {
      return (
        await unwrap(
          client.POST('/api/v1/invitations/accept-existing', {
            params: { header: { 'Idempotency-Key': idempotencyKey } },
            body: { code, confirmed: true },
          }),
        )
      ).data;
    },
    async inspectNewInvitation(code: string): Promise<InspectInvitationResponse> {
      return (
        await unwrap(
          client.POST('/api/v1/invitations/inspect-new', {
            params: { header: anonymousHeaders },
            body: { code },
          }),
        )
      ).data;
    },
    async acceptNewInvitation(
      code: string,
      displayName: string,
      password: string,
      idempotencyKey: string,
    ): Promise<AcceptNewInvitationResponse> {
      return (
        await unwrap(
          client.POST('/api/v1/invitations/accept-new', {
            params: { header: { ...anonymousHeaders, 'Idempotency-Key': idempotencyKey } },
            body: { code, displayName, password, confirmed: true },
          }),
        )
      ).data;
    },
    async recoverNewInvitation(
      code: string,
      password: string,
    ): Promise<AcceptNewInvitationResponse> {
      return (
        await unwrap(
          client.POST('/api/v1/invitations/acceptance-receipt', {
            params: { header: anonymousHeaders },
            body: { code, password },
          }),
        )
      ).data;
    },
  };
}
