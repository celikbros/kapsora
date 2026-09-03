import type { ApiError, Person, UpdatePersonRequest } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, useTranslation } from '@kapsora/i18n';
import { Badge, Button, Card, FormField, Input, ProblemAlert, Select, useToast } from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useEffect, useState } from 'react';
import { useForm } from 'react-hook-form';

import { problemOf } from '../problems';
import { useUpdatePerson } from './queries';
import {
  PERSON_STATUSES,
  SEXES,
  parseIssueMessage,
  personEditSchema,
  type PersonEditValues,
} from './schema';

export interface IdentityTabProps {
  person: Person;
  etag: string;
}

/** Names, dates and the masked identifiers, editable under If-Match. */
export function IdentityTab({ person, etag }: IdentityTabProps) {
  const { t } = useTranslation();
  const toast = useToast();
  const canManage = usePermission('member.manage');
  const update = useUpdatePerson(person.id);
  const [editing, setEditing] = useState(false);
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);

  const form = useForm<PersonEditValues>({
    resolver: zodResolver(personEditSchema),
    defaultValues: {
      firstName: person.firstName,
      middleName: person.middleName ?? '',
      lastName: person.lastName,
      birthDate: person.birthDate ?? '',
      sexAtBirth: person.sexAtBirth ?? '',
      status: person.status === 'MERGED' ? 'ACTIVE' : person.status,
    },
    mode: 'onBlur',
  });

  useEffect(() => {
    if (!editing) {
      form.reset({
        firstName: person.firstName,
        middleName: person.middleName ?? '',
        lastName: person.lastName,
        birthDate: person.birthDate ?? '',
        sexAtBirth: person.sexAtBirth ?? '',
        status: person.status === 'MERGED' ? 'ACTIVE' : person.status,
      });
    }
  }, [editing, person, form]);

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function submit(values: PersonEditValues) {
    setProblem(null);
    const patch: UpdatePersonRequest = {};
    if (values.firstName !== person.firstName) patch.firstName = values.firstName;
    if (values.lastName !== person.lastName) patch.lastName = values.lastName;
    const middle = values.middleName === '' ? null : values.middleName;
    if (middle !== (person.middleName ?? null)) patch.middleName = middle;
    const birth = values.birthDate === '' ? null : values.birthDate;
    if (birth !== (person.birthDate ?? null)) patch.birthDate = birth;
    const sex = values.sexAtBirth === '' ? null : values.sexAtBirth;
    if (sex !== (person.sexAtBirth ?? null)) patch.sexAtBirth = sex;
    if (values.status !== person.status) patch.status = values.status;
    if (Object.keys(patch).length === 0) {
      setEditing(false);
      return;
    }
    try {
      await update.mutateAsync({ etag, patch });
      toast.notify({ tone: 'success', title: t('people.updated') });
      setEditing(false);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  const row = (label: string, value: string) => (
    <>
      <dt className="text-fg-muted">{label}</dt>
      <dd>{value === '' ? t('common.none') : value}</dd>
    </>
  );

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <div className="flex items-start justify-between gap-3">
          <h2 className="text-base font-semibold">{t('people.detailTitle')}</h2>
          {canManage && !editing ? (
            <Button size="sm" variant="secondary" onClick={() => setEditing(true)}>
              {t('organizations.edit')}
            </Button>
          ) : null}
        </div>

        {editing ? (
          <form onSubmit={form.handleSubmit(submit)} className="mt-4 grid gap-4" noValidate>
            <ProblemAlert problem={problem} hideFieldErrors />
            <div className="grid gap-4 md:grid-cols-2">
              <FormField
                label={t('people.fields.firstName')}
                required
                requiredLabel={t('common.requiredMark')}
                error={message(form.formState.errors.firstName)}
              >
                <Input {...form.register('firstName')} />
              </FormField>
              <FormField
                label={t('people.fields.middleName')}
                error={message(form.formState.errors.middleName)}
              >
                <Input {...form.register('middleName')} />
              </FormField>
              <FormField
                label={t('people.fields.lastName')}
                required
                requiredLabel={t('common.requiredMark')}
                error={message(form.formState.errors.lastName)}
              >
                <Input {...form.register('lastName')} />
              </FormField>
              <FormField
                label={t('people.fields.birthDate')}
                error={message(form.formState.errors.birthDate)}
              >
                <Input {...form.register('birthDate')} type="date" />
              </FormField>
              <FormField label={t('people.fields.sexAtBirth')}>
                <Select
                  {...form.register('sexAtBirth')}
                  placeholder={t('common.none')}
                  options={SEXES.map((sex) => ({ value: sex, label: t(`people.sex.${sex}`) }))}
                />
              </FormField>
              <FormField label={t('people.fields.status')}>
                <Select
                  {...form.register('status')}
                  options={PERSON_STATUSES.map((status) => ({
                    value: status,
                    label: t(`people.statuses.${status}`),
                  }))}
                />
              </FormField>
            </div>
            <div className="flex justify-end gap-2">
              <Button
                variant="secondary"
                onClick={() => {
                  setProblem(null);
                  setEditing(false);
                }}
                disabled={update.isPending}
              >
                {t('common.cancel')}
              </Button>
              <Button type="submit" loading={update.isPending}>
                {t('common.save')}
              </Button>
            </div>
          </form>
        ) : (
          <dl className="mt-3 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
            {row(t('people.fields.firstName'), person.firstName)}
            {row(t('people.fields.middleName'), person.middleName ?? '')}
            {row(t('people.fields.lastName'), person.lastName)}
            {row(t('people.fields.birthDate'), formatDate(person.birthDate))}
            {row(
              t('people.fields.sexAtBirth'),
              person.sexAtBirth ? t(`people.sex.${person.sexAtBirth}`) : '',
            )}
            <dt className="text-fg-muted">{t('people.fields.status')}</dt>
            <dd>
              <Badge tone={person.status === 'ACTIVE' ? 'success' : 'neutral'}>
                {t(`people.statuses.${person.status}`)}
              </Badge>
            </dd>
          </dl>
        )}
      </Card>

      <Card>
        <h2 className="text-base font-semibold">{t('people.fields.identifiers')}</h2>
        <p className="text-fg-muted mt-1 text-xs">{t('people.identifierHint')}</p>
        <ul className="mt-3 grid gap-2 text-sm">
          {(person.identifiers ?? []).map((identifier, index) => (
            <li key={`${identifier.type}-${index}`} className="flex items-center gap-2">
              <Badge>{identifier.type}</Badge>
              <code className="font-mono">{identifier.maskedValue}</code>
              {identifier.primary ? <Badge tone="info">{t('people.fields.primary')}</Badge> : null}
            </li>
          ))}
          {(person.identifiers ?? []).length === 0 ? (
            <li className="text-fg-muted">{t('common.none')}</li>
          ) : null}
        </ul>
      </Card>
    </div>
  );
}
