import { ApiError, randomId } from '@kapsora/api-client';
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
import { Link } from '@tanstack/react-router';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { problemOf } from '../problems';
import {
  useCreateRelationship,
  useEndRelationship,
  usePartyCatalogs,
  usePersonList,
  useRelationships,
} from './queries';
import { parseIssueMessage, relationshipFormSchema, type RelationshipFormValues } from './schema';

/** Family and other person-to-person links, with their validity periods. */
export function FamilyTab({ personId }: { personId: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const canManage = usePermission('member.relationship.manage');
  const relationships = useRelationships(personId);
  const catalogs = usePartyCatalogs();
  const create = useCreateRelationship(personId);
  const end = useEndRelationship(personId);
  const [adding, setAdding] = useState(false);
  const [ending, setEnding] = useState<{ id: string; etag: string } | null>(null);
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);
  // The picker offers the tenant's people; a big tenant would search instead.
  const candidates = usePersonList({ limit: 50 });

  const form = useForm<RelationshipFormValues>({
    resolver: zodResolver(relationshipFormSchema),
    defaultValues: { targetPersonId: '', relationshipType: '', validFrom: '', validTo: '' },
    mode: 'onBlur',
  });
  const endForm = useForm<{ endsOn: string; reasonCode: string }>({
    defaultValues: { endsOn: new Date().toISOString().slice(0, 10), reasonCode: 'ENDED' },
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function submit(values: RelationshipFormValues) {
    setProblem(null);
    try {
      await create.mutateAsync({
        body: {
          targetPersonId: values.targetPersonId,
          relationshipType: values.relationshipType,
          validFrom: values.validFrom,
          ...(values.validTo ? { validTo: values.validTo } : {}),
        },
        idempotencyKey: randomId(),
      });
      toast.notify({ tone: 'success', title: t('family.created') });
      form.reset();
      setAdding(false);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitEnd(values: { endsOn: string; reasonCode: string }) {
    if (!ending) return;
    setProblem(null);
    try {
      await end.mutateAsync({
        relationshipId: ending.id,
        etag: ending.etag,
        body: { endsOn: values.endsOn, reasonCode: values.reasonCode },
      });
      toast.notify({ tone: 'success', title: t('family.ended') });
      setEnding(null);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  const rows = relationships.data ?? [];

  return (
    <Card>
      <div className="flex items-start justify-between gap-3">
        <h2 className="text-base font-semibold">{t('family.title')}</h2>
        {canManage ? (
          <Button size="sm" onClick={() => setAdding(true)}>
            {t('family.add')}
          </Button>
        ) : null}
      </div>

      <ProblemAlert
        problem={
          problem ?? (relationships.error instanceof ApiError ? relationships.error.problem : null)
        }
        className="mt-4"
      />

      {relationships.isPending ? (
        <div className="text-fg-muted mt-4 flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <div className="mt-4">
          <EmptyState title={t('family.empty')} />
        </div>
      ) : (
        <div className="mt-4">
          <Table data-testid="relationship-table">
            <THead>
              <TR>
                <TH>{t('family.columns.person')}</TH>
                <TH>{t('family.columns.type')}</TH>
                <TH>{t('family.columns.direction')}</TH>
                <TH>{t('family.columns.period')}</TH>
                <TH>{t('common.actions')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((relationship) => (
                <TR key={relationship.id}>
                  <TD>
                    <Link
                      to="/people/$personId"
                      params={{ personId: relationship.otherPerson.id }}
                      className="font-medium underline-offset-2 hover:underline"
                    >
                      {relationship.otherPerson.displayName}
                    </Link>
                  </TD>
                  <TD>{relationship.relationshipType}</TD>
                  <TD>{t(`family.directions.${relationship.direction}`)}</TD>
                  <TD>
                    {formatDate(relationship.validFrom)}
                    {relationship.validTo ? ` – ${formatDate(relationship.validTo)}` : ''}
                  </TD>
                  <TD>
                    <div className="flex items-center gap-2">
                      <Badge tone={statusTone(relationship.status)}>{relationship.status}</Badge>
                      {canManage && relationship.status === 'ACTIVE' ? (
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() =>
                            setEnding({
                              id: relationship.id,
                              etag: `"${relationship.rowVersion}"`,
                            })
                          }
                        >
                          {t('family.end')}
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
        title={t('family.addTitle')}
      >
        <form onSubmit={form.handleSubmit(submit)} className="grid gap-4" noValidate>
          <FormField
            label={t('family.fields.targetPerson')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.targetPersonId)}
          >
            <Select
              {...form.register('targetPersonId')}
              placeholder={t('common.none')}
              options={(candidates.data?.items ?? [])
                .filter((candidate) => candidate.id !== personId)
                .map((candidate) => ({ value: candidate.id, label: candidate.displayName }))}
            />
          </FormField>
          <FormField
            label={t('family.fields.relationshipType')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.relationshipType)}
          >
            <Select
              {...form.register('relationshipType')}
              placeholder={t('common.none')}
              options={(catalogs.data?.relationshipTypes ?? []).map((entry) => ({
                value: entry.code,
                label: entry.displayName,
              }))}
            />
          </FormField>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('family.fields.validFrom')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.validFrom)}
            >
              <Input {...form.register('validFrom')} type="date" />
            </FormField>
            <FormField label={t('family.fields.validTo')}>
              <Input {...form.register('validTo')} type="date" />
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
        open={ending !== null}
        onOpenChange={(open) => {
          if (!open) setEnding(null);
        }}
        title={t('family.endTitle')}
      >
        <form onSubmit={endForm.handleSubmit(submitEnd)} className="grid gap-4" noValidate>
          <FormField
            label={t('family.fields.endsOn')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input {...endForm.register('endsOn')} type="date" required />
          </FormField>
          <FormField
            label={t('family.fields.reasonCode')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input {...endForm.register('reasonCode')} required />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setEnding(null)} disabled={end.isPending}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={end.isPending}>
              {t('family.end')}
            </Button>
          </div>
        </form>
      </Dialog>
    </Card>
  );
}
