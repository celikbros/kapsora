import type { Provider, UpdateProviderRequest } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
  Dialog,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  Tabs,
  Textarea,
  statusTone,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useParams } from '@tanstack/react-router';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { problemOf } from '../problems';
import { CapabilitiesTab } from './CapabilitiesTab';
import { LocationsTab } from './LocationsTab';
import { PractitionersTab } from './PractitionersTab';
import { useProvider, useProviderTransition, useUpdateProvider } from './queries';
import { ProviderLink } from './routes';
import {
  PROVIDER_TRANSITIONS,
  PROVIDER_TYPES,
  emptyReasonForm,
  parseIssueMessage,
  providerFormSchema,
  reasonFormSchema,
  type ProviderFormValues,
  type ReasonFormValues,
} from './schema';

/** One provider with its network underneath, one tab per concern. */
export function ProviderDetailPage() {
  const { t } = useTranslation();
  const params: Record<string, unknown> = useParams({ strict: false });
  const providerId = typeof params['providerId'] === 'string' ? params['providerId'] : '';
  const query = useProvider(providerId);
  const [tab, setTab] = useState('profile');

  if (query.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (query.error || !query.data) {
    return (
      <ProblemAlert
        page
        problem={problemOf(query.error)}
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
    );
  }

  const provider = query.data.data;

  return (
    <>
      <PageHeader
        title={provider.organizationName}
        description={t(`providers.types.${provider.providerType}`)}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('providers.title'),
                render: (label) => (
                  <ProviderLink target={{ to: '/providers', search: {} }}>{label}</ProviderLink>
                ),
              },
              { label: provider.organizationName },
            ]}
          />
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={statusTone(provider.status)}>
              {t(`providers.statuses.${provider.status}`)}
            </Badge>
            <LifecycleCommands providerId={providerId} provider={provider} etag={query.data.etag} />
          </div>
        }
      />

      <Tabs
        ariaLabel={t('providers.detailTitle')}
        value={tab}
        onValueChange={setTab}
        tabs={[
          {
            value: 'profile',
            label: t('providers.tabs.profile'),
            content: (
              <ProfileTab providerId={providerId} provider={provider} etag={query.data.etag} />
            ),
          },
          {
            value: 'locations',
            label: t('providers.tabs.locations'),
            content: <LocationsTab providerId={providerId} />,
          },
          {
            value: 'capabilities',
            label: t('providers.tabs.capabilities'),
            content: <CapabilitiesTab providerId={providerId} />,
          },
          {
            value: 'practitioners',
            label: t('providers.tabs.practitioners'),
            content: <PractitionersTab providerId={providerId} />,
          },
        ]}
      />
    </>
  );
}

interface CommandProps {
  providerId: string;
  provider: Provider;
  etag: string;
}

/**
 * Aktifleştir, Askıya al and Sonlandır. The status is never a field on a form: each
 * command is its own request, only the transitions the server accepts are offered, and
 * terminating says in the dialog that it cannot be undone.
 */
