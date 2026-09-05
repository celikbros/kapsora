import { randomId, type KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type NotificationTemplate = components['schemas']['NotificationTemplate'];
export type NotificationTemplatePage = components['schemas']['NotificationTemplatePage'];
export type NotificationMessage = components['schemas']['NotificationMessage'];
export type NotificationMessageDetail = components['schemas']['NotificationMessageDetail'];
export type NotificationMessagePage = components['schemas']['NotificationMessagePage'];
export type NotificationDelivery = components['schemas']['NotificationDelivery'];
export type NotificationPreference = components['schemas']['NotificationPreference'];
export type NotificationPreferenceInput = components['schemas']['NotificationPreferenceInput'];
export type NotificationChannel = components['schemas']['NotificationChannel'];
export type CreateNotificationTemplate = components['schemas']['CreateNotificationTemplate'];
export type NotificationTemplateStatus = components['schemas']['NotificationTemplateStatus'];
export type NotificationMessageStatus = components['schemas']['NotificationMessageStatus'];
export type NotificationRecipientType = components['schemas']['NotificationRecipientType'];
export type PutNotificationPreferences = components['schemas']['PutNotificationPreferences'];

export interface NotificationTemplateListQuery {
  cursor?: string;
  limit?: number;
  eventCode?: string;
  channel?: NotificationChannel;
  locale?: string;
  status?: NotificationTemplateStatus;
}

export interface NotificationMessageListQuery {
  cursor?: string;
  limit?: number;
  eventCode?: string;
  channel?: NotificationChannel;
  status?: NotificationMessageStatus;
  recipientType?: NotificationRecipientType;
  recipientId?: string;
}

/**
 * Notification templates, the message log and per-recipient preferences.
 *
 * A template declares the variables it may carry, and the server refuses a render that
 * uses anything else. The declared list is therefore not decoration on the editor: it is
 * the contract that keeps a diagnosis, an identity number or an operator's free-text
 * comment out of something that leaves the building and cannot be recalled.
 *
 * A published template is immutable, and publishing another for the same event, channel
 * and locale retires the one in force. The message log shows what was suppressed as well
 * as what was sent, with the reason — a member who was not told has to be visible as not
 * told, which is why a screen must never filter SUPPRESSED out of the default view.
 */
export function notificationOperations(client: KapsoraClient) {
  const header = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const command = (tenantId: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'Idempotency-Key': idempotencyKey,
  });

  return {
    async listTemplates(
      tenantId: string,
      query: NotificationTemplateListQuery = {},
    ): Promise<NotificationTemplatePage> {
      const q: NotificationTemplateListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.eventCode) q.eventCode = query.eventCode;
      if (query.channel) q.channel = query.channel;
      if (query.locale) q.locale = query.locale;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/notification-templates', {
            params: { header: header(tenantId), query: q },
          }),
        )
      ).data;
    },

    async getTemplate(
      tenantId: string,
      templateId: string,
    ): Promise<Versioned<NotificationTemplate>> {
      const r = await unwrap(
        client.GET('/api/v1/notification-templates/{templateId}', {
          params: { header: header(tenantId), path: { templateId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async createTemplate(
      tenantId: string,
      body: CreateNotificationTemplate,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<NotificationTemplate>> {
      const r = await unwrap(
        client.POST('/api/v1/notification-templates', {
          params: { header: command(tenantId, idempotencyKey) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Publishing retires whatever was in force for the same event, channel and locale. */
    async publishTemplate(
      tenantId: string,
      templateId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<NotificationTemplate>> {
      const r = await unwrap(
        client.POST('/api/v1/notification-templates/{templateId}/publish', {
          params: {
            header: { ...command(tenantId, idempotencyKey), 'If-Match': etag },
            path: { templateId },
          },
        }),
      );
      return versioned(r.data, r.response);
    },

    async listMessages(
      tenantId: string,
      query: NotificationMessageListQuery = {},
    ): Promise<NotificationMessagePage> {
      const q: NotificationMessageListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.eventCode) q.eventCode = query.eventCode;
      if (query.channel) q.channel = query.channel;
      if (query.status) q.status = query.status;
      if (query.recipientType) q.recipientType = query.recipientType;
      if (query.recipientId) q.recipientId = query.recipientId;
      return (
        await unwrap(
          client.GET('/api/v1/notification-messages', {
            params: { header: header(tenantId), query: q },
          }),
        )
      ).data;
    },

    /** The message and every attempt made on it, including the ones that were refused. */
    async getMessage(tenantId: string, messageId: string): Promise<NotificationMessageDetail> {
      return (
        await unwrap(
          client.GET('/api/v1/notification-messages/{messageId}', {
            params: { header: header(tenantId), path: { messageId } },
          }),
        )
      ).data;
    },

    /** A resend is a new message; the original stays as evidence of what happened. */
    async resendMessage(
      tenantId: string,
      messageId: string,
      idempotencyKey: string = randomId(),
    ): Promise<NotificationMessage> {
      return (
        await unwrap(
          client.POST('/api/v1/notification-messages/{messageId}/resend', {
            params: { header: command(tenantId, idempotencyKey), path: { messageId } },
          }),
        )
      ).data;
    },

    async getPreferences(
      tenantId: string,
      recipientType: NotificationRecipientType,
      recipientId: string,
    ): Promise<NotificationPreference[]> {
      const r = await unwrap(
        client.GET('/api/v1/notification-preferences', {
          params: { header: header(tenantId), query: { recipientType, recipientId } },
        }),
      );
      return r.data.items;
    },

    async putPreferences(
      tenantId: string,
      body: PutNotificationPreferences,
      idempotencyKey: string = randomId(),
    ): Promise<NotificationPreference[]> {
      const r = await unwrap(
        client.PUT('/api/v1/notification-preferences', {
          params: { header: command(tenantId, idempotencyKey) },
          body,
        }),
      );
      return r.data.items;
    },
  };
}
