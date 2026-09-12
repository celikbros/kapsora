import type {
  ServiceRequestChannel,
  ServiceRequestType,
  ServiceUnitType,
} from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
import {
  Breadcrumb,
  Button,
  Card,
  FormField,
  HelpHint,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
} from '@kapsora/ui';
import { Link, useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { usePersonEnrollments } from '../benefit/queries';
import { useServiceDefinitionOptions } from '../catalog/names';
import { PersonPicker } from '../people/PersonPicker';
import { problemOf } from '../problems';
import { useCreateRequest } from './queries';

const TYPES: ServiceRequestType[] = [
  'DIRECT_SERVICE',
  'PREAUTHORIZATION',
  'RESERVATION',
  'REIMBURSEMENT',
];
const CHANNEL: ServiceRequestChannel = 'BACKOFFICE';

/**
 * A new request: the person by name, the enrollment the request draws on, the first
 * line. It is created as a draft and opens on the detail page, where the lines are
 * edited in place and the submit lives — one place for the whole story rather than a
 * wizard that ends where the work begins.
 */
export function RequestCreatePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const create = useCreateRequest();
  const definitions = useServiceDefinitionOptions();
  const [personQuery, setPersonQuery] = useState('');
  const [personId, setPersonId] = useState('');
  const [enrollmentId, setEnrollmentId] = useState('');
  const [requestType, setRequestType] = useState<ServiceRequestType>('DIRECT_SERVICE');
  const [serviceDate, setServiceDate] = useState(new Date().toISOString().slice(0, 10));
  const [definitionId, setDefinitionId] = useState('');
  const [unitType, setUnitType] = useState<ServiceUnitType>('SESSION');
  const [quantity, setQuantity] = useState('1');
  const [amount, setAmount] = useState('');

  const enrollments = usePersonEnrollments(personId);
  const enrollment = (enrollments.data ?? []).find((e) => e.id === enrollmentId);
  const ready = personId && enrollment?.programId && definitionId && quantity.trim() && serviceDate;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ready || !enrollment?.programId) return;
    const created = await create.mutateAsync({
      personId,
      enrollmentId,
      programId: enrollment.programId,
      requestType,
      channel: CHANNEL,
      serviceDate,
      items: [
        {
          serviceDefinitionId: definitionId,
          unitType,
          requestedQuantity: quantity.trim(),
          ...(amount.trim() ? { requestedAmount: amount.trim(), currencyCode: 'TRY' } : {}),
        },
      ],
    });
    await navigate({ to: '/requests/$requestId', params: { requestId: created.data.id } });
  }

  return (
    <>
      <PageHeader
        title={t('requests.new')}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('requests.title'),
                render: (label) => (
                  <Link to="/requests" search={{}}>
                    {label}
                  </Link>
                ),
              },
              { label: t('requests.new') },
            ]}
          />
        }
      />
      <Card>
        <form onSubmit={submit} className="grid gap-4" noValidate data-testid="request-create-form">
          <ProblemAlert problem={create.error ? problemOf(create.error) : null} />
          <div className="grid gap-4 md:grid-cols-2">
            <PersonPicker
              query={personQuery}
              onQueryChange={setPersonQuery}
              personId={personId}
              onPick={(id) => {
                setPersonId(id);
                setEnrollmentId('');
              }}
              label={t('requests.columns.person')}
            />
            <FormField
              label={t('enrollments.title')}
              required
              requiredLabel={t('common.requiredMark')}
              help={<HelpHint term="plan" />}
            >
              <Select
                name="enrollmentId"
                value={enrollmentId}
                onChange={(e) => setEnrollmentId(e.target.value)}
                placeholder={t('common.none')}
                disabled={!personId}
                options={(enrollments.data ?? [])
                  .filter((e) => e.status === 'ACTIVE')
                  .map((e) => ({ value: e.id, label: `${e.planCode} · ${e.validFrom}` }))}
              />
            </FormField>
            <FormField label={t('requests.columns.type')}>
              <Select
                name="requestType"
                value={requestType}
                onChange={(e) => setRequestType(e.target.value as ServiceRequestType)}
                options={TYPES.map((v) => ({ value: v, label: t(`requests.type.${v}`) }))}
              />
            </FormField>
            <FormField
              label={t('requests.columns.serviceDate')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="serviceDate"
                type="date"
                value={serviceDate}
                onChange={(e) => setServiceDate(e.target.value)}
              />
            </FormField>
            <FormField
              label={t('requests.items.service')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Select
                name="serviceDefinitionId"
                value={definitionId}
                onChange={(e) => {
                  setDefinitionId(e.target.value);
                  const picked = definitions.data?.find((d) => d.value === e.target.value);
                  if (picked) setUnitType(picked.unitType);
                }}
                placeholder={t('common.none')}
                options={(definitions.data ?? []).map((d) => ({ value: d.value, label: d.label }))}
              />
            </FormField>
            <div className="grid grid-cols-2 gap-4">
              <FormField
                label={t('requests.items.requestedQuantity')}
                required
                requiredLabel={t('common.requiredMark')}
                hint={unitType}
              >
                <Input
                  name="requestedQuantity"
                  value={quantity}
                  onChange={(e) => setQuantity(e.target.value)}
                  inputMode="decimal"
                  className="text-right font-mono"
                />
              </FormField>
              <FormField label={t('requests.items.requestedAmount')}>
                <Input
                  name="requestedAmount"
                  value={amount}
                  onChange={(e) => setAmount(e.target.value)}
                  inputMode="decimal"
                  className="text-right font-mono"
                />
              </FormField>
            </div>
          </div>
          <div className="flex justify-end gap-2">
            <Link
              to="/requests"
              search={{}}
              className="text-fg-muted inline-flex h-10 items-center px-3 text-sm"
            >
              {t('common.cancel')}
            </Link>
            <Button type="submit" loading={create.isPending} disabled={!ready}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Card>
    </>
  );
}
