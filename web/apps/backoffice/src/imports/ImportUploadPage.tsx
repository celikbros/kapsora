import { ApiError, randomId, type UploadImportInput } from '@kapsora/api-client';
import { usePermission, useStepUp } from '@kapsora/auth';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import {
  Breadcrumb,
  Button,
  Card,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  StepUpDialog,
  useToast,
} from '@kapsora/ui';
import { Link, useNavigate } from '@tanstack/react-router';
import { useRef, useState, type ChangeEvent, type FormEvent } from 'react';

import { usePlans, usePrograms } from '../benefit/queries';
import { useOrganizationList } from '../organizations/queries';
import { problemOf } from '../problems';
import { useUploadImport } from './queries';

/**
 * Uploads a sponsor's member file. Nothing reaches the live tables here: the file is
 * staged, validated and matched, and the operator continues on the batch page. The
 * server asks for a password re-entry first, and one idempotency key per form instance
 * means a retried submit replays the stored answer instead of staging the file twice.
 */
export function ImportUploadPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const canManage = usePermission('import.execute');
  const canReadPrograms = usePermission('program.read');
  const stepUp = useStepUp();
  const upload = useUploadImport();

  const sponsors = useOrganizationList({ role: 'SPONSOR', limit: 200 });
  const programs = usePrograms({ status: 'ACTIVE', limit: 200 });
  const [programId, setProgramId] = useState('');
  const plans = usePlans(programId);

  const [file, setFile] = useState<File | null>(null);
  const [sponsorOrganizationId, setSponsorOrganizationId] = useState('');
  const [sourceSystem, setSourceSystem] = useState('');
  const [sourceVersion, setSourceVersion] = useState('');
  const [planId, setPlanId] = useState('');
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);
  const idempotencyKey = useRef(randomId());

  function pickFile(event: ChangeEvent<HTMLInputElement>) {
    setFile(event.target.files?.[0] ?? null);
  }

  function fieldError(name: string): string | undefined {
    const found = problem?.errors?.find((entry) => entry.field === name);
    return found ? fieldErrorMessage(t, found.code, found.message) : undefined;
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!file) return;
    setProblem(null);
    const body: UploadImportInput = {
      file,
      fileName: file.name,
      sponsorOrganizationId,
      sourceSystem: sourceSystem.trim(),
      sourceVersion: sourceVersion.trim(),
      ...(planId ? { planId } : {}),
    };
    try {
      const created = await stepUp.run(() =>
        upload.mutateAsync({ body, idempotencyKey: idempotencyKey.current }),
      );
      // The operator cancelled the password prompt; the file is still on the form.
      if (!created) return;
      toast.notify({ tone: 'success', title: t('imports.uploaded') });
      await navigate({ to: '/imports/$importId', params: { importId: created.data.id } });
    } catch (err) {
      setProblem(problemOf(err));
      if (err instanceof ApiError && err.status === 422) {
        // Nothing was staged, so the next attempt is a fresh command.
        idempotencyKey.current = randomId();
      }
    }
  }

  const breadcrumb = (
    <Breadcrumb
      items={[
        {
          label: t('imports.title'),
          render: (label) => <Link to="/imports">{label}</Link>,
        },
        { label: t('imports.uploadTitle') },
      ]}
    />
  );

  if (!canManage) {
    return (
      <>
        <PageHeader title={t('imports.uploadTitle')} breadcrumb={breadcrumb} />
        <EmptyState title={t('imports.notAllowed')} />
      </>
    );
  }

  const ready =
    file !== null &&
    sponsorOrganizationId !== '' &&
    sourceSystem.trim() !== '' &&
    sourceVersion.trim() !== '';

  return (
    <>
      <PageHeader
        title={t('imports.uploadTitle')}
        description={t('imports.intro')}
        breadcrumb={breadcrumb}
      />
      <Card>
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <FormField
            label={t('imports.fields.file')}
            hint={t('imports.fields.fileHint')}
            error={fieldError('file')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input type="file" accept=".csv,text/csv" onChange={pickFile} required />
          </FormField>

          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('imports.fields.sponsor')}
              error={fieldError('sponsorOrganizationId')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Select
                name="sponsorOrganizationId"
                value={sponsorOrganizationId}
                onChange={(e) => setSponsorOrganizationId(e.target.value)}
                placeholder={t('imports.fields.sponsorPlaceholder')}
                options={(sponsors.data?.items ?? [])
                  .filter((entry) => entry.relationshipStatus === 'ACTIVE')
                  .map((entry) => ({ value: entry.id, label: entry.displayName }))}
                required
              />
            </FormField>
            <FormField
              label={t('imports.fields.sourceSystem')}
              error={fieldError('sourceSystem')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="sourceSystem"
                value={sourceSystem}
                onChange={(e) => setSourceSystem(e.target.value)}
                autoComplete="off"
                required
              />
            </FormField>
          </div>

          <FormField
            label={t('imports.fields.sourceVersion')}
            hint={t('imports.fields.sourceVersionHint')}
            error={fieldError('sourceVersion')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              name="sourceVersion"
              value={sourceVersion}
              onChange={(e) => setSourceVersion(e.target.value)}
              autoComplete="off"
              className="md:w-1/2"
              required
            />
          </FormField>

          {canReadPrograms ? (
            <div className="grid gap-4 md:grid-cols-2">
              <FormField label={t('imports.fields.program')}>
                <Select
                  name="programId"
                  value={programId}
                  onChange={(e) => {
                    setProgramId(e.target.value);
                    setPlanId('');
                  }}
                  placeholder={t('common.none')}
                  options={(programs.data?.items ?? []).map((program) => ({
                    value: program.id,
                    label: `${program.code} · ${program.name}`,
                  }))}
                />
              </FormField>
              <FormField
                label={t('imports.fields.plan')}
                hint={t('imports.fields.planHint')}
                error={fieldError('planId')}
              >
                <Select
                  name="planId"
                  value={planId}
                  onChange={(e) => setPlanId(e.target.value)}
                  placeholder={t('common.none')}
                  options={(plans.data ?? [])
                    .filter((plan) => plan.status === 'ACTIVE')
                    .map((plan) => ({ value: plan.id, label: `${plan.code} · ${plan.name}` }))}
                  disabled={programId === ''}
                />
              </FormField>
            </div>
          ) : null}

          <ProblemAlert problem={problem} hideFieldErrors />

          <div className="flex justify-end gap-2">
            <Button
              variant="secondary"
              onClick={() => void navigate({ to: '/imports' })}
              disabled={upload.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={upload.isPending} disabled={!ready}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Card>

      <StepUpDialog
        open={stepUp.required}
        action={t('imports.uploadTitle')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </>
  );
}