function LifecycleCommands({ providerId, provider, etag }: CommandProps) {
  const { t } = useTranslation();
  const toast = useToast();
  const canManage = usePermission('provider.manage');
  const transition = useProviderTransition(providerId);
  const [asking, setAsking] = useState<'suspend' | 'terminate' | null>(null);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);

  const form = useForm<ReasonFormValues>({
    resolver: zodResolver(reasonFormSchema),
    defaultValues: emptyReasonForm,
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  const allowed = PROVIDER_TRANSITIONS[provider.status] ?? [];
  if (!canManage || allowed.length === 0) return null;

  async function activate() {
    setProblem(null);
    try {
      await transition.mutateAsync({ kind: 'activate', etag });
      toast.notify({ tone: 'success', title: t('providers.activated') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitReason(values: ReasonFormValues) {
    if (!asking) return;
    setProblem(null);
    const body = {
      reasonCode: values.reasonCode,
      ...(values.reasonText ? { reasonText: values.reasonText } : {}),
    };
    try {
      await transition.mutateAsync(
        asking === 'suspend' ? { kind: 'suspend', etag, body } : { kind: 'terminate', etag, body },
      );
      toast.notify({
        tone: 'success',
        title: asking === 'suspend' ? t('providers.suspended') : t('providers.terminated'),
      });
      form.reset(emptyReasonForm);
      setAsking(null);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  return (
    <>
      {allowed.includes('ACTIVE') ? (
        <Button size="sm" variant="secondary" onClick={() => void activate()}>
          {t('providers.activate')}
        </Button>
      ) : null}
      {allowed.includes('SUSPENDED') ? (
        <Button size="sm" variant="secondary" onClick={() => setAsking('suspend')}>
          {t('providers.suspend')}
        </Button>
      ) : null}
      {allowed.includes('TERMINATED') ? (
        <Button size="sm" variant="danger" onClick={() => setAsking('terminate')}>
          {t('providers.terminate')}
        </Button>
      ) : null}

      {problem && asking === null ? <ProblemAlert problem={problem} className="w-full" /> : null}

      <Dialog
        open={asking !== null}
        onOpenChange={(open) => {
          if (!open) setAsking(null);
        }}
        title={asking === 'terminate' ? t('providers.terminateTitle') : t('providers.suspendTitle')}
        description={asking === 'terminate' ? t('providers.terminateConfirm') : undefined}
      >
        <form onSubmit={form.handleSubmit(submitReason)} className="grid gap-4" noValidate>
          <FormField
            label={t('providers.reasonCode')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.reasonCode)}
          >
            <Input {...form.register('reasonCode')} autoComplete="off" className="font-mono" />
          </FormField>
          <FormField
            label={t('providers.reasonText')}
            error={message(form.formState.errors.reasonText)}
          >
            <Textarea {...form.register('reasonText')} rows={3} />
          </FormField>
          <ProblemAlert problem={problem} hideFieldErrors />
          <div className="flex justify-end gap-2">
            <Button
              variant="secondary"
              onClick={() => setAsking(null)}
              disabled={transition.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button
              type="submit"
              variant={asking === 'terminate' ? 'danger' : 'primary'}
              loading={transition.isPending}
            >
              {asking === 'terminate' ? t('providers.terminate') : t('providers.suspend')}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}

/** The profile: a merge-patch form carrying the ETag the detail read returned. */
function ProfileTab({ providerId, provider, etag }: CommandProps) {
  const { t } = useTranslation();
  const toast = useToast();
  const canManage = usePermission('provider.manage');
  const update = useUpdateProvider(providerId);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);

  const form = useForm<ProviderFormValues>({
    resolver: zodResolver(providerFormSchema),
    defaultValues: {
      tenantOrganizationId: provider.tenantOrganizationId,
      providerType: provider.providerType,
      networkTier: provider.networkTier ?? '',
      contractedFrom: provider.contractedFrom ?? '',
      contractedTo: provider.contractedTo ?? '',
      notes: provider.notes ?? '',
    },
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function submit(values: ProviderFormValues) {
    setProblem(null);
    const patch: UpdateProviderRequest = {
      providerType: values.providerType,
      networkTier: values.networkTier === '' ? null : values.networkTier,
      contractedFrom: values.contractedFrom === '' ? null : values.contractedFrom,
      contractedTo: values.contractedTo === '' ? null : values.contractedTo,
      notes: values.notes === '' ? null : values.notes,
    };
    try {
      await update.mutateAsync({ etag, patch });
      toast.notify({ tone: 'success', title: t('providers.updated') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  if (!canManage) {
    return (
      <Card>
        <dl className="grid gap-4 text-sm md:grid-cols-2">
          <Field label={t('providers.fields.organization')} value={provider.organizationName} />
          <Field
            label={t('providers.fields.providerType')}
            value={t(`providers.types.${provider.providerType}`)}
          />
          <Field
            label={t('providers.fields.networkTier')}
            value={provider.networkTier ?? t('common.none')}
          />
          <Field
            label={t('providers.fields.status')}
            value={t(`providers.statuses.${provider.status}`)}
          />
          <Field
            label={t('providers.fields.contractedFrom')}
            value={provider.contractedFrom ?? t('common.none')}
          />
          <Field
            label={t('providers.fields.contractedTo')}
            value={provider.contractedTo ?? t('common.none')}
          />
          <Field label={t('providers.fields.notes')} value={provider.notes ?? t('common.none')} />
        </dl>
      </Card>
    );
  }

  return (
    <Card>
      <form
        onSubmit={form.handleSubmit(submit)}
        className="grid gap-4"
        noValidate
        data-testid="provider-profile-form"
      >
        <div className="grid gap-4 md:grid-cols-2">
          <FormField label={t('providers.fields.organization')}>
            <Input value={provider.organizationName} readOnly />
          </FormField>
          <FormField
            label={t('providers.fields.providerType')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.providerType)}
          >
            <Select
              {...form.register('providerType')}
              options={PROVIDER_TYPES.map((type) => ({
                value: type,
                label: t(`providers.types.${type}`),
              }))}
            />
          </FormField>
          <FormField
            label={t('providers.fields.networkTier')}
            error={message(form.formState.errors.networkTier)}
          >
            <Input {...form.register('networkTier')} autoComplete="off" className="font-mono" />
          </FormField>
          <div />
          <FormField
            label={t('providers.fields.contractedFrom')}
            error={message(form.formState.errors.contractedFrom)}
          >
            <Input {...form.register('contractedFrom')} type="date" />
          </FormField>
          <FormField
            label={t('providers.fields.contractedTo')}
            error={message(form.formState.errors.contractedTo)}
          >
            <Input {...form.register('contractedTo')} type="date" />
          </FormField>
        </div>
        <FormField label={t('providers.fields.notes')} error={message(form.formState.errors.notes)}>
          <Textarea {...form.register('notes')} rows={3} />
        </FormField>

        <ProblemAlert problem={problem} />

        <div className="flex justify-end">
          <Button type="submit" loading={update.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1">
      <dt className="text-fg-muted text-xs font-medium">{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}
