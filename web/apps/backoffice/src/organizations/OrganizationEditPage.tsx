import {
  ApiError,
  randomId,
  type Organization,
  type UpdateOrganizationRequest,
} from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import {
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
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { problemOf } from '../problems';
import { useEffect, useRef, useState } from 'react';
import { useForm } from 'react-hook-form';
import { OrganizationForm } from './OrganizationForm';
import { useCreateOrganization, useOrganization, useUpdateOrganization } from './queries';
import {
  RELATIONSHIP_STATUSES,
  organizationEditSchema,
  parseIssueMessage,
  toCreateRequest,
  type OrganizationEditValues,
  type OrganizationFormValues,
} from './schema';

export function OrganizationEditPage({ mode }: { mode: 'create' | 'edit' }) {
  return mode === 'create' ? <CreatePage /> : <EditPage />;
}

function CreatePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const create = useCreateOrganization();
  // One idempotency key per form instance: a retried submit replays instead of duplicating.
  const idempotencyKey = useRef(randomId());
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);

  async function submit(values: OrganizationFormValues) {
    setProblem(null);
    try {
      const result = await create.mutateAsync({
        body: toCreateRequest(values),
        idempotencyKey: idempotencyKey.current,
      });
      toast.notify({ tone: 'success', title: t('organizations.created') });
      await navigate({
        to: '/organizations/$organizationId',
        params: { organizationId: result.data.id },
      });
    } catch (err) {
      setProblem(problemOf(err));
      if (err instanceof ApiError && err.status === 422) {
        // Validation failed before anything was written: a new key for the next attempt.
        idempotencyKey.current = randomId();
      }
    }
  }

  return (
    <>
      <PageHeader
        title={t('organizations.createTitle')}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('organizations.title'),
                render: (l) => (
                  <Link to="/organizations" search={{}}>
                    {l}
                  </Link>
                ),
              },
              { label: t('organizations.createTitle') },
            ]}
          />
        }
      />
      <Card>
        <OrganizationForm
          onSubmit={submit}
          onCancel={() => void navigate({ to: '/organizations', search: {} })}
          busy={create.isPending}
          problem={problem}
        />
      </Card>
    </>
  );
}

interface Conflict {
  server: Organization;
  serverEtag: string;
  yours: OrganizationEditValues;
}

function EditPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const { organizationId } = useParams({ from: '/app/organizations/$organizationId/edit' });
  const query = useOrganization(organizationId);
  const update = useUpdateOrganization(organizationId);
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);
  const [conflict, setConflict] = useState<Conflict | null>(null);
  const form = useForm<OrganizationEditValues>({
    resolver: zodResolver(organizationEditSchema),
    defaultValues: { displayName: '', relationshipStatus: 'ACTIVE', tenantCode: '' },
    mode: 'onBlur',
  });

  const loaded = query.data;
  useEffect(() => {
    if (loaded && !form.formState.isDirty) {
      const o = loaded.data;
      form.reset({
        displayName: o.displayName,
        relationshipStatus: o.relationshipStatus === 'PENDING' ? 'ACTIVE' : o.relationshipStatus,
        tenantCode: o.tenantCode ?? '',
      });
    }
  }, [loaded, form]);

  function message(err: { message?: string } | undefined): string | undefined {
    if (!err?.message) return undefined;
    const { code, params } = parseIssueMessage(err.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  function toPatch(
    values: OrganizationEditValues,
    current: Organization,
  ): UpdateOrganizationRequest {
    const patch: UpdateOrganizationRequest = {};
    if (values.displayName !== current.displayName) patch.displayName = values.displayName;
    if (values.relationshipStatus !== current.relationshipStatus)
      patch.relationshipStatus = values.relationshipStatus;
    const code = values.tenantCode === '' ? null : values.tenantCode;
    if (code !== (current.tenantCode ?? null)) patch.tenantCode = code;
    return patch;
  }

  async function submit(values: OrganizationEditValues) {
    if (!loaded) return;
    setProblem(null);
    const patch = toPatch(values, loaded.data);
    if (Object.keys(patch).length === 0) {
      await navigate({ to: '/organizations/$organizationId', params: { organizationId } });
      return;
    }
    try {
      await update.mutateAsync({ etag: loaded.etag, patch });
      toast.notify({ tone: 'success', title: t('organizations.updated') });
      await navigate({ to: '/organizations/$organizationId', params: { organizationId } });
    } catch (err) {
      if (err instanceof ApiError && err.status === 412) {
        // Never overwrite silently: fetch the server version and show both (v1.2 15.5).
        const latest = await query.refetch();
        if (latest.data)
          setConflict({ server: latest.data.data, serverEtag: latest.data.etag, yours: values });
        return;
      }
      setProblem(problemOf(err));
    }
  }

  function reloadLatest() {
    if (!conflict) return;
    form.reset({
      displayName: conflict.server.displayName,
      relationshipStatus:
        conflict.server.relationshipStatus === 'PENDING'
          ? 'ACTIVE'
          : conflict.server.relationshipStatus,
      tenantCode: conflict.server.tenantCode ?? '',
    });
    setConflict(null);
  }

  if (query.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (query.error || !loaded) {
    return <ProblemAlert problem={problemOf(query.error)} />;
  }
  const errors = form.formState.errors;
  const sharedReadOnly = problem?.code === 'ORGANIZATION_SHARED_READONLY';

  return (
    <>
      <PageHeader
        title={t('organizations.editTitle')}
        description={loaded.data.legalName}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('organizations.title'),
                render: (l) => (
                  <Link to="/organizations" search={{}}>
                    {l}
                  </Link>
                ),
              },
              {
                label: loaded.data.displayName,
                render: (l) => (
                  <Link to="/organizations/$organizationId" params={{ organizationId }}>
                    {l}
                  </Link>
                ),
              },
              { label: t('organizations.editTitle') },
            ]}
          />
        }
      />
      <Card>
        <form onSubmit={form.handleSubmit(submit)} className="grid gap-5" noValidate>
          <ProblemAlert problem={problem} hideFieldErrors />
          {sharedReadOnly ? (
            <p className="text-fg-muted text-sm">{t('organizations.sharedReadOnly')}</p>
          ) : null}
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('organizations.fields.displayName')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(errors.displayName)}
            >
              <Input {...form.register('displayName')} />
            </FormField>
            <FormField
              label={t('organizations.fields.relationshipStatus')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(errors.relationshipStatus)}
            >
              <Select
                {...form.register('relationshipStatus')}
                options={RELATIONSHIP_STATUSES.map((s) => ({
                  value: s,
                  label: t(`organizations.statuses.${s}`),
                }))}
              />
            </FormField>
            <FormField
              label={t('organizations.fields.tenantCode')}
              hint={t('organizations.fields.tenantCodeHint')}
              error={message(errors.tenantCode)}
            >
              <Input {...form.register('tenantCode')} />
            </FormField>
          </div>
          <p className="text-fg-muted text-xs">
            {t('organizations.fields.rowVersion')}: {loaded.data.rowVersion}
          </p>
          <div className="flex justify-end gap-2">
            <Button
              variant="secondary"
              onClick={() =>
                void navigate({ to: '/organizations/$organizationId', params: { organizationId } })
              }
              disabled={update.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={update.isPending}>
              {update.isPending ? t('common.saving') : t('common.save')}
            </Button>
          </div>
        </form>
      </Card>

      <Dialog
        open={conflict !== null}
        onOpenChange={(open) => {
          if (!open) setConflict(null);
        }}
        title={t('organizations.conflictTitle')}
        description={t('organizations.conflictBody')}
        actions={
          <>
            <Button variant="secondary" onClick={() => setConflict(null)}>
              {t('common.close')}
            </Button>
            <Button onClick={reloadLatest}>{t('organizations.reloadLatest')}</Button>
          </>
        }
      >
        {conflict ? (
          <table className="k-table" data-testid="conflict-table">
            <thead>
              <tr>
                <th />
                <th>{t('organizations.conflictYours')}</th>
                <th>{t('organizations.conflictServer')}</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>{t('organizations.fields.displayName')}</td>
                <td>{conflict.yours.displayName}</td>
                <td>{conflict.server.displayName}</td>
              </tr>
              <tr>
                <td>{t('organizations.fields.relationshipStatus')}</td>
                <td>{t(`organizations.statuses.${conflict.yours.relationshipStatus}`)}</td>
                <td>{t(`organizations.statuses.${conflict.server.relationshipStatus}`)}</td>
              </tr>
              <tr>
                <td>{t('organizations.fields.tenantCode')}</td>
                <td>{conflict.yours.tenantCode || t('common.none')}</td>
                <td>{conflict.server.tenantCode ?? t('common.none')}</td>
              </tr>
              <tr>
                <td>{t('organizations.fields.rowVersion')}</td>
                <td>{loaded.data.rowVersion}</td>
                <td>{conflict.server.rowVersion}</td>
              </tr>
            </tbody>
          </table>
        ) : null}
      </Dialog>
    </>
  );
}
