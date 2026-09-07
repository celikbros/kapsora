import type { CreateRoomType } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
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
  useToast,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { useDefinitionOptions } from '../catalogOptions';
import { problemOf } from '../problems';
import { useCreateRoomType, useProperty, useRoomTypes } from './queries';

/** One property: its facts and its room types, each priced as one night service. */
export function PropertyDetailPage() {
  const { propertyId } = useParams({ from: '/app/lodging/properties/$propertyId' });
  const { t } = useTranslation();
  const canManage = usePermission('accommodation.inventory.manage');
  const property = useProperty(propertyId);
  const rooms = useRoomTypes(propertyId);
  const definitions = useDefinitionOptions();
  const definitionName = new Map((definitions.data ?? []).map((d) => [d.value, d.label] as const));
  const [creating, setCreating] = useState(false);

  if (property.isPending) return <Spinner />;
  if (property.isError) return <ProblemAlert problem={problemOf(property.error)} />;
  const row = property.data.data;

  return (
    <div className="grid gap-4">
      <Breadcrumb
        items={[
          {
            label: t('lodging.office.properties.title'),
            render: (text) => <Link to="/lodging/properties">{text}</Link>,
          },
          { label: row.name },
        ]}
      />
      <PageHeader
        title={row.name}
        actions={
          canManage ? (
            <Button size="sm" onClick={() => setCreating((v) => !v)}>
              {t('lodging.office.properties.newRoomType')}
            </Button>
          ) : undefined
        }
      />
      <Card>
        <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-2 text-sm">
          <dt className="text-fg-muted">{t('lodging.office.properties.fields.code')}</dt>
          <dd className="font-mono">{row.code}</dd>
          <dt className="text-fg-muted">{t('lodging.office.properties.fields.type')}</dt>
          <dd>{t(`lodging.office.properties.types.${row.propertyType}`)}</dd>
          <dt className="text-fg-muted">{t('lodging.office.properties.fields.timezone')}</dt>
          <dd>{row.timezone}</dd>
          <dt className="text-fg-muted">{t('lodging.office.properties.fields.city')}</dt>
          <dd>{row.city ?? '—'}</dd>
          <dt className="text-fg-muted">{t('lodging.office.properties.columns.status')}</dt>
          <dd>
            <Badge tone={row.status === 'ACTIVE' ? 'success' : 'neutral'}>
              {t(`lodging.office.properties.status.${row.status}`)}
            </Badge>
          </dd>
        </dl>
      </Card>

      {creating ? <RoomTypeForm propertyId={propertyId} onDone={() => setCreating(false)} /> : null}

      <Card className="min-w-0">
        <h2 className="text-base font-semibold">{t('lodging.office.properties.roomTypes')}</h2>
        {rooms.isPending ? (
          <Spinner />
        ) : rooms.isError ? (
          <ProblemAlert problem={problemOf(rooms.error)} />
        ) : rooms.data.length === 0 ? (
          <p className="text-fg-muted mt-2 text-sm">
            {t('lodging.office.properties.roomTypesEmpty')}
          </p>
        ) : (
          <div className="relative mt-3 overflow-x-auto">
            <Table data-testid="room-type-table">
              <THead>
                <TR>
                  <TH>{t('lodging.office.properties.roomColumns.code')}</TH>
                  <TH>{t('lodging.office.properties.roomColumns.name')}</TH>
                  <TH>{t('lodging.office.properties.roomColumns.occupancy')}</TH>
                  <TH>{t('lodging.office.properties.roomColumns.service')}</TH>
                  <TH>{t('lodging.office.properties.roomColumns.status')}</TH>
                </TR>
              </THead>
              <TBody>
                {rooms.data.map((r) => (
                  <TR key={r.id} data-testid="room-type-row">
                    <TD className="font-mono text-xs">{r.code}</TD>
                    <TD>{r.name}</TD>
                    <TD>
                      {r.maxAdults} + {r.maxChildren} · {r.maxOccupancy}
                    </TD>
                    <TD>{definitionName.get(r.serviceDefinitionId) ?? '…'}</TD>
                    <TD>
                      <Badge tone={r.status === 'ACTIVE' ? 'success' : 'neutral'}>
                        {t(`lodging.office.properties.status.${r.status}`)}
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
  );
}

function RoomTypeForm({ propertyId, onDone }: { propertyId: string; onDone: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const definitions = useDefinitionOptions();
  const create = useCreateRoomType(propertyId);
  const [form, setForm] = useState({
    code: '',
    name: '',
    maxAdults: '2',
    maxChildren: '0',
    maxOccupancy: '2',
    serviceDefinitionId: '',
  });
  const set = (key: keyof typeof form) => (value: string) =>
    setForm((f) => ({ ...f, [key]: value }));

  function submit(e: FormEvent) {
    e.preventDefault();
    const body: CreateRoomType = {
      code: form.code.trim(),
      name: form.name.trim(),
      maxAdults: Number(form.maxAdults),
      maxChildren: Number(form.maxChildren),
      maxOccupancy: Number(form.maxOccupancy),
      serviceDefinitionId: form.serviceDefinitionId,
    };
    create.mutate(body, {
      onSuccess: () => {
        toast.notify({ tone: 'success', title: t('lodging.office.properties.roomTypeCreated') });
        onDone();
      },
    });
  }

  return (
    <Card>
      <form
        onSubmit={submit}
        className="grid gap-3 md:grid-cols-3"
        noValidate
        data-testid="room-type-form"
      >
        <FormField
          label={t('lodging.office.properties.roomFields.code')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          <Input
            name="code"
            value={form.code}
            onChange={(e) => set('code')(e.target.value)}
            className="font-mono"
            required
          />
        </FormField>
        <FormField
          label={t('lodging.office.properties.roomFields.name')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          <Input
            name="name"
            value={form.name}
            onChange={(e) => set('name')(e.target.value)}
            required
          />
        </FormField>
        <FormField
          label={t('lodging.office.properties.roomFields.service')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          <Select
            name="serviceDefinitionId"
            value={form.serviceDefinitionId}
            onChange={(e) => set('serviceDefinitionId')(e.target.value)}
            options={definitions.data ?? []}
            placeholder={t('common.none')}
          />
        </FormField>
        <FormField label={t('lodging.office.properties.roomFields.maxAdults')}>
          <Input
            type="number"
            inputMode="numeric"
            min={1}
            name="maxAdults"
            value={form.maxAdults}
            onChange={(e) => set('maxAdults')(e.target.value)}
          />
        </FormField>
        <FormField label={t('lodging.office.properties.roomFields.maxChildren')}>
          <Input
            type="number"
            inputMode="numeric"
            min={0}
            name="maxChildren"
            value={form.maxChildren}
            onChange={(e) => set('maxChildren')(e.target.value)}
          />
        </FormField>
        <FormField label={t('lodging.office.properties.roomFields.maxOccupancy')}>
          <Input
            type="number"
            inputMode="numeric"
            min={1}
            name="maxOccupancy"
            value={form.maxOccupancy}
            onChange={(e) => set('maxOccupancy')(e.target.value)}
          />
        </FormField>
        <div className="md:col-span-3">
          <ProblemAlert
            problem={create.isError ? problemOf(create.error) : null}
            className="mb-3"
          />
          <div className="flex gap-2">
            <Button
              type="submit"
              loading={create.isPending}
              disabled={
                form.code.trim() === '' || form.name.trim() === '' || !form.serviceDefinitionId
              }
            >
              {t('lodging.office.properties.create')}
            </Button>
            <Button type="button" variant="secondary" onClick={onDone}>
              {t('common.cancel')}
            </Button>
          </div>
        </div>
      </form>
    </Card>
  );
}
