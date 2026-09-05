/**
 * MSW handlers for M4 notifications: message templates and their versions, the message
 * log with every delivery attempt made on it, resending, and what a recipient has said
 * about being told things.
 *
 * The behaviour worth writing a screen against is the suppressed message. A member who
 * was not told — because they turned this off, because it was the middle of their night,
 * because nobody has written a template for the event, or because the platform holds no
 * address for them — has a row on this log saying so and naming the reason, so an
 * operator asked "was the member told" can answer it either way. A suppression is never
 * an absence.
 *
 * The other is the safe-variable catalogue. A template may only declare variables from
 * it, the declared set and the placeholders the body uses must be the same set, and no
 * value carries a run of eight digits — which is the same rule the schema enforces with a
 * CHECK, because there is deliberately nowhere in a notification to put an identity
 * number, a diagnosis, or something an operator typed into a comment.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  toNotificationDelivery,
  toNotificationMessage,
  toNotificationPreference,
  toNotificationTemplate,
  type MockWorld,
  type StoredNotificationMessage,
  type StoredNotificationPreference,
  type StoredNotificationTemplate,
} from './data';
import type { MockApi } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  parseLimit,
  pathParam,
  problem,
  readJson,
  requireIdempotencyKey,
  requireIfMatch,
  validationFailed,
  wait,
  type FieldError,
  type Guarded,
  type Schemas,
} from './handlers';

const EVENT_CODE = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,4}$/;
const LOCALE = /^[a-z]{2}(-[A-Z]{2})?$/;
const PLACEHOLDER = /\{\{\s*([a-z][a-z0-9_]*)\s*\}\}/g;
const CLOCK_TIME = /^([01]\d|2[0-3]):([0-5]\d)(:[0-5]\d)?$/;
const DIGIT_RUN = /\d{8}/;
const MAX_SUBJECT = 200;
const MAX_BODY = 5000;
const MAX_DECLARED_VARIABLES = 10;
const MAX_PREFERENCES = 40;

const CHANNELS = new Set<string>(['EMAIL', 'SMS', 'PUSH', 'INAPP']);
const RECIPIENT_TYPES = new Set<string>(['PERSON', 'ACTOR', 'ORGANIZATION']);
const TEMPLATE_STATUSES = new Set<string>(['DRAFT', 'PUBLISHED', 'RETIRED']);
const MESSAGE_STATUSES = new Set<string>(['QUEUED', 'SENDING', 'SENT', 'FAILED', 'SUPPRESSED']);

/**
 * The whole of what a notification may carry. A given name has no digits at all, a status
 * is a code, a date is a date, an amount is a decimal, and a deep link is a path with no
 * query string — so there is nowhere in any of them for a token or an identity number to
 * ride along.
 */
const SAFE_VARIABLES = new Set<string>([
  'given_name',
  'reference_no',
  'status_code',
  'event_date',
  'expires_at',
  'amount',
  'currency',
  'provider_name',
  'program_name',
  'deep_link',
]);

function templateNotFound(api: MockApi): Response {
  return problem(api, 404, 'NOTIFICATION_TEMPLATE_NOT_FOUND', 'Bildirim şablonu bulunamadı');
}

function messageNotFound(api: MockApi): Response {
  return problem(api, 404, 'NOTIFICATION_MESSAGE_NOT_FOUND', 'Bildirim kaydı bulunamadı');
}

/** The placeholders a body and subject actually use, in the order they first appear. */
function placeholdersOf(text: string): string[] {
  const out: string[] = [];
  for (const match of text.matchAll(PLACEHOLDER)) {
    const name = match[1]!;
    if (!out.includes(name)) out.push(name);
  }
  return out;
}

/**
 * A template is refused rather than repaired. A declared variable the body never mentions
 * is a value every caller has to supply for nothing; a placeholder nobody declared is a
 * hole in the message; and a brace pair the renderer would not substitute is refused
 * rather than sent verbatim to a member.
 */
