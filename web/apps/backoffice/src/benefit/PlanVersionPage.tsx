import type {
  EntitlementDefinitionInput,
  ReasonCommand,
  UpdatePlanVersionRequest,
} from '@kapsora/api-client';
import { usePermission, useSession, useStepUp } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, useTranslation } from '@kapsora/i18n';
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
  StepUpDialog,
  Textarea,
  statusTone,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { Link, useParams } from '@tanstack/react-router';
import { useEffect, useState } from 'react';
import { useForm } from 'react-hook-form';

import { parseIssueMessage, problemOf } from '../problems';
import { DefinitionsEditor, DefinitionsTable } from './DefinitionsEditor';
import { usePlan, usePlanVersion, usePlanVersionCommands } from './queries';
import {
  planVersionFormSchema,
  retireFormSchema,
  type PlanVersionFormValues,
  type RetireFormValues,
} from './schema';

/** Reason codes the server recognises for taking a published version out of use. */
const RETIRE_REASONS = ['SUPERSEDED', 'SPONSOR_REQUEST', 'ERROR', 'REGULATORY'] as const;

/**
 * A plan version: the draft is edited, submitted for review, published by someone else,
 * and eventually retired. Status decides everything on this page, because a published
 * version is what people's entitlements were granted from and must never move under them.
 */
