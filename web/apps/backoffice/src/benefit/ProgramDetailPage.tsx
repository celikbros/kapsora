import { randomId, type Plan, type UpdateProgramRequest } from '@kapsora/api-client';
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
  statusTone,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { Link, useParams } from '@tanstack/react-router';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { parseIssueMessage, problemOf } from '../problems';
import { useCreatePlan, usePlans, useProgram, useUpdateProgram } from './queries';
import { PROGRAM_TRANSITIONS, planFormSchema, type PlanFormValues } from './schema';

/** One program with the plans underneath it. */
export function ProgramDetailPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const { programId } = useParams({ from: '/app/programs/$programId' });
  const canManage = usePermission('program.manage');
  const canManagePlan = usePermission('plan.manage');
  const program = useProgram(programId);
  const plans = usePlans(programId);
  const update = useUpdateProgram(programId);
  const createPlan = useCreatePlan(programId);
  const [adding, setAdding] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [planKey, setPlanKey] = useState(randomId);

  const form = useForm<PlanFormValues>({
    resolver: zodResolver(planFormSchema),
    defaultValues: { code: '', name: '' },
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function changeStatus(status: string) {
    if (!program.data || status === '') return;
    setProblem(null);
    const patch: UpdateProgramRequest = { status: status as 'ACTIVE' | 'SUSPENDED' | 'CLOSED' };
    try {
      await update.mutateAsync({ etag: program.data.etag, patch });
      toast.notify({ tone: 'success', title: t('programs.updated') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitPlan(values: PlanFormValues) {
    setProblem(null);
    try {
      await createPlan.mutateAsync({ body: values, idempotencyKey: planKey });
      toast.notify({ tone: 'success', title: t('plans.created') });
      setAdding(false);
      form.reset({ code: '', name: '' });
      setPlanKey(randomId());
    } catch (err) {
      setProblem(problemOf(err));
      setPlanKey(randomId());
    }
  }

  if (program.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (!program.data) {
    return <ProblemAlert problem={problemOf(program.error)} />;
  }

  const row = program.data.data;
  const nextStatuses = PROGRAM_TRANSITIONS[row.status] ?? [];
  const planRows: Plan[] = plans.data ?? [];

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
              { label: row.name },
            ]}
          />
        }
        actions={
          <Badge tone={statusTone(row.status)}>{t(`programs.statuses.${row.status}`)}</Badge>
        }
      />

      <ProblemAlert problem={problem} className="mb-4" />

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <h2 className="text-base font-semibold">{t('programs.detailTitle')}</h2>
          <dl className="mt-3 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-fg-muted">{t('programs.fields.programType')}</dt>
            <dd>{t(`programs.types.${row.programType}`, { defaultValue: row.programType })}</dd>
            <dt className="text-fg-muted">{t('programs.fields.sponsorOrganization')}</dt>
            <dd>{row.sponsorDisplayName ?? t('common.none')}</dd>
            <dt className="text-fg-muted">{t('programs.fields.payerOrganization')}</dt>
            <dd>{row.payerDisplayName ?? t('common.none')}</dd>
            <dt className="text-fg-muted">{t('programs.fields.validFrom')}</dt>
            <dd>{formatDate(row.validFrom) || t('common.none')}</dd>
            <dt className="text-fg-muted">{t('programs.fields.validTo')}</dt>
            <dd>{formatDate(row.validTo) || t('common.none')}</dd>
          </dl>

          {canManage && nextStatuses.length > 0 ? (
            <div className="border-line mt-4 border-t pt-4">
              <FormField label={t('programs.fields.status')}>
                <Select
                  name="status"
                  value=""
                  onChange={(e) => void changeStatus(e.target.value)}
                  placeholder={t('programs.changeStatus')}
                  options={nextStatuses.map((status) => ({
                    value: status,
                    label: t(`programs.statuses.${status}`),
                  }))}
                  disabled={update.isPending}
                  className="w-56"
                />
              </FormField>
            </div>
          ) : null}
        </Card>

        <Card>
          <div className="flex items-start justify-between gap-3">
            <h2 className="text-base font-semibold">{t('plans.title')}</h2>
            {canManagePlan ? (
              <Button size="sm" variant="secondary" onClick={() => setAdding(true)}>
                {t('plans.new')}
              </Button>
            ) : null}
          </div>

          {plans.isPending ? (
            <div className="text-fg-muted flex items-center gap-2 py-4 text-sm" aria-busy="true">
              <Spinner /> {t('common.loading')}
            </div>
          ) : planRows.length === 0 ? (
            <EmptyState title={t('plans.empty')} />
          ) : (
            <div className="mt-3">
              <Table data-testid="plan-table">
                <THead>
                  <TR>
                    <TH>{t('plans.columns.code')}</TH>
                    <TH>{t('plans.columns.name')}</TH>
                    <TH>{t('plans.columns.versions')}</TH>
                    <TH>{t('plans.columns.status')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {planRows.map((plan) => (
                    <TR key={plan.id}>
                      <TD>
                        <code className="font-mono text-xs">{plan.code}</code>
                      </TD>
                      <TD>
                        <Link
                          to="/plans/$planId"
                          params={{ planId: plan.id }}
                          className="font-medium underline-offset-2 hover:underline"
                        >
                          {plan.name}
                        </Link>
                      </TD>
                      <TD>{plan.versions.length}</TD>
                      <TD>
                        <Badge tone={statusTone(plan.status)}>
                          {t(`plans.statuses.${plan.status}`)}
                        </Badge>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
        </Card>
      </div>

      <Dialog
        open={adding}
        onOpenChange={(open) => {
          if (!open) setAdding(false);
        }}
        title={t('plans.createTitle')}
      >
        <form onSubmit={form.handleSubmit(submitPlan)} className="grid gap-4" noValidate>
          <FormField
            label={t('plans.fields.code')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.code)}
          >
            <Input {...form.register('code')} autoComplete="off" className="font-mono" />
          </FormField>
          <FormField
            label={t('plans.fields.name')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.name)}
          >
            <Input {...form.register('name')} />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setAdding(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={createPlan.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}
