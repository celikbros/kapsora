import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const sourceId = process.env['E2E_NOTIFICATION_SOURCE_REQUEST'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !process.env['E2E_EXISTING_UI_URL'] || !sourceId,
  'requires an existing approved synthetic request and the operator-started demo',
);

// Read the actual worker's retained delivery evidence. This neither republishes an
// event nor resends a notification; isolated database tests cover redelivery/rollback.
test('real request decision retains one safe notification per recipient and channel', async () => {
  const admin = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const provider = new Actor(await apiRequest.newContext(), 'provider', true);
  try {
    await admin.login('admin.a');
    await doctor.login('doctor.a');
    await provider.login('provider.a');
    const path = `/api/v1/service-requests/${sourceId}`;
    const source = await doctor.call<S<'ServiceRequest'>>('GET', path);
    expect(source.data.status).toBe('APPROVED');
    expect(source.data.personDisplayName).toMatch(/^Deneme /);
    const ids: string[] = [];
    for (const [recipientType, recipientId] of [
      ['PERSON', source.data.personId],
      ['ORGANIZATION', source.data.providerOrganizationId],
    ] as const) {
      expect(recipientId).toBeTruthy();
      const query = `/api/v1/notification-messages?eventCode=service_request.decided&recipientType=${recipientType}&recipientId=${recipientId}&limit=100`;
      const messages = (
        await admin.call<S<'NotificationMessagePage'>>('GET', query)
      ).data.items.filter((m) => m.safeVariables['reference_no'] === source.data.reference);
      expect(messages.map((m) => m.channel).sort()).toEqual(['EMAIL', 'INAPP']);
      for (const message of messages) {
        expect(Object.keys(message.safeVariables).sort()).toEqual([
          'deep_link',
          'event_date',
          'reference_no',
          'status_code',
        ]);
        expect(message.safeVariables['status_code']).toBe('APPROVED');
        expect(message.safeVariables['deep_link']).toContain(sourceId);
        const detail = (
          await admin.call<S<'NotificationMessageDetail'>>(
            'GET',
            `/api/v1/notification-messages/${message.id}`,
          )
        ).data;
        if (message.channel === 'INAPP') {
          expect(message.status).toBe('SENT');
          expect(detail.deliveries).toHaveLength(1);
          expect(detail.deliveries[0]!.providerCode).toBe('INAPP_RECORDER');
          expect(detail.deliveries[0]!.outcome).toBe('ACCEPTED');
        } else {
          // This named fixture has no contacts; absence is recorded, never a fake delivery.
          expect(message.status).toBe('SUPPRESSED');
          expect(message.suppressedReason).toBe('NO_ADDRESS');
          expect(detail.deliveries).toEqual([]);
        }
        ids.push(message.id);
      }
      await provider.call('GET', query, undefined, { expected: 403 });
    }
    expect(await doctor.call('GET', path)).toEqual(source);
    await test.info().attach('request-notification-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        requestId: sourceId,
        messageIds: ids,
        inAppRecordedOnce: true,
        emailTruthfullySuppressed: true,
      }),
    });
  } finally {
    await Promise.all([admin.close(), doctor.close(), provider.close()]);
  }
});
