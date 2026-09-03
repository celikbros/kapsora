import { ApiError, randomId, type SponsorMembership } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, useTranslation } from '@kapsora/i18n';
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
import { zodResolver } from '@hookform/resolvers/zod';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { useOrganizationList } from '../organizations/queries';
import { problemOf } from '../problems';
import {
  useCreateMembership,
  useMemberships,
  usePartyCatalogs,
  useUpdateMembership,
} from './queries';
import { membershipFormSchema, parseIssueMessage, type MembershipFormValues } from './schema';

const STATUSES = ['ACTIVE', 'SUSPENDED', 'ENDED'] as const;

/** Sponsor memberships: which sponsor covers this person, under which type and number. */
export function MembershipsTab({ personId }: { personId: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const canManage = usePermission('membership.manage');
  const memberships = useMemberships(personId);
  const catalogs = usePartyCatalogs();
  const sponsors = useOrganizationList({ role: 'SPONSOR', limit: 200 });
  const create = useCreateMembership(personId);
  const update = useUpdateMembership(personId);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<SponsorMembership | null>(null);
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);

  const form = useForm<MembershipFormValues>({
    resolver: zodResolver(membershipFormSchema),
    defaultValues: {
      sponsorOrganizationId: '',
      membershipType: '',
      principalMembershipId: '',
      externalMemberNo: '',
      validFrom: '',
      validTo: '',
    },
    mode: 'onBlur',
  });
  const editForm = useForm<{ status: string; validTo: string; externalMemberNo: string }>({
    defaultValues: { status: 'ACTIVE', validTo: '', externalMemberNo: '' },
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  const requiresPrincipal = (code: string): boolean =>
    (catalogs.data?.membershipTypes ?? []).some(
      (entry) => entry.code === code && entry.requiresPrincipal === true,
    );

  async function submit(values: MembershipFormValues) {
    setProblem(null);
    if (requiresPrincipal(values.membershipType) && values.principalMembershipId === '') {
      form.setError('principalMembershipId', { type: 'manual', message: 'PRINCIPAL_REQUIRED' });
      return;
    }
    try {
      await create.mutateAsync({
        body: {
          sponsorOrganizationId: values.sponsorOrganizationId,
          membershipType: values.membershipType,
          validFrom: values.validFrom,
          ...(values.validTo ? { validTo: values.validTo } : {}),
          ...(values.externalMemberNo ? { externalMemberNo: values.externalMemberNo } : {}),
          ...(values.principalMembershipId
            ? { principalMembershipId: values.principalMembershipId }
            : {}),
        },
        idempotencyKey: randomId(),
      });
      toast.notify({ tone: 'success', title: t('memberships.created') });
      form.reset();
      setAdding(false);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitEdit(values: { status: string; validTo: string; externalMemberNo: string }) {
    if (!editing) return;
    setProblem(null);
    try {
      await update.mutateAsync({
        membershipId: editing.id,
        etag: `"${editing.rowVersion}"`,
        patch: {
          status: values.status as 'ACTIVE' | 'SUSPENDED' | 'ENDED',
          validTo: values.validTo === '' ? null : values.validTo,
          externalMemberNo: values.externalMemberNo === '' ? null : values.externalMemberNo,
        },
      });
      toast.notify({ tone: 'success', title: t('memberships.updated') });
      setEditing(null);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  const rows = memberships.data ?? [];

  return (
    <Card>
      <div className="flex items-start justify-between gap-3">
        <h2 className="text-base font-semibold">{t('memberships.title')}</h2>
        {canManage ? (
          <Button size="sm" onClick={() => setAdding(true)}>
            {t('memberships.add')}
          </Button>
        ) : null}
      </div>

      <ProblemAlert
        problem={
          problem ?? (memberships.error instanceof ApiError ? memberships.error.problem : null)
        }
        className="mt-4"
      />

      {memberships.isPending ? (
        <div className="text-fg-muted mt-4 flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <div className="mt-4">
          <EmptyState title={t('memberships.empty')} />
        </div>
      ) : (
        <div className="mt-4">
          <Table data-testid="membership-table">
            <THead>
              <TR>
                <TH>{t('memberships.columns.sponsor')}</TH>
                <TH>{t('memberships.columns.type')}</TH>
                <TH>{t('memberships.columns.memberNo')}</TH>
                <TH>{t('memberships.columns.period')}</TH>
                <TH>{t('memberships.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((membership) => (
                <TR key={membership.id}>
                  <TD>{membership.sponsorDisplayName}</TD>
                  <TD>{membership.membershipType}</TD>
                  <TD>
                    <code className="font-mono text-xs">
                      {membership.externalMemberNo ?? t('common.none')}
                    </code>
                  </TD>
                  <TD>
                    {formatDate(membership.validFrom)}
                    {membership.validTo ? ` – ${formatDate(membership.validTo)}` : ''}
                  </TD>
                  <TD>
                    <div className="flex items-center gap-2">
                      <Badge tone={statusTone(membership.status)}>
                        {t(`memberships.statuses.${membership.status}`)}
                      </Badge>
                      {canManage ? (
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => {
                            editForm.reset({
                              status:
                                membership.status === 'PENDING' ? 'ACTIVE' : membership.status,
                              validTo: membership.validTo ?? '',
                              externalMemberNo: membership.externalMemberNo ?? '',
                            });
                            setEditing(membership);
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
        title={t('memberships.addTitle')}
      >
        <form onSubmit={form.handleSubmit(submit)} className="grid gap-4" noValidate>
          <FormField
            label={t('memberships.fields.sponsor')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.sponsorOrganizationId)}
          >
            <Select
              {...form.register('sponsorOrganizationId')}
              placeholder={t('common.none')}
              options={(sponsors.data?.items ?? []).map((organization) => ({
                value: organization.id,
                label: organization.displayName,
              }))}
            />
          </FormField>
          <FormField
            label={t('memberships.fields.membershipType')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.membershipType)}
          >
            <Select
              {...form.register('membershipType')}
              placeholder={t('common.none')}
              options={(catalogs.data?.membershipTypes ?? []).map((entry) => ({
                value: entry.code,
                label: entry.requiresPrincipal
                  ? `${entry.displayName} (${t('memberships.fields.principal')})`
                  : entry.displayName,
              }))}
            />
          </FormField>
          <FormField
            label={t('memberships.fields.principal')}
            hint={t('memberships.principalRequired')}
            error={message(form.formState.errors.principalMembershipId)}
          >
            <Select
              {...form.register('principalMembershipId')}
              placeholder={t('common.none')}
              options={rows
                .filter((membership) => membership.principalMembershipId === null)
                .map((membership) => ({
                  value: membership.id,
                  label: `${membership.sponsorDisplayName} · ${membership.membershipType}`,
                }))}
            />
          </FormField>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('memberships.fields.externalMemberNo')}
              error={message(form.formState.errors.externalMemberNo)}
            >
              <Input {...form.register('externalMemberNo')} autoComplete="off" />
            </FormField>
            <FormField
              label={t('memberships.fields.validFrom')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.validFrom)}
            >
              <Input {...form.register('validFrom')} type="date" />
            </FormField>
          </div>
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
        title={t('memberships.editTitle')}
      >
        <form onSubmit={editForm.handleSubmit(submitEdit)} className="grid gap-4" noValidate>
          <FormField label={t('memberships.fields.status')}>
            <Select
              {...editForm.register('status')}
              options={STATUSES.map((status) => ({
                value: status,
                label: t(`memberships.statuses.${status}`),
              }))}
            />
          </FormField>
          <FormField label={t('memberships.fields.validTo')}>
            <Input {...editForm.register('validTo')} type="date" />
          </FormField>
          <FormField label={t('memberships.fields.externalMemberNo')}>
            <Input {...editForm.register('externalMemberNo')} autoComplete="off" />
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
