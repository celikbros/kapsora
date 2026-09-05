import type { NotificationMessage, NotificationTemplate } from '@kapsora/api-client';
import { etagOf, type NotificationChannel } from '@kapsora/api-client';
import { usePermission, useStepUp } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Card,
  Dialog,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  StepUpDialog,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  Tabs,
  Textarea,
  useToast,
  type BadgeTone,
} from '@kapsora/ui';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useMessage, useMessages, useTemplateCommands, useTemplates } from './queries';

const CHANNELS: NotificationChannel[] = ['EMAIL', 'SMS', 'PUSH', 'INAPP'];
/** The closed catalogue. The server refuses anything else at render time. */
const SAFE_VARIABLES = [
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
] as const;
type SafeVariable = (typeof SAFE_VARIABLES)[number];

function templateTone(status: NotificationTemplate['status']): BadgeTone {
  return status === 'PUBLISHED' ? 'success' : status === 'RETIRED' ? 'neutral' : 'info';
}
function messageTone(status: NotificationMessage['status']): BadgeTone {
  switch (status) {
    case 'SENT':
      return 'success';
    case 'FAILED':
      return 'danger';
    case 'SUPPRESSED':
      return 'warning';
    default:
      return 'info';
  }
}

function TemplatesTab() {
  const { t } = useTranslation();
  const toast = useToast();
  const stepUp = useStepUp();
  const canManage = usePermission('notification.manage');
  const templates = useTemplates({ limit: 100 });
  const commands = useTemplateCommands();
  const [creating, setCreating] = useState(false);
  const [eventCode, setEventCode] = useState('');
  const [channel, setChannel] = useState<NotificationChannel>('EMAIL');
  const [locale, setLocale] = useState('tr-TR');
  const [subject, setSubject] = useState('');
  const [body, setBody] = useState('');
  const [declared, setDeclared] = useState<SafeVariable[]>(['given_name', 'reference_no']);

  async function submit(event: FormEvent) {
    event.preventDefault();
    await commands.create.mutateAsync({
      eventCode: eventCode.trim(),
      channel,
      locale: locale.trim(),
      body,
      declaredVariables: declared,
      ...(channel === 'EMAIL' && subject.trim() ? { subject: subject.trim() } : {}),
    });
    setCreating(false);
    setEventCode('');
    setSubject('');
    setBody('');
    toast.notify({ tone: 'success', title: t('common.save') });
  }

  async function publish(template: NotificationTemplate) {
    await stepUp.run(() =>
      commands.publish.mutateAsync({ id: template.id, etag: etagOf(template.rowVersion) }),
    );
    toast.notify({ tone: 'success', title: t('notifications.templates.publish') });
  }

  const rows = templates.data?.items ?? [];

  return (
    <div className="grid gap-4">
      <div className="flex justify-end">
        {canManage && !creating ? (
          <Button onClick={() => setCreating(true)}>{t('notifications.templates.new')}</Button>
        ) : null}
      </div>

      {creating ? (
        <Card>
          <form onSubmit={submit} className="grid gap-4" noValidate data-testid="template-form">
            <ProblemAlert
              problem={commands.create.error ? problemOf(commands.create.error) : null}
            />
            <div className="grid gap-4 md:grid-cols-3">
              <FormField
                label={t('notifications.templates.event')}
                required
                requiredLabel={t('common.requiredMark')}
              >
                <Input
                  name="eventCode"
                  value={eventCode}
                  onChange={(e) => setEventCode(e.target.value)}
                  className="font-mono"
                  autoComplete="off"
                  spellCheck={false}
                />
              </FormField>
              <FormField label={t('notifications.templates.channel')}>
                <Select
                  name="channel"
                  value={channel}
                  onChange={(e) => setChannel(e.target.value as NotificationChannel)}
                  options={CHANNELS.map((c) => ({
                    value: c,
                    label: t(`notifications.channels.${c}`),
                  }))}
                />
              </FormField>
              <FormField label={t('notifications.templates.locale')}>
                <Input name="locale" value={locale} onChange={(e) => setLocale(e.target.value)} />
              </FormField>
            </div>
            {channel === 'EMAIL' ? (
              <FormField
                label={t('notifications.templates.subject')}
                required
                requiredLabel={t('common.requiredMark')}
              >
                <Input
                  name="subject"
                  value={subject}
                  onChange={(e) => setSubject(e.target.value)}
                />
              </FormField>
            ) : null}
            <FormField
              label={t('notifications.templates.body')}
              required
              requiredLabel={t('common.requiredMark')}
              hint={t('notifications.templates.variablesHelp')}
            >
              <Textarea
                name="body"
                rows={5}
                value={body}
                onChange={(e) => setBody(e.target.value)}
              />
            </FormField>
            <fieldset>
              <legend className="text-sm font-medium">
                {t('notifications.templates.declared')}
              </legend>
              <div className="mt-2 flex flex-wrap gap-x-4 gap-y-2 text-sm">
                {SAFE_VARIABLES.map((name) => (
                  <label key={name} className="inline-flex items-center gap-2">
                    <input
                      type="checkbox"
                      checked={declared.includes(name)}
                      onChange={(e) =>
                        setDeclared((d) =>
                          e.target.checked ? [...d, name] : d.filter((x) => x !== name),
                        )
                      }
                    />
                    <code className="font-mono text-xs">{`{{${name}}}`}</code>
                  </label>
                ))}
              </div>
            </fieldset>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="secondary" onClick={() => setCreating(false)}>
                {t('common.cancel')}
              </Button>
              <Button
                type="submit"
                loading={commands.create.isPending}
                disabled={!eventCode.trim() || !body.trim()}
              >
                {t('common.save')}
              </Button>
            </div>
          </form>
        </Card>
      ) : null}

      <ProblemAlert problem={templates.error ? problemOf(templates.error) : null} />
      <ProblemAlert problem={commands.publish.error ? problemOf(commands.publish.error) : null} />

      {templates.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <EmptyState title={t('notifications.templates.empty')} />
      ) : (
        <Table data-testid="template-table">
          <THead>
            <TR>
              <TH>{t('notifications.templates.event')}</TH>
              <TH>{t('notifications.templates.channel')}</TH>
              <TH>{t('notifications.templates.locale')}</TH>
              <TH className="text-right">{t('notifications.templates.version')}</TH>
              <TH>{t('notifications.templates.declared')}</TH>
              <TH>{t('requests.columns.status')}</TH>
              <TH>
                <span className="sr-only">{t('common.actions')}</span>
              </TH>
            </TR>
          </THead>
          <TBody>
            {rows.map((template) => (
              <TR key={template.id}>
                <TD>
                  <code className="font-mono text-xs">{template.eventCode}</code>
                </TD>
                <TD>{t(`notifications.channels.${template.channel}`)}</TD>
                <TD>{template.locale}</TD>
                <TD className="text-right font-mono text-xs tabular-nums">{template.versionNo}</TD>
                <TD>
                  <span className="font-mono text-xs">{template.declaredVariables.join(', ')}</span>
                </TD>
                <TD>
                  <Badge tone={templateTone(template.status)}>
                    {t(`notifications.status.${template.status}`)}
                  </Badge>
                  {template.status === 'PUBLISHED' ? (
                    <p className="text-fg-muted mt-1 text-xs">
                      {t('notifications.templates.immutable')}
                    </p>
                  ) : null}
                </TD>
                <TD>
                  {canManage && template.status === 'DRAFT' ? (
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => void publish(template)}
                      loading={
                        commands.publish.isPending && commands.publish.variables?.id === template.id
                      }
                      title={t('notifications.templates.replaces')}
                    >
                      {t('notifications.templates.publish')}
                    </Button>
                  ) : null}
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
      <StepUpDialog
        open={stepUp.required}
        action={t('notifications.templates.publish')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </div>
  );
}

function MessageDetail({ messageId, onClose }: { messageId: string; onClose: () => void }) {
  const { t } = useTranslation();
  const detail = useMessage(messageId);
  const commands = useTemplateCommands();
  const canManage = usePermission('notification.manage');
  const m = detail.data?.message;
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={t('notifications.messages.title')}
      description={m?.eventCode}
    >
      {detail.isPending ? (
        <Spinner />
      ) : detail.error || !detail.data ? (
        <ProblemAlert problem={problemOf(detail.error)} />
      ) : (
        <div className="grid gap-4 text-sm">
          <dl className="grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-1">
            <dt className="text-fg-muted">{t('requests.columns.status')}</dt>
            <dd>
              <Badge tone={messageTone(m!.status)}>
                {t(`notifications.messageStatus.${m!.status}`)}
              </Badge>
              {m!.suppressedReason ? (
                <span className="ml-2">{t(`notifications.suppressed.${m!.suppressedReason}`)}</span>
              ) : null}
            </dd>
            <dt className="text-fg-muted">{t('notifications.messages.recipient')}</dt>
            <dd>
              {t(`notifications.recipientTypes.${m!.recipientType}`)} ·{' '}
              <code className="font-mono text-xs">{m!.recipientId}</code>
            </dd>
            <dt className="text-fg-muted">{t('notifications.messages.sentAt')}</dt>
            <dd>{m!.sentAt ? formatDateTime(m!.sentAt) : t('common.none')}</dd>
          </dl>
          {m!.bodyRendered ? (
            <div>
              <h3 className="font-medium">{t('notifications.messages.body')}</h3>
              {m!.subjectRendered ? <p className="mt-1 font-medium">{m!.subjectRendered}</p> : null}
              <pre className="bg-surface-2 mt-1 whitespace-pre-wrap rounded p-3 font-sans text-sm">
                {m!.bodyRendered}
              </pre>
            </div>
          ) : null}
          <div>
            <h3 className="font-medium">{t('notifications.messages.attempts')}</h3>
            {detail.data.deliveries.length === 0 ? (
              <p className="text-fg-muted mt-1">{t('common.none')}</p>
            ) : (
              <ol className="mt-1 grid gap-1">
                {detail.data.deliveries.map((d) => (
                  <li key={d.id} className="flex flex-wrap items-baseline gap-2">
                    <span className="font-mono text-xs tabular-nums">#{d.attemptNo}</span>
                    <span>{formatDateTime(d.attemptedAt)}</span>
                    <code className="font-mono text-xs">{d.providerCode}</code>
                    <Badge tone={d.outcome === 'ACCEPTED' ? 'success' : 'danger'}>
                      {t(`notifications.deliveryOutcome.${d.outcome}`)}
                    </Badge>
                    {d.detail ? <span className="text-fg-muted">{d.detail}</span> : null}
                  </li>
                ))}
              </ol>
            )}
          </div>
          <ProblemAlert problem={commands.resend.error ? problemOf(commands.resend.error) : null} />
          {canManage && (m!.status === 'FAILED' || m!.status === 'SUPPRESSED') ? (
            <div className="flex justify-end">
              <Button
                variant="secondary"
                onClick={() => void commands.resend.mutateAsync(m!.id).then(onClose)}
                loading={commands.resend.isPending}
              >
                {t('notifications.messages.resend')}
              </Button>
            </div>
          ) : null}
        </div>
      )}
    </Dialog>
  );
}

function MessagesTab() {
  const { t } = useTranslation();
  const messages = useMessages({ limit: 100 });
  const [openId, setOpenId] = useState<string | null>(null);
  const rows = messages.data?.items ?? [];
  return (
    <div className="grid gap-4">
      <ProblemAlert problem={messages.error ? problemOf(messages.error) : null} />
      {messages.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <EmptyState title={t('notifications.messages.empty')} />
      ) : (
        <Table data-testid="message-table">
          <THead>
            <TR>
              <TH>{t('notifications.templates.event')}</TH>
              <TH>{t('requests.columns.status')}</TH>
              <TH>{t('notifications.templates.channel')}</TH>
              <TH>{t('notifications.messages.recipient')}</TH>
              <TH>{t('notifications.messages.sentAt')}</TH>
            </TR>
          </THead>
          <TBody>
            {rows.map((m) => (
              <TR key={m.id}>
                <TD>
                  <button
                    type="button"
                    onClick={() => setOpenId(m.id)}
                    className="font-mono text-xs font-medium underline-offset-2 hover:underline"
                  >
                    {m.eventCode}
                  </button>
                </TD>
                <TD>
                  <Badge tone={messageTone(m.status)}>
                    {t(`notifications.messageStatus.${m.status}`)}
                  </Badge>
                  {m.suppressedReason ? (
                    <p className="text-fg-muted mt-1 text-xs">
                      {t(`notifications.suppressed.${m.suppressedReason}`)}
                    </p>
                  ) : null}
                </TD>
                <TD>{t(`notifications.channels.${m.channel}`)}</TD>
                <TD>{t(`notifications.recipientTypes.${m.recipientType}`)}</TD>
                <TD>{m.sentAt ? formatDateTime(m.sentAt) : formatDateTime(m.createdAt)}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
      {openId ? <MessageDetail messageId={openId} onClose={() => setOpenId(null)} /> : null}
    </div>
  );
}

/** Templates and the message log — including what was not sent, and why. */
export function NotificationsPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState('templates');
  return (
    <>
      <PageHeader title={t('notifications.title')} />
      <Tabs
        ariaLabel={t('notifications.title')}
        value={tab}
        onValueChange={setTab}
        tabs={[
          {
            value: 'templates',
            label: t('notifications.templates.title'),
            content: <TemplatesTab />,
          },
          { value: 'messages', label: t('notifications.messages.title'), content: <MessagesTab /> },
        ]}
      />
    </>
  );
}
