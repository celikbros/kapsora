import {
  randomId,
  type CreatePlanVersionRequest,
  type PlanVersionSummary,
  type UpdatePlanRequest,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
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
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  Textarea,
  statusTone,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { parseIssueMessage, problemOf } from '../problems';
import { useCreatePlanVersion, usePlan, useProgram, useUpdatePlan } from './queries';
import { PLAN_TRANSITIONS, planVersionFormSchema, type PlanVersionFormValues } from './schema';

/** Half-open period, printed the way the operator reads a coverage window. */
function period(t: (key: string) => string, from?: string | null, to?: string | null): string {
  const start = formatDate(from) || t('common.none');
  return to ? `${start} – ${formatDate(to)}` : start;
}

/** One plan with its version history; a version is where the entitlements actually live. */
export function PlanDetailPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const { planId } = useParams({ from: '/app/plans/$planId' });
  const canManage = usePermission('plan.manage');
  const plan = usePlan(planId);
  const program = useProgram(plan.data?.data.programId ?? '');
  const update = useUpdatePlan(planId);
  const createVersion = useCreatePlanVersion(planId);
  const [adding, setAdding] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [versionKey, setVersionKey] = useState(randomId);

  const form = useForm<PlanVersionFormValues>({
    resolver: zodResolver(planVersionFormSchema),
    defaultValues: { validFrom: '', validTo: '', notes: '', copyFromVersionId: '' },
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function changeStatus(status: string) {
    if (!plan.data || status === '') return;
    setProblem(null);
    const patch: UpdatePlanRequest = { status: status as 'ACTIVE' | 'RETIRED' };
    try {
      await update.mutateAsync({ etag: plan.data.etag, patch });
      toast.notify({ tone: 'success', title: t('plans.updated') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitVersion(values: PlanVersionFormValues) {
    setProblem(null);
    const body: CreatePlanVersionRequest = {
      validFrom: values.validFrom,
      ...(values.validTo !== '' ? { validTo: values.validTo } : {}),
      ...(values.notes !== '' ? { notes: values.notes } : {}),
      ...(values.copyFromVersionId !== '' ? { copyFromVersionId: values.copyFromVersionId } : {}),
    };
    try {
      const created = await createVersion.mutateAsync({ body, idempotencyKey: versionKey });
      setAdding(false);
      setVersionKey(randomId());
      await navigate({
        to: '/plan-versions/$planVersionId',
        params: { planVersionId: created.data.id },
      });
    } catch (err) {
      setProblem(problemOf(err));
      setVersionKey(randomId());
    }
  }

  if (plan.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (!plan.data) {
    return <ProblemAlert problem={problemOf(plan.error)} />;
  }

  const row = plan.data.data;
  const nextStatuses = PLAN_TRANSITIONS[row.status] ?? [];
  // Newest first: the version an operator needs is almost always the most recent one.
  const versions: PlanVersionSummary[] = [...row.versions].sort(
    (a, b) => b.versionNo - a.versionNo,
  );
  const programName = program.data?.data.name;

  return (
    <>
      <PageHeader
        title={row.name}
        description={row.code}
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
                label: programName ?? t('plans.title'),
                render: (label) => (
                  <Link to="/programs/$programId" params={{ programId: row.programId }}>
                    {label}
                  </Link>
                ),
              },
              { label: row.name },
            ]}
          />
        }
        actions={
          <>
            <Badge tone={statusTone(row.status)}>{t(`plans.statuses.${row.status}`)}</Badge>
            {canManage ? (
              <Button size="sm" variant="secondary" onClick={() => setAdding(true)}>
                {t('plans.versions.new')}
              </Button>
            ) : null}
          </>
        }
      />

      <ProblemAlert problem={problem} className="mb-4" />

      <Card>
        <h2 className="text-base font-semibold">{t('plans.versions.title')}</h2>
        {versions.length === 0 ? (
          <EmptyState title={t('plans.empty')} />
        ) : (
          <div className="mt-3">
            <Table data-testid="version-table">
              <THead>
                <TR>
                  <TH>{t('plans.versions.columns.versionNo')}</TH>
                  <TH>{t('plans.versions.columns.period')}</TH>
                  <TH>{t('plans.versions.columns.published')}</TH>
                  <TH>{t('plans.versions.columns.status')}</TH>
                </TR>
              </THead>
              <TBody>
                {versions.map((version) => (
                  <TR key={version.id}>
                    <TD>
                      <Link
                        to="/plan-versions/$planVersionId"
                        params={{ planVersionId: version.id }}
                        className="font-medium underline-offset-2 hover:underline"
                      >
                        {`v${version.versionNo}`}
                      </Link>
                    </TD>
                    <TD>{period(t, version.validFrom, version.validTo)}</TD>
                    <TD>{formatDate(version.publishedAt) || t('common.none')}</TD>
                    <TD>
                      <Badge tone={statusTone(version.status)}>
                        {t(`plans.versions.statuses.${version.status}`)}
                      </Badge>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </div>
        )}

        {canManage && nextStatuses.length > 0 ? (
          <div className="border-line mt-4 border-t pt-4">
            <FormField label={t('plans.fields.status')}>
              <Select
                name="status"
                value=""
                onChange={(e) => void changeStatus(e.target.value)}
                placeholder={t('programs.changeStatus')}
                options={nextStatuses.map((status) => ({
                  value: status,
                  label: t(`plans.statuses.${status}`),
                }))}
                disabled={update.isPending}
                className="w-56"
              />
            </FormField>
          </div>
        ) : null}
      </Card>

      <Dialog
        open={adding}
        onOpenChange={(open) => {
          if (!open) setAdding(false);
        }}
        title={t('plans.versions.createTitle')}
      >
        <form onSubmit={form.handleSubmit(submitVersion)} className="grid gap-4" noValidate>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('plans.versions.fields.validFrom')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.validFrom)}
            >
              <Input {...form.register('validFrom')} type="date" />
            </FormField>
            <FormField
              label={t('plans.versions.fields.validTo')}
              error={message(form.formState.errors.validTo)}
            >
              <Input {...form.register('validTo')} type="date" />
            </FormField>
          </div>
          <FormField label={t('plans.versions.copyFrom')}>
            <Select
              {...form.register('copyFromVersionId')}
              placeholder={t('plans.versions.copyFromNone')}
              options={versions.map((version) => ({
                value: version.id,
                label: `v${version.versionNo} · ${t(`plans.versions.statuses.${version.status}`)}`,
              }))}
            />
          </FormField>
          <FormField
            label={t('plans.versions.fields.notes')}
            error={message(form.formState.errors.notes)}
          >
            <Textarea {...form.register('notes')} rows={3} />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setAdding(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={createVersion.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}