function validateTemplate(body: Schemas['CreateNotificationTemplate'] | null): FieldError[] {
  const errors: FieldError[] = [];
  if (typeof body?.eventCode !== 'string' || !EVENT_CODE.test(body.eventCode)) {
    errors.push({ field: 'eventCode', code: 'FORMAT' });
  }
  if (!CHANNELS.has(body?.channel as string)) errors.push({ field: 'channel', code: 'ENUM' });
  if (typeof body?.locale !== 'string' || !LOCALE.test(body.locale)) {
    errors.push({ field: 'locale', code: 'FORMAT' });
  }
  const text = typeof body?.body === 'string' ? body.body : '';
  if (text.trim().length < 1 || text.length > MAX_BODY) {
    errors.push({ field: 'body', code: 'LENGTH' });
  }
  // A subject belongs to an e-mail and nowhere else.
  if (body?.channel === 'EMAIL') {
    if (typeof body.subject !== 'string' || body.subject.trim().length < 1) {
      errors.push({ field: 'subject', code: 'REQUIRED' });
    } else if (body.subject.length > MAX_SUBJECT) {
      errors.push({ field: 'subject', code: 'LENGTH' });
    }
  } else if (body?.subject !== undefined) {
    errors.push({ field: 'subject', code: 'UNSUPPORTED' });
  }
  const declared = body?.declaredVariables ?? [];
  if (declared.length > MAX_DECLARED_VARIABLES) {
    errors.push({ field: 'declaredVariables', code: 'RANGE' });
  }
  const seen = new Set<string>();
  declared.forEach((name, i) => {
    if (!SAFE_VARIABLES.has(name)) {
      errors.push({ field: `declaredVariables[${i}]`, code: 'ENUM' });
    }
    if (seen.has(name)) errors.push({ field: `declaredVariables[${i}]`, code: 'DUPLICATE' });
    seen.add(name);
  });
  const used = placeholdersOf(`${body?.subject ?? ''} ${text}`);
  const declaredNames: string[] = declared;
  for (const name of used) {
    if (!declaredNames.includes(name)) {
      errors.push({
        field: 'body',
        code: 'NOT_DECLARED',
        message: `bildirilmemiş değişken: ${name}`,
      });
    }
  }
  for (const name of declared) {
    if (!used.includes(name)) {
      errors.push({
        field: 'declaredVariables',
        code: 'UNUSED',
        message: `metinde kullanılmayan değişken: ${name}`,
      });
    }
  }
  // Anything left after the valid placeholders are removed is a brace pair the renderer
  // would not touch.
  if (`${body?.subject ?? ''} ${text}`.replace(PLACEHOLDER, '').includes('{{')) {
    errors.push({ field: 'body', code: 'PLACEHOLDER' });
  }
  if (DIGIT_RUN.test(text) || DIGIT_RUN.test(body?.subject ?? '')) {
    errors.push({
      field: 'body',
      code: 'UNSAFE_VALUE',
      message: 'sekiz haneli rakam dizisi bir kimlik numarası olabilir',
    });
  }
  return errors;
}

