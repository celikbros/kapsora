import type {
  CreateNotificationTemplate,
  NotificationMessageListQuery,
  NotificationTemplateListQuery,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const notificationKeys = {
  all: (tenantId: string) => ['notifications', tenantId] as const,
  templates: (tenantId: string, query: NotificationTemplateListQuery) =>
    ['notifications', tenantId, 'templates', query] as const,
  messages: (tenantId: string, query: NotificationMessageListQuery) =>
    ['notifications', tenantId, 'messages', query] as const,
  message: (tenantId: string, id: string) => ['notifications', tenantId, 'message', id] as const,
};

export function useTemplates(query: NotificationTemplateListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: notificationKeys.templates(tenantId, query),
    queryFn: () => ops.notifications.listTemplates(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

/**
 * The log, suppressed messages included. A screen must never filter SUPPRESSED out of
 * the default view: a member who was not told has to be visible as not told.
 */
export function useMessages(query: NotificationMessageListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: notificationKeys.messages(tenantId, query),
    queryFn: () => ops.notifications.listMessages(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function useMessage(messageId: string | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: notificationKeys.message(tenantId, messageId ?? ''),
    queryFn: () => ops.notifications.getMessage(tenantId, messageId!),
    enabled: messageId !== null,
  });
}

export function useTemplateCommands() {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  const settle = () => client.invalidateQueries({ queryKey: notificationKeys.all(tenantId) });
  return {
    create: useMutation({
      mutationFn: (body: CreateNotificationTemplate) =>
        ops.notifications.createTemplate(tenantId, body),
      onSuccess: settle,
    }),
    publish: useMutation({
      mutationFn: (v: { id: string; etag: string }) =>
        ops.notifications.publishTemplate(tenantId, v.id, v.etag),
      onSuccess: settle,
    }),
    resend: useMutation({
      mutationFn: (messageId: string) => ops.notifications.resendMessage(tenantId, messageId),
      onSuccess: settle,
    }),
  };
}
