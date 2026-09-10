import type { CreateProperty, PropertyType } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import {
  Badge,
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
  useMinWidth,
  useToast,
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { useOrganizationName } from '../claims/names';
import { problemOf } from '../problems';
import { LodgingNav } from './LodgingNav';
import { useCreateProperty, useProperties, useProviderOrganizations } from './queries';

const TYPES: PropertyType[] = ['HOTEL', 'RESORT', 'GUESTHOUSE', 'SOCIAL_FACILITY', 'OTHER'];

function ProviderCell({ organizationId }: { organizationId: string }) {
  const name = useOrganizationName(organizationId);
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}

/** Tesisler: every property the payer's providers opened, and the form that opens one. */
export function PropertiesPage() {
  const { t } = useTranslation();
  const canManage = usePermission('accommodation.inventory.manage');
  const canReadOrganizations = usePermission('organization.read');
  const wide = useMinWidth(768);
  const properties = useProperties();
  const [creating, setCreating] = useState(false);

  return (
    <div className="grid gap-4">
      <LodgingNav />
      <PageHeader
        title={t('lodging.office.properties.title')}
        actions={
          canManage ? (
            <Button size="sm" onClick={() => setCreating((v) => !v)}>
              {t('lodging.office.properties.new')}
            </Button>
          ) : undefined
        }
      />
      <p className="text-fg-muted text-sm">{t('lodging.office.properties.intro')}</p>
      {creating ? <PropertyForm onDone={() => setCreating(false)} /> : null}
      {properties.isPending ? (
        <Spinner />
      ) : properties.isError ? (
        <ProblemAlert problem={problemOf(properties.error)} />
      ) : properties.data.items.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('lodging.office.properties.empty')}</p>
      ) : !wide ? (
        <ul className="grid gap-2" data-testid="property-table">
          {properties.data.items.map((p) => (
            <li
              key={p.id}
              className="bg-surface-raised border-line rounded-lg border p-3"
              data-testid="property-row"
            >
              <div className="flex items-baseline justify-between gap-3">
                <Link
                  to="/lodging/properties/$propertyId"
                  params={{ propertyId: p.id }}
                  className="text-primary text-sm font-medium underline-offset-4 hover:underline"
                >
                  {p.name}
                </Link>
                <Badge tone={p.status === 'ACTIVE' ? 'success' : 'neutral'}>
                  {t(`lodging.office.properties.status.${p.status}`)}
                </Badge>
              </div>
              <p className="text-fg-muted mt-1 text-sm">
                <span className="font-mono text-xs">{p.code}</span> ·{' '}
                {t(`lodging.office.properties.types.${p.propertyType}`)}
                {p.city ? ` · ${p.city}` : ''}
              </p>
              {canReadOrganizations ? (
                <p className="text-fg-muted mt-1 text-sm">
                  <ProviderCell organizationId={p.providerOrganizationId} />
                </p>
              ) : null}
            </li>
          ))}
        </ul>
      ) : (
        <div className="relative overflow-x-auto">
          <Table data-testid="property-table">
            <THead>
              <TR>
                <TH>{t('lodging.office.properties.columns.name')}</TH>
                <TH>{t('lodging.office.properties.columns.code')}</TH>
                {canReadOrganizations ? (
                  <TH>{t('lodging.office.properties.columns.provider')}</TH>
                ) : null}
                <TH>{t('lodging.office.properties.columns.type')}</TH>
                <TH>{t('lodging.office.properties.columns.city')}</TH>
                <TH>{t('lodging.office.properties.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {properties.data.items.map((p) => (
                <TR key={p.id} data-testid="property-row">
                  <TD>
                    <Link
                      to="/lodging/properties/$propertyId"
                      params={{ propertyId: p.id }}
                      className="text-primary underline-offset-4 hover:underline"
                    >
                      {p.name}
                    </Link>
                  </TD>
                  <TD className="font-mono text-xs">{p.code}</TD>
                  {canReadOrganizations ? (
                    <TD>
                      <ProviderCell organizationId={p.providerOrganizationId} />
                    </TD>
                  ) : null}
                  <TD>{t(`lodging.office.properties.types.${p.propertyType}`)}</TD>
                  <TD>{p.city ?? '—'}</TD>
                  <TD>
                    <Badge tone={p.status === 'ACTIVE' ? 'success' : 'neutral'}>
                      {t(`lodging.office.properties.status.${p.status}`)}
                    </Badge>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
    </div>
  );
}

function PropertyForm({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const organizations = useProviderOrganizations();
  const create = useCreateProperty();
  const [form, setForm] = useState({
    providerOrganizationId: '',
    code: '',
    name: '',
    propertyType: 'HOTEL' as PropertyType,
    timezone: 'Europe/Istanbul',
    city: '',
    regionCode: '',
  });
  const set = (key: keyof typeof form) => (value: string) =>
    setForm((f) => ({ ...f, [key]: value }));

  function submit(e: FormEvent) {
    e.preventDefault();
    const body: CreateProperty = {
      providerOrganizationId: form.providerOrganizationId,
      code: form.code.trim(),
      name: form.name.trim(),
      propertyType: form.propertyType,
      timezone: form.timezone.trim(),
      city: form.city.trim() || null,
      regionCode: form.regionCode.trim() || null,
    };
    create.mutate(body, {
      onSuccess: () => {
        toast.notify({ tone: 'success', title: t('lodging.office.properties.created') });
        onDone();
      },
    });
  }

  return (
    <Card>
      <form
        onSubmit={submit}
        className="grid gap-3 md:grid-cols-2"
        noValidate
        data-testid="property-form"
      >
        <FormField
          label={t('lodging.office.properties.fields.provider')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          <Select
            name="providerOrganizationId"
            value={form.providerOrganizationId}
            onChange={(e) => set('providerOrganizationId')(e.target.value)}
            options={(organizations.data?.items ?? []).map((o) => ({
              value: o.id,
              label: o.displayName,
            }))}
            placeholder={t('common.none')}
          />
        </FormField>
        <FormField
          label={t('lodging.office.properties.fields.type')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          <Select
            name="propertyType"
            value={form.propertyType}
            onChange={(e) => set('propertyType')(e.target.value)}
            options={TYPES.map((type) => ({
              value: type,
              label: t(`lodging.office.properties.types.${type}`),
            }))}
          />
        </FormField>
        <FormField
          label={t('lodging.office.properties.fields.code')}
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
          label={t('lodging.office.properties.fields.name')}
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
          label={t('lodging.office.properties.fields.timezone')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          <Input
            name="timezone"
            value={form.timezone}
            onChange={(e) => set('timezone')(e.target.value)}
            required
          />
        </FormField>
        <FormField label={t('lodging.office.properties.fields.city')}>
          <Input name="city" value={form.city} onChange={(e) => set('city')(e.target.value)} />
        </FormField>
        <FormField label={t('lodging.office.properties.fields.region')}>
          <Input
            name="regionCode"
            value={form.regionCode}
            onChange={(e) => set('regionCode')(e.target.value)}
            className="font-mono"
          />
        </FormField>
        <div className="md:col-span-2">
          <ProblemAlert
            problem={create.isError ? problemOf(create.error) : null}
            className="mb-3"
          />
          <div className="flex gap-2">
            <Button
              type="submit"
              loading={create.isPending}
              disabled={
                !form.providerOrganizationId || form.code.trim() === '' || form.name.trim() === ''
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