export function notificationHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  /** Reading is either grant; writing is only `notification.manage`. */
  const guardRead = (request: Request, mutation: boolean): Guarded => {
    const read = guardTenant(api, request, 'notification.read', mutation);
    if (!('error' in read)) return read;
    const manage = guardTenant(api, request, 'notification.manage', mutation);
    return 'error' in manage ? read : manage;
  };

  const findTemplate = (tenantId: string, id: string): StoredNotificationTemplate | undefined =>
    world().notificationTemplates.find((t) => t.id === id && t.tenantId === tenantId);

  const findMessage = (tenantId: string, id: string): StoredNotificationMessage | undefined =>
    world().notificationMessages.find((m) => m.id === id && m.tenantId === tenantId);

  return [
    http.get(`${ANY}/api/v1/notification-templates`, async ({ request }) => {
      await wait(api);
      const g = guardRead(request, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') return validationFailed(api, [{ field: 'limit', code: 'FORMAT' }]);
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const eventCode = url.searchParams.get('eventCode');
      const channel = url.searchParams.get('channel');
      const locale = url.searchParams.get('locale');
      const status = url.searchParams.get('status');
      const filterErrors: FieldError[] = [];
      if (channel && !CHANNELS.has(channel)) filterErrors.push({ field: 'channel', code: 'ENUM' });
      if (status && !TEMPLATE_STATUSES.has(status)) {
        filterErrors.push({ field: 'status', code: 'ENUM' });
      }
      if (filterErrors.length > 0) return validationFailed(api, filterErrors);
      const rows = world()
        .notificationTemplates.filter(
          (t) =>
            t.tenantId === g.tenantId &&
            (!eventCode || t.eventCode === eventCode) &&
            (!channel || t.channel === channel) &&
            (!locale || t.locale === locale) &&
            (!status || t.status === status),
        )
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page: Schemas['NotificationTemplatePage'] = {
        // Every version stays on this list, so the message somebody was sent last month
        // can still be read next to the template that produced it.
        items: rows.slice(offset, offset + limit).map(toNotificationTemplate),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(page);
    }),

    http.post(`${ANY}/api/v1/notification-templates`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'notification.manage', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const body = await readJson<Schemas['CreateNotificationTemplate']>(request);
      const errors = validateTemplate(body);
      if (errors.length > 0) return validationFailed(api, errors);
      // The version number is not a field: it is computed from the versions that already
      // exist for this event, channel and language.
      const siblings = world().notificationTemplates.filter(
        (t) =>
          t.tenantId === g.tenantId &&
          t.eventCode === body!.eventCode &&
          t.channel === body!.channel &&
          t.locale === body!.locale,
      );
      const versionNo = siblings.reduce((max, t) => Math.max(max, t.versionNo), 0) + 1;
      const template: StoredNotificationTemplate = {
        tenantId: g.tenantId,
        id: world().nextId(),
        eventCode: body!.eventCode,
        channel: body!.channel,
        locale: body!.locale,
        versionNo,
        // Nothing renders from a draft, so a half-written message cannot reach anybody
        // while it is being written.
        status: 'DRAFT',
        subject: body!.subject ?? null,
        body: body!.body,
        declaredVariables: body!.declaredVariables ?? [],
        publishedAt: null,
        publishedBy: null,
        createdAt: new Date().toISOString(),
        rowVersion: 1,
      };
      world().notificationTemplates.push(template);
      return HttpResponse.json(toNotificationTemplate(template), {
        status: 201,
        headers: { ETag: etagOf(template.rowVersion) },
      });
    }),

    http.get(`${ANY}/api/v1/notification-templates/:templateId`, async ({ request, params }) => {
      await wait(api);
      const g = guardRead(request, false);
      if ('error' in g) return g.error;
      const template = findTemplate(g.tenantId, pathParam(params, 'templateId'));
      if (!template) return templateNotFound(api);
      return HttpResponse.json(toNotificationTemplate(template), {
        headers: { ETag: etagOf(template.rowVersion) },
      });
    }),

    http.post(
      `${ANY}/api/v1/notification-templates/:templateId/publish`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'notification.manage', true);
        if ('error' in g) return g.error;
        const missingKey = requireIdempotencyKey(api, request);
        if (missingKey) return missingKey;
        const expected = requireIfMatch(api, request);
        if (typeof expected !== 'number') return expected;
        const template = findTemplate(g.tenantId, pathParam(params, 'templateId'));
        if (!template) return templateNotFound(api);
        if (template.status !== 'DRAFT') {
          return problem(
            api,
            409,
            'NOTIFICATION_TEMPLATE_NOT_DRAFT',
            'Yalnızca taslak şablon yayınlanabilir',
          );
        }
        if (template.rowVersion !== expected) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        const now = new Date().toISOString();
        // Publishing retires rather than refuses. Refusing until somebody retired the old
        // one would leave the event with no published template for however long the two
        // commands are apart, and a message arriving in that gap would be suppressed for
        // want of one. Replacing in one step has no such gap.
        for (const other of world().notificationTemplates) {
          if (
            other.tenantId === g.tenantId &&
            other.id !== template.id &&
            other.eventCode === template.eventCode &&
            other.channel === template.channel &&
            other.locale === template.locale &&
            other.status === 'PUBLISHED'
          ) {
            other.status = 'RETIRED';
            other.rowVersion += 1;
          }
        }
        template.status = 'PUBLISHED';
        template.publishedAt = now;
        template.publishedBy = g.session.account.actorId;
        template.rowVersion += 1;
        return HttpResponse.json(toNotificationTemplate(template), {
          headers: { ETag: etagOf(template.rowVersion) },
        });
      },
    ),

    http.get(`${ANY}/api/v1/notification-messages`, async ({ request }) => {
      await wait(api);
      const g = guardRead(request, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') return validationFailed(api, [{ field: 'limit', code: 'FORMAT' }]);
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const eventCode = url.searchParams.get('eventCode');
      const channel = url.searchParams.get('channel');
      const status = url.searchParams.get('status');
      const recipientType = url.searchParams.get('recipientType');
      const recipientId = url.searchParams.get('recipientId');
      const errors: FieldError[] = [];
      if (channel && !CHANNELS.has(channel)) errors.push({ field: 'channel', code: 'ENUM' });
      if (status && !MESSAGE_STATUSES.has(status)) errors.push({ field: 'status', code: 'ENUM' });
      if (recipientType && !RECIPIENT_TYPES.has(recipientType)) {
        errors.push({ field: 'recipientType', code: 'ENUM' });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      const rows = world()
        .notificationMessages.filter(
          (m) =>
            m.tenantId === g.tenantId &&
            (!eventCode || m.eventCode === eventCode) &&
            (!channel || m.channel === channel) &&
            (!status || m.status === status) &&
            (!recipientType || m.recipientType === recipientType) &&
            (!recipientId || m.recipientId === recipientId),
        )
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page: Schemas['NotificationMessagePage'] = {
        // Suppressed messages are on this list like any other, which is the point of it.
        items: rows.slice(offset, offset + limit).map(toNotificationMessage),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(page);
    }),

    http.get(`${ANY}/api/v1/notification-messages/:messageId`, async ({ request, params }) => {
      await wait(api);
      const g = guardRead(request, false);
      if ('error' in g) return g.error;
      const message = findMessage(g.tenantId, pathParam(params, 'messageId'));
      if (!message) return messageNotFound(api);
      const detail: Schemas['NotificationMessageDetail'] = {
        message: toNotificationMessage(message),
        // The attempts travel with the message because a status alone does not answer the
        // question: one that says SENT after two failures and one that went first time
        // are the same status and different stories.
        deliveries: world()
          .notificationDeliveries.filter((d) => d.messageId === message.id)
          .sort((a, b) => a.attemptNo - b.attemptNo)
          .map(toNotificationDelivery),
      };
      return HttpResponse.json(detail);
    }),

    http.post(
      `${ANY}/api/v1/notification-messages/:messageId/resend`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'notification.manage', true);
        if ('error' in g) return g.error;
        const missingKey = requireIdempotencyKey(api, request);
        if (missingKey) return missingKey;
        const original = findMessage(g.tenantId, pathParam(params, 'messageId'));
        if (!original) return messageNotFound(api);
        // A message with no rendered body cannot be resent: it was suppressed before
        // anything was written, so there is nothing to send.
        if (!original.bodyRendered || !original.templateId) {
          return problem(
            api,
            409,
            'NOTIFICATION_MESSAGE_NOT_RESENDABLE',
            'Bu bildirim yeniden gönderilemez',
          );
        }
        if (original.status !== 'SENT' && original.status !== 'FAILED') {
          return problem(
            api,
            409,
            'NOTIFICATION_MESSAGE_NOT_RESENDABLE',
            'Bu bildirim yeniden gönderilemez',
          );
        }
        // An operator clicking a button must not override somebody's own answer to "do
        // you want to hear about this". Quiet hours are deliberately not re-checked: an
        // operator resending has already decided about the timing.
        const preference = world()
          .notificationPreferences.filter(
            (p) =>
              p.tenantId === g.tenantId &&
              p.recipientType === original.recipientType &&
              p.recipientId === original.recipientId &&
              p.channel === original.channel &&
              (p.eventCode === original.eventCode || p.eventCode === null),
          )
          // A row naming the event wins over the row naming none.
          .sort((a, b) => (a.eventCode === null ? 1 : 0) - (b.eventCode === null ? 1 : 0))[0];
        if (preference && !preference.enabled) {
          return problem(
            api,
            409,
            'NOTIFICATION_RECIPIENT_OPTED_OUT',
            'Alıcı bu bildirimi kapatmış',
          );
        }
        // A new message rather than a second attempt at the old one: the original is
        // evidence of what happened, and re-queuing it would overwrite that story.
        const copy: StoredNotificationMessage = {
          ...original,
          id: world().nextId(),
          status: 'QUEUED',
          suppressedReason: null,
          sentAt: null,
          resentFromMessageId: original.id,
          createdAt: new Date().toISOString(),
          rowVersion: 1,
        };
        world().notificationMessages.push(copy);
        return HttpResponse.json(toNotificationMessage(copy), { status: 201 });
      },
    ),

    http.get(`${ANY}/api/v1/notification-preferences`, async ({ request }) => {
      await wait(api);
      const g = guardRead(request, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const recipientType = url.searchParams.get('recipientType');
      const recipientId = url.searchParams.get('recipientId');
      const errors: FieldError[] = [];
      if (!recipientType || !RECIPIENT_TYPES.has(recipientType)) {
        errors.push({ field: 'recipientType', code: recipientType ? 'ENUM' : 'REQUIRED' });
      }
      if (!recipientId) errors.push({ field: 'recipientId', code: 'REQUIRED' });
      if (errors.length > 0) return validationFailed(api, errors);
      const list: Schemas['NotificationPreferenceList'] = {
        // An empty list means nobody has said anything, which is not the same as
        // everything being off: silence means send.
        items: world()
          .notificationPreferences.filter(
            (p) =>
              p.tenantId === g.tenantId &&
              p.recipientType === recipientType &&
              p.recipientId === recipientId,
          )
          .sort(
            (a, b) =>
              a.channel.localeCompare(b.channel) ||
              (a.eventCode ?? '').localeCompare(b.eventCode ?? ''),
          )
          .map(toNotificationPreference),
      };
      return HttpResponse.json(list);
    }),

    http.put(`${ANY}/api/v1/notification-preferences`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'notification.manage', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const body = await readJson<Schemas['PutNotificationPreferences']>(request);
      const errors: FieldError[] = [];
      if (!RECIPIENT_TYPES.has(body?.recipientType as string)) {
        errors.push({ field: 'recipientType', code: 'ENUM' });
      }
      if (!body?.recipientId) errors.push({ field: 'recipientId', code: 'REQUIRED' });
      const inputs = body?.preferences;
      if (!Array.isArray(inputs)) {
        errors.push({ field: 'preferences', code: 'REQUIRED' });
      } else if (inputs.length > MAX_PREFERENCES) {
        errors.push({ field: 'preferences', code: 'RANGE' });
      } else {
        const seen = new Set<string>();
        inputs.forEach((p, i) => {
          const path = `preferences[${i}]`;
          if (!CHANNELS.has(p?.channel as string)) {
            errors.push({ field: `${path}.channel`, code: 'ENUM' });
          }
          if (typeof p?.enabled !== 'boolean') {
            errors.push({ field: `${path}.enabled`, code: 'REQUIRED' });
          }
          if (p?.eventCode !== undefined && !EVENT_CODE.test(p.eventCode)) {
            errors.push({ field: `${path}.eventCode`, code: 'FORMAT' });
          }
          const key = `${p?.channel}:${p?.eventCode ?? ''}`;
          if (seen.has(key)) errors.push({ field: `${path}.channel`, code: 'DUPLICATE' });
          seen.add(key);
          // Quiet hours are a pair or are absent: one edge on its own means nothing.
          const start = p?.quietHoursStart;
          const end = p?.quietHoursEnd;
          if ((start === undefined) !== (end === undefined)) {
            errors.push({
              field: `${path}.quietHoursStart`,
              code: 'REQUIRED',
              message: 'sessiz saatler başlangıç ve bitişle birlikte verilir',
            });
          }
          for (const [name, value] of [
            ['quietHoursStart', start],
            ['quietHoursEnd', end],
          ] as const) {
            if (value !== undefined && !CLOCK_TIME.test(value)) {
              errors.push({ field: `${path}.${name}`, code: 'FORMAT' });
            }
          }
          if (start !== undefined && end !== undefined && start === end) {
            errors.push({ field: `${path}.quietHoursEnd`, code: 'RANGE' });
          }
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      // A replace rather than a merge: the set is read as a whole, and a merge would leave
      // behind a channel the person believed they had turned off.
      world().notificationPreferences = world().notificationPreferences.filter(
        (p) =>
          !(
            p.tenantId === g.tenantId &&
            p.recipientType === body!.recipientType &&
            p.recipientId === body!.recipientId
          ),
      );
      const now = new Date().toISOString();
      for (const input of inputs!) {
        const row: StoredNotificationPreference = {
          tenantId: g.tenantId,
          id: world().nextId(),
          recipientType: body!.recipientType,
          recipientId: body!.recipientId,
          // Absent means every event on this channel, which is how somebody turns a
          // channel off once instead of once per event.
          eventCode: input.eventCode ?? null,
          channel: input.channel,
          enabled: input.enabled,
          // Seconds are dropped: a window that ends at 07:59:59 is a window somebody
          // meant to end at 08:00.
          quietHoursStart: input.quietHoursStart?.slice(0, 5) ?? null,
          quietHoursEnd: input.quietHoursEnd?.slice(0, 5) ?? null,
          timezone: input.timezone ?? 'Europe/Istanbul',
          createdAt: now,
          rowVersion: 1,
        };
        world().notificationPreferences.push(row);
      }
      const list: Schemas['NotificationPreferenceList'] = {
        items: world()
          .notificationPreferences.filter(
            (p) =>
              p.tenantId === g.tenantId &&
              p.recipientType === body!.recipientType &&
              p.recipientId === body!.recipientId,
          )
          .sort(
            (a, b) =>
              a.channel.localeCompare(b.channel) ||
              (a.eventCode ?? '').localeCompare(b.eventCode ?? ''),
          )
          .map(toNotificationPreference),
      };
      return HttpResponse.json(list);
    }),
  ];
}
