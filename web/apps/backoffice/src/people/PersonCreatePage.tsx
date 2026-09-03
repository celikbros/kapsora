import { ApiError, randomId, type CreatePersonRequest } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
import { Breadcrumb, Card, PageHeader, useToast } from '@kapsora/ui';
import { Link, useNavigate } from '@tanstack/react-router';
import { useRef, useState } from 'react';

import { problemOf } from '../problems';
import { PersonForm } from './PersonForm';
import { useCreatePerson, usePartyCatalogs } from './queries';
import type { PersonFormValues } from './schema';

/** New member form. */
export function PersonCreatePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const catalogs = usePartyCatalogs();
  const create = useCreatePerson();
  // One key per form instance: a retried submit replays instead of creating a twin.
  const idempotencyKey = useRef(randomId());
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);

  async function submit(values: PersonFormValues) {
    setProblem(null);
    const body: CreatePersonRequest = {
      firstName: values.firstName,
      lastName: values.lastName,
      identifiers: values.identifiers.map((identifier) => ({
        type: identifier.type,
        value: identifier.value,
        primary: identifier.primary,
      })),
    };
    if (values.middleName) body.middleName = values.middleName;
    if (values.birthDate) body.birthDate = values.birthDate;
    if (values.sexAtBirth) body.sexAtBirth = values.sexAtBirth;

    try {
      const result = await create.mutateAsync({ body, idempotencyKey: idempotencyKey.current });
      toast.notify({ tone: 'success', title: t('people.created') });
      await navigate({ to: '/people/$personId', params: { personId: result.data.id } });
    } catch (err) {
      setProblem(problemOf(err));
      if (err instanceof ApiError && err.status === 422) {
        // Nothing was written, so the next attempt starts a fresh command.
        idempotencyKey.current = randomId();
      }
    }
  }

  return (
    <>
      <PageHeader
        title={t('people.createTitle')}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('people.title'),
                render: (label) => (
                  <Link to="/people" search={{}}>
                    {label}
                  </Link>
                ),
              },
              { label: t('people.createTitle') },
            ]}
          />
        }
      />
      <Card>
        <PersonForm
          onSubmit={submit}
          onCancel={() => void navigate({ to: '/people', search: {} })}
          busy={create.isPending}
          problem={problem}
          {...(catalogs.data ? { identifierTypes: catalogs.data.identifierTypes } : {})}
        />
      </Card>
    </>
  );
}
