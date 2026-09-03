import { ApiError, randomId, type Enrollment } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Card,
  Dialog,
  EmptyState,
  FormField,
  Input,
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
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import {
  useCreateEnrollment,
  usePersonEnrollments,
  usePlans,
  usePrograms,
  useUpdateEnrollment,
} from '../benefit/queries';
import { problemOf } from '../problems';
import { useMemberships } from './queries';

const STATUSES = ['ACTIVE', 'SUSPENDED', 'ENDED'] as const;

/** Which plans this person is enrolled in, through which membership and for how long. */
export function EnrollmentsTab({ personId }: { personId: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const canManage = usePermission('enrollment.manage');
  const enrollments = usePersonEnrollments(personId);
  const memberships = useMemberships(personId);
  const programs = usePrograms({ status: 'ACTIVE', limit: 200 });
  const [programId, setProgramId] = useState('');
  const plans = usePlans(programId);
  const create = useCreateEnrollment(personId);
  const update = useUpdateEnrollment(personId);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Enrollment | null>(null);
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);

  const form = useForm<{
    sponsorMembershipId: string;
    planId: string;
    validFrom: string;
    validTo: string;
    enrollmentReason: string;
  }>({
    defaultValues: {
      sponsorMembershipId: '',
      planId: '',
      validFrom: new Date().toISOString().slice(0, 10),
      validTo: '',
      enrollmentReason: '',
    },
  });
  const editForm = useForm<{ status: string; validTo: string }>({
    defaultValues: { status: 'ACTIVE', validTo: '' },
  });

  async function submit(values: {
    sponsorMembershipId: string;
    planId: string;
    validFrom: string;
    validTo: string;
    enrollmentReason: string;
  }) {
    setProblem(null);
    try {
      await create.mutateAsync({
        body: {
          sponsorMembershipId: values.sponsorMembershipId,
          planId: values.planId,
          validFrom: values.validFrom,
          ...(values.validTo ? { validTo: values.validTo } : {}),
          ...(values.enrollmentReason ? { enrollmentReason: values.enrollmentReason } : {}),
        },
        idempotencyKey: randomId(),
      });
      toast.notify({ tone: 'success', title: t('enrollments.created') });
      form.reset();
      setAdding(false);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitEdit(values: { status: string; validTo: string }) {
    if (!editing) return;
    setProblem(null);
    try {
      await update.mutateAsync({
        enrollmentId: editing.id,
        etag: `"${editing.rowVersion}"`,
        patch: {
          status: values.status as 'ACTIVE' | 'SUSPENDED' | 'ENDED',
          validTo: values.validTo === '' ? null : values.validTo,
        },
      });
      toast.notify({ tone: 'success', title: t('enrollments.updated') });
      setEditing(null);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  const rows = enrollments.data ?? [];
  const planNotPublished = problem?.code === 'PLAN_NOT_PUBLISHED';

  return (
    <Card>
      <div className="flex items-start justify-between gap-3">
        <h2 className="text-base font-semibold">{t('enrollments.title')}</h2>
        {canManage ? (
          <Button
            size="sm"
            onClick={() => setAdding(true)}
            disabled={(memberships.data ?? []).length === 0}
          >
            {t('enrollments.add')}
          </Button>
        ) : null}
      </div>

      <ProblemAlert
        problem={
          problem ?? (enrollments.error instanceof ApiError ? enrollments.error.problem : null)
        }
        className="mt-4"
      />
      {planNotPublished ? (
        <p className="text-fg-muted mt-2 text-sm">{t('enrollments.planNotPublished')}</p>
      ) : null}

      {enrollments.isPending ? (
        <div className="text-fg-muted mt-4 flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <div className="mt-4">
          <EmptyState title={t('enrollments.empty')} />
        </div>
      ) : (
        <div className="mt-4">
          <Table data-testid="enrollment-table">
            <THead>
              <TR>
                <TH>{t('enrollments.columns.plan')}</TH>
                <TH>{t('enrollments.columns.period')}</TH>
                <TH>{t('enrollments.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((enrollment) => (
                <TR key={enrollment.id}>
                  <TD>
                    <code className="font-mono text-xs">{enrollment.planCode}</code>
                  </TD>
                  <TD>
                    {formatDate(enrollment.validFrom)}
                    {enrollment.validTo ? ` – ${formatDate(enrollment.validTo)}` : ''}
                  </TD>
                  <TD>
                    <div className="flex items-center gap-2">
                      <Badge tone={statusTone(enrollment.status)}>
                        {t(`enrollments.statuses.${enrollment.status}`)}
                      </Badge>
                      {canManage ? (
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => {
                            editForm.reset({
                              status:
                                enrollment.status === 'PENDING' ? 'ACTIVE' : enrollment.status,
                              validTo: enrollment.validTo ?? '',
                            });
                            setEditing(enrollment);
                          }}
                        >
                          {t('organizations.edit')}
                        </Button>
                      ) : null}
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}

      <Dialog
        open={adding}
        onOpenChange={(open) => {
          if (!open) setAdding(false);
        }}
        title={t('enrollments.addTitle')}
      >
        <form onSubmit={form.handleSubmit(submit)} className="grid gap-4" noValidate>
          <FormField
            label={t('enrollments.fields.membership')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Select
              {...form.register('sponsorMembershipId')}
              placeholder={t('common.none')}
              options={(memberships.data ?? [])
                .filter((membership) => membership.status === 'ACTIVE')
                .map((membership) => ({
                  value: membership.id,
                  label: `${membership.sponsorDisplayName} · ${membership.membershipType}`,
                }))}
              required
            />
          </FormField>
          <FormField label={t('programs.title')}>
            <Select
              name="programId"
              value={programId}
              onChange={(e) => {
                setProgramId(e.target.value);
                form.setValue('planId', '');
              }}
              placeholder={t('common.none')}
              options={(programs.data?.items ?? []).map((program) => ({
                value: program.id,
                label: `${program.code} · ${program.name}`,
              }))}
            />
          </FormField>
          <FormField
            label={t('enrollments.fields.plan')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Select
              {...form.register('planId')}
              placeholder={t('common.none')}
              options={(plans.data ?? [])
                .filter((plan) => plan.status === 'ACTIVE')
                .map((plan) => ({ value: plan.id, label: `${plan.code} · ${plan.name}` }))}
              disabled={programId === ''}
              required
            />
          </FormField>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('enrollments.fields.validFrom')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input {...form.register('validFrom')} type="date" required />
            </FormField>
            <FormField label={t('enrollments.fields.validTo')}>
              <Input {...form.register('validTo')} type="date" />
            </FormField>
          </div>
          <FormField label={t('enrollments.fields.reason')}>
            <Input {...form.register('enrollmentReason')} />
          </FormField>
          <ProblemAlert problem={problem} hideFieldErrors />
          <div className="flex justify-end gap-2">
            <Button
              variant="secondary"
              onClick={() => setAdding(false)}
              disabled={create.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={create.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog
        open={editing !== null}
        onOpenChange={(open) => {
          if (!open) setEditing(null);
        }}
        title={t('enrollments.editTitle')}
      >
        <form onSubmit={editForm.handleSubmit(submitEdit)} className="grid gap-4" noValidate>
          <FormField label={t('enrollments.fields.status')}>
            <Select
              {...editForm.register('status')}
              options={STATUSES.map((status) => ({
                value: status,
                label: t(`enrollments.statuses.${status}`),
              }))}
            />
          </FormField>
          <FormField label={t('enrollments.fields.validTo')}>
            <Input {...editForm.register('validTo')} type="date" />
          </FormField>
          <ProblemAlert problem={problem} hideFieldErrors />
          <div className="flex justify-end gap-2">
            <Button
              variant="secondary"
              onClick={() => setEditing(null)}
              disabled={update.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={update.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Dialog>
    </Card>
  );
}
