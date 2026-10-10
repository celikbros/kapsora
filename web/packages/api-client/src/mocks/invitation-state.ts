import type { components } from '../generated/kapsora-v1';

type Schemas = components['schemas'];

export interface StoredMockInvitation {
  summary: Schemas['TenantInvitation'];
  tenantId: string;
  contactIndex: string | null;
  proofDigest: string;
  acceptedActorId: string | null;
  acceptedKey: string | null;
  acceptedOutcome: Schemas['AcceptExistingInvitationResponse'] | null;
  acceptedMode?: 'EXISTING' | 'NEW' | null;
  newOutcome?: {
    tenantId: string;
    tenantDisplayName: string;
    membershipId: string;
    membershipStatus: 'ACTIVE';
    accessPending: boolean;
    loginHandle: string;
    recoveryExpiresAt: string;
  } | null;
  terminalAt: number | null;
}

/** Private in-memory delivery stand-in; neither manager API nor replay exposes codes. */
export class MockInvitationState {
  readonly rows = new Map<string, StoredMockInvitation>();
  readonly createReceipts = new Map<
    string,
    { fingerprint: string; summary: Schemas['TenantInvitation'] }
  >();
  readonly cancelReceipts = new Map<
    string,
    { version: number; summary: Schemas['TenantInvitation'] }
  >();
  readonly events: { action: 'create' | 'cancel' | 'accept'; invitationId: string }[] = [];
  readonly newCredentials = new Map<string, string>();
  readonly pendingAcceptances = new Set<string>();
  private readonly credentialAttempts = new Map<
    string,
    { failures: number; lockedUntil: number }
  >();
  private readonly deliveredCodes = new Map<string, string>();
  private key = crypto.getRandomValues(new Uint8Array(32));

  reset(): void {
    this.rows.clear();
    this.createReceipts.clear();
    this.cancelReceipts.clear();
    this.deliveredCodes.clear();
    this.events.length = 0;
    this.newCredentials.clear();
    this.pendingAcceptances.clear();
    this.credentialAttempts.clear();
    this.key = crypto.getRandomValues(new Uint8Array(32));
  }

  async fingerprint(value: readonly string[]): Promise<string> {
    const key = await crypto.subtle.importKey(
      'raw',
      this.key,
      { name: 'HMAC', hash: 'SHA-256' },
      false,
      ['sign'],
    );
    return hex(
      await crypto.subtle.sign('HMAC', key, new TextEncoder().encode(JSON.stringify(value))),
    );
  }

  async credentialProof(actorId: string, password: string): Promise<string> {
    return this.fingerprint(['credential', actorId, password]);
  }

  async verifyNewCredential(actorId: string, password: string): Promise<boolean> {
    const expected = this.newCredentials.get(actorId);
    if (this.isNewCredentialLocked(actorId)) return false;
    const actual = await this.credentialProof(actorId, password);
    if (expected && expected === actual) {
      this.credentialAttempts.delete(actorId);
      return true;
    }
    const previous = this.credentialAttempts.get(actorId);
    const failures = (previous?.failures ?? 0) + 1;
    this.credentialAttempts.set(actorId, {
      failures,
      lockedUntil: failures >= 10 ? Date.now() + 15 * 60_000 : 0,
    });
    return false;
  }

  isNewCredentialLocked(actorId: string): boolean {
    const attempts = this.credentialAttempts.get(actorId);
    if (!attempts) return false;
    if (attempts.lockedUntil > Date.now()) return true;
    if (attempts.lockedUntil > 0) this.credentialAttempts.delete(actorId);
    return false;
  }

  /** Test/demo mail delivery is private and volatile, like the fake SMTP test adapter. */
  deliver(invitationId: string, code: string): void {
    this.deliveredCodes.set(invitationId, code);
  }

  takeDeliveredCode(invitationId: string): string | undefined {
    const code = this.deliveredCodes.get(invitationId);
    this.deliveredCodes.delete(invitationId);
    return code;
  }

  purgeDelivery(invitationId: string): void {
    this.deliveredCodes.delete(invitationId);
  }
}

export async function invitationProofDigest(code: string): Promise<string> {
  return hex(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(code)));
}

function hex(value: ArrayBuffer): string {
  return Array.from(new Uint8Array(value), (byte) => byte.toString(16).padStart(2, '0')).join('');
}