export function PlanVersionPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const { planVersionId } = useParams({ from: '/app/plan-versions/$planVersionId' });
  const actorId = useSession((s) => s.session?.actorId ?? null);
  const canManage = usePermission('plan.manage');
  const canPublish = usePermission('plan.publish');
  const version = usePlanVersion(planVersionId);
  const plan = usePlan(version.data?.data.planId ?? '');
  const commands = usePlanVersionCommands(planVersionId);
  const stepUp = useStepUp();
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [retiring, setRetiring] = useState(false);
  const [comment, setComment] = useState('');
  const [confirming, setConfirming] = useState<'submit' | 'publish' | null>(null);

  // The draft's own period and notes. A version created with the wrong start date would
  // otherwise be stuck: submit refuses without a validFrom and a draft cannot be deleted.
  const periodForm = useForm<PlanVersionFormValues>({
    resolver: zodResolver(planVersionFormSchema),
    defaultValues: { validFrom: '', validTo: '', notes: '', copyFromVersionId: '' },
    mode: 'onBlur',
  });

  const retireForm = useForm<RetireFormValues>({
    resolver: zodResolver(retireFormSchema),
    defaultValues: { reasonCode: 'SUPERSEDED', reasonText: '' },
    mode: 'onBlur',
  });

  const loaded = version.data?.data;
  useEffect(() => {
    if (!loaded) return;
    periodForm.reset({
      validFrom: loaded.validFrom ?? '',
      validTo: loaded.validTo ?? '',
      notes: loaded.notes ?? '',
      copyFromVersionId: '',
    });
  }, [loaded, periodForm]);

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function savePeriod(values: PlanVersionFormValues) {
    if (!version.data) return;
    setProblem(null);
    const patch: UpdatePlanVersionRequest = {
      validFrom: values.validFrom,
      validTo: values.validTo === '' ? null : values.validTo,
      notes: values.notes === '' ? null : values.notes,
    };
    try {
      await commands.patch.mutateAsync({ etag: version.data.etag, patch });
      toast.notify({ tone: 'success', title: t('plans.updated') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function saveDefinitions(items: EntitlementDefinitionInput[]) {
    if (!version.data) return;
    setProblem(null);
    try {
      await commands.definitions.mutateAsync({ etag: version.data.etag, items });
      toast.notify({ tone: 'success', title: t('plans.definitions.saved') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitForReview() {
    if (!version.data) return;
    setProblem(null);
    setConfirming(null);
    try {
      await commands.submit.mutateAsync({
        etag: version.data.etag,
        ...(comment !== '' ? { body: { comment } } : {}),
      });
      setComment('');
      toast.notify({ tone: 'success', title: t('plans.versions.submitted') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function publish() {
    if (!version.data) return;
    setProblem(null);
    setConfirming(null);
    const etag = version.data.etag;
    try {
      // Publishing needs a fresh password; the dialog retries this same call after it.
      await stepUp.run(() =>
        commands.publish.mutateAsync({
          etag,
          ...(comment !== '' ? { body: { comment } } : {}),
        }),
      );
      setComment('');
      toast.notify({ tone: 'success', title: t('plans.versions.published') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function retire(values: RetireFormValues) {
    if (!version.data) return;
    setProblem(null);
    const etag = version.data.etag;
    const body: ReasonCommand = {
      reasonCode: values.reasonCode,
      ...(values.reasonText !== '' ? { reasonText: values.reasonText } : {}),
    };
    try {
      await stepUp.run(() => commands.retire.mutateAsync({ etag, body }));
      setRetiring(false);
      toast.notify({ tone: 'success', title: t('plans.versions.retired') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  if (version.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (!version.data) {
    return <ProblemAlert problem={problemOf(version.error)} />;
  }

  const row = version.data.data;
  const planName = plan.data?.data.name;
  const isDraft = row.status === 'DRAFT';
  const isUnderReview = row.status === 'UNDER_REVIEW';
  const isPublished = row.status === 'PUBLISHED';
  // The submitter may not publish their own work; the server refuses it either way, but
  // offering the button and then explaining the refusal would waste the operator's time.
  const submittedByMe = row.submittedBy !== null && row.submittedBy === actorId;
  const busy =
    commands.submit.isPending ||
    commands.publish.isPending ||
    commands.retire.isPending ||
    stepUp.busy;

  return (
    <>
      <PageHeader
        title={`${planName ?? t('plans.versions.title')} · v${row.versionNo}`}
        description={
          row.validFrom
            ? `${formatDate(row.validFrom)}${row.validTo ? ` – ${formatDate(row.validTo)}` : ''}`
            : undefined
        }
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('programs.title'),
                render: (label) => (
                  <Link to="/programs" search={{}}>
                    {label}
                  </Link>
                ),
              },
              {
                label: planName ?? t('plans.title'),
                render: (label) => (
                  <Link to="/plans/$planId" params={{ planId: row.planId }}>
                    {label}
                  </Link>
                ),
              },
              { label: `v${row.versionNo}` },
            ]}
          />
        }
        actions={
          <>
            <Badge tone={statusTone(row.status)}>
              {t(`plans.versions.statuses.${row.status}`)}
            </Badge>
            {isDraft && canManage ? (
              <Button onClick={() => setConfirming('submit')} disabled={busy}>
                {t('plans.versions.submit')}
              </Button>
            ) : null}
            {isUnderReview && canPublish && !submittedByMe ? (
              <Button onClick={() => setConfirming('publish')} disabled={busy}>
                {t('plans.versions.publish')}
              </Button>
            ) : null}
            {isPublished && canPublish ? (
              <Button variant="secondary" onClick={() => setRetiring(true)} disabled={busy}>
                {t('plans.versions.retire')}
              </Button>
            ) : null}
          </>
        }
      />

      {isUnderReview && submittedByMe ? (
        <p role="status" className="bg-warning-soft text-fg mb-4 rounded-md p-3 text-sm">
          {t('plans.versions.sameActorBlocked')}
        </p>
      ) : null}
      {isPublished || row.status === 'RETIRED' ? (
        <p className="text-fg-muted mb-4 text-sm">{t('plans.versions.readOnly')}</p>
      ) : null}

      <ProblemAlert problem={problem} className="mb-4" />

      <div className="grid gap-4">
        <Card>
          <h2 className="text-base font-semibold">{t('plans.versions.detailTitle')}</h2>
          {isDraft && canManage ? (
            <form
              onSubmit={periodForm.handleSubmit(savePeriod)}
              className="mt-3 grid gap-4"
              data-testid="version-period-form"
            >
              <div className="grid gap-4 md:grid-cols-3">
                <FormField
                  label={t('plans.versions.fields.validFrom')}
                  required
                  requiredLabel={t('common.requiredMark')}
                  error={message(periodForm.formState.errors.validFrom)}
                >
                  <Input {...periodForm.register('validFrom')} type="date" />
                </FormField>
                <FormField
                  label={t('plans.versions.fields.validTo')}
                  error={message(periodForm.formState.errors.validTo)}
                >
                  <Input {...periodForm.register('validTo')} type="date" />
                </FormField>
                <FormField
                  label={t('plans.versions.fields.notes')}
                  error={message(periodForm.formState.errors.notes)}
                >
                  <Input {...periodForm.register('notes')} />
                </FormField>
              </div>
              <div className="flex justify-end">
                <Button type="submit" variant="secondary" loading={commands.patch.isPending}>
                  {t('common.save')}
                </Button>
              </div>
            </form>
          ) : null}
          <dl className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-2 text-sm">
            {!isDraft ? (
              <>
                <dt className="text-fg-muted">{t('plans.versions.fields.validFrom')}</dt>
                <dd>{formatDate(row.validFrom) || t('common.none')}</dd>
                <dt className="text-fg-muted">{t('plans.versions.fields.validTo')}</dt>
                <dd>{formatDate(row.validTo) || t('common.none')}</dd>
                <dt className="text-fg-muted">{t('plans.versions.fields.notes')}</dt>
                <dd>{row.notes || t('common.none')}</dd>
              </>
            ) : null}
            {row.submittedBy ? (
              <>
                <dt className="text-fg-muted">{t('plans.versions.fields.submittedBy')}</dt>
                <dd>{formatDate(row.submittedAt) || t('common.none')}</dd>
              </>
            ) : null}
            {row.publishedBy ? (
              <>
                <dt className="text-fg-muted">{t('plans.versions.fields.publishedBy')}</dt>
                <dd>{formatDate(row.publishedAt) || t('common.none')}</dd>
              </>
            ) : null}
            {row.reviewComment ? (
              <>
                <dt className="text-fg-muted">{t('plans.versions.fields.reviewComment')}</dt>
                <dd>{row.reviewComment}</dd>
              </>
            ) : null}
            {row.configurationHash ? (
              <>
                <dt className="text-fg-muted">{t('plans.versions.fields.configurationHash')}</dt>
                <dd>
                  <code className="font-mono text-xs">{row.configurationHash}</code>
                </dd>
              </>
            ) : null}
          </dl>
        </Card>

        <Card>
          <h2 className="mb-3 text-base font-semibold">{t('plans.definitions.title')}</h2>
          {isDraft && canManage ? (
            <DefinitionsEditor
              definitions={row.definitions}
              onSave={saveDefinitions}
              saving={commands.definitions.isPending}
              problem={null}
            />
          ) : (
            <DefinitionsTable definitions={row.definitions} />
          )}
        </Card>
      </div>

      <Dialog
        open={confirming !== null}
        onOpenChange={(open) => {
          if (!open) setConfirming(null);
        }}
        title={confirming === 'publish' ? t('plans.versions.publish') : t('plans.versions.submit')}
        description={
          confirming === 'publish'
            ? t('plans.versions.publishConfirm')
            : t('plans.versions.submitConfirm')
        }
      >
        <div className="grid gap-4">
          <FormField label={t('plans.versions.fields.reviewComment')}>
            <Textarea value={comment} onChange={(e) => setComment(e.target.value)} rows={3} />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setConfirming(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              loading={busy}
              onClick={() => {
                if (confirming === 'publish') void publish();
                else void submitForReview();
              }}
            >
              {confirming === 'publish' ? t('plans.versions.publish') : t('plans.versions.submit')}
            </Button>
          </div>
        </div>
      </Dialog>

      <Dialog
        open={retiring}
        onOpenChange={(open) => {
          if (!open) setRetiring(false);
        }}
        title={t('plans.versions.retire')}
        description={t('plans.versions.retireConfirm')}
      >
        <form onSubmit={retireForm.handleSubmit(retire)} className="grid gap-4" noValidate>
          <FormField
            label={t('plans.versions.retireReason')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(retireForm.formState.errors.reasonCode)}
          >
            <Select
              {...retireForm.register('reasonCode')}
              options={RETIRE_REASONS.map((reason) => ({
                value: reason,
                label: t(`plans.versions.retireReasons.${reason}`),
              }))}
            />
          </FormField>
          <FormField
            label={t('plans.versions.fields.reviewComment')}
            error={message(retireForm.formState.errors.reasonText)}
          >
            <Textarea {...retireForm.register('reasonText')} rows={3} />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setRetiring(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={busy}>
              {t('plans.versions.retire')}
            </Button>
          </div>
        </form>
      </Dialog>

      <StepUpDialog
        open={stepUp.required}
        action={t('plans.versions.publish')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </>
  );
}
