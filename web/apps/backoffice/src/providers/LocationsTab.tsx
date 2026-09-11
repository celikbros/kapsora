import {
  randomId,
  type CreateProviderLocationRequest,
  type ProviderLocation,
  type ProviderLocationListQuery,
  type ProviderLocationStatus,
  type UpdateProviderLocationRequest,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
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
import { useForm, type UseFormReturn } from 'react-hook-form';

import { problemOf } from '../problems';
import { useCreateLocation, useProviderLocations, useUpdateLocation } from './queries';
import {
  LOCATION_STATUSES,
  emptyLocationForm,
  locationFormSchema,
  parseIssueMessage,
  type LocationFormValues,
} from './schema';

const PAGE_SIZE = 25;

/** The places a provider actually delivers from. The code is set once and never changes. */
export function LocationsTab({ providerId }: { providerId: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const canManage = usePermission('provider.manage');
  const [city, setCity] = useState('');
  const [status, setStatus] = useState('');
  const [cursor, setCursor] = useState<string | null>(null);
  const [trail, setTrail] = useState<string[]>([]);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<ProviderLocation | null>(null);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);

  const listQuery: ProviderLocationListQuery = {
    ...(city ? { city } : {}),
    ...(status ? { status: status as ProviderLocationStatus } : {}),
    ...(cursor ? { cursor } : {}),
    limit: PAGE_SIZE,
  };
  const locations = useProviderLocations(providerId, listQuery);
  const create = useCreateLocation(providerId);
  const update = useUpdateLocation();

  const createForm = useForm<LocationFormValues>({
    resolver: zodResolver(locationFormSchema),
    defaultValues: emptyLocationForm,
    mode: 'onBlur',
  });
  const editForm = useForm<LocationFormValues>({
    resolver: zodResolver(locationFormSchema),
    defaultValues: emptyLocationForm,
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  function resetPaging() {
    setCursor(null);
    setTrail([]);
  }

  async function submitCreate(values: LocationFormValues) {
    setProblem(null);
    const body: CreateProviderLocationRequest = {
      code: values.code,
      name: values.name,
      countryCode: values.countryCode,
      timezone: values.timezone,
      ...(values.addressLine ? { addressLine: values.addressLine } : {}),
      ...(values.district ? { district: values.district } : {}),
      ...(values.city ? { city: values.city } : {}),
      ...(values.postalCode ? { postalCode: values.postalCode } : {}),
      ...(values.phone ? { phone: values.phone } : {}),
      ...(values.latitude !== '' && values.longitude !== ''
        ? { latitude: Number(values.latitude), longitude: Number(values.longitude) }
        : {}),
    };
    try {
      await create.mutateAsync({ body, idempotencyKey: randomId() });
      toast.notify({ tone: 'success', title: t('locations.created') });
      createForm.reset(emptyLocationForm);
      setAdding(false);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitEdit(values: LocationFormValues) {
    if (!editing) return;
    setProblem(null);
    // The code is absent on purpose: the server answers IMMUTABLE to a code in a patch.
    const patch: UpdateProviderLocationRequest = {
      name: values.name,
      countryCode: values.countryCode,
      timezone: values.timezone,
      status: values.status,
      addressLine: values.addressLine === '' ? null : values.addressLine,
      district: values.district === '' ? null : values.district,
      city: values.city === '' ? null : values.city,
      postalCode: values.postalCode === '' ? null : values.postalCode,
      phone: values.phone === '' ? null : values.phone,
      // A pin is given or removed as a pair; the database refuses half of one.
      latitude: values.latitude === '' ? null : Number(values.latitude),
      longitude: values.longitude === '' ? null : Number(values.longitude),
    };
    try {
      await update.mutateAsync({
        locationId: editing.id,
        etag: `"${editing.rowVersion}"`,
        patch,
      });
      toast.notify({ tone: 'success', title: t('locations.updated') });
      setEditing(null);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  const rows: ProviderLocation[] = locations.data?.items ?? [];

  return (
    <Card>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <h2 className="text-base font-semibold">{t('locations.title')}</h2>
        {canManage ? (
          <Button size="sm" onClick={() => setAdding(true)}>
            {t('locations.new')}
          </Button>
        ) : null}
      </div>

      <div className="mt-4 flex flex-wrap items-end gap-2">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('providers.cityFilter')}</span>
          <Input
            value={city}
            onChange={(e) => {
              setCity(e.target.value);
              resetPaging();
            }}
            className="w-48"
          />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('providers.statusFilter')}</span>
          <Select
            value={status}
            onChange={(e) => {
              setStatus(e.target.value);
              resetPaging();
            }}
            placeholder={t('providers.allStatuses')}
            options={LOCATION_STATUSES.map((value) => ({
              value,
              label: t(`locations.statuses.${value}`),
            }))}
            className="w-48"
          />
        </label>
      </div>

      <ProblemAlert
        page
        problem={problem ?? (locations.error ? problemOf(locations.error) : null)}
        className="mt-4"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void locations.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />

      {locations.isPending ? (
        <div className="text-fg-muted mt-4 flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <div className="mt-4">
          <EmptyState title={t('locations.empty')} />
        </div>
      ) : (
        <div className="mt-4">
          <Table data-testid="location-table">
            <THead>
              <TR>
                <TH>{t('locations.columns.code')}</TH>
                <TH>{t('locations.columns.name')}</TH>
                <TH>{t('locations.columns.city')}</TH>
                <TH>{t('locations.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((location) => (
                <TR key={location.id}>
                  <TD>
                    <code className="font-mono text-xs">{location.code}</code>
                  </TD>
                  <TD>{location.name}</TD>
                  <TD>{location.city ?? t('common.none')}</TD>
                  <TD>
                    <div className="flex items-center gap-2">
                      <Badge tone={statusTone(location.status)}>
                        {t(`locations.statuses.${location.status}`)}
                      </Badge>
                      {canManage ? (
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => {
                            editForm.reset({
                              code: location.code,
                              name: location.name,
                              addressLine: location.addressLine ?? '',
                              district: location.district ?? '',
                              city: location.city ?? '',
                              postalCode: location.postalCode ?? '',
                              countryCode: location.countryCode,
                              phone: location.phone ?? '',
                              timezone: location.timezone,
                              latitude: location.latitude == null ? '' : String(location.latitude),
                              longitude:
                                location.longitude == null ? '' : String(location.longitude),
                              status: location.status,
                            });
                            setProblem(null);
                            setEditing(location);
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
          <nav
            className="mt-3 flex items-center justify-between text-sm"
            aria-labelledby="location-page-label"
          >
            <span id="location-page-label" className="text-fg-muted">
              {t('organizations.page', { n: trail.length + 1 })}
            </span>
            <div className="flex gap-2">
              <Button
                variant="secondary"
                size="sm"
                disabled={trail.length === 0}
                onClick={() => {
                  const previous = trail[trail.length - 1] ?? '';
                  setTrail((rest) => rest.slice(0, -1));
                  setCursor(previous === '' ? null : previous);
                }}
              >
                {t('organizations.prevPage')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={!locations.data?.nextCursor}
                onClick={() => {
                  const next = locations.data?.nextCursor;
                  if (!next) return;
                  setTrail((previous) => [...previous, cursor ?? '']);
                  setCursor(next);
                }}
              >
                {t('organizations.nextPage')}
              </Button>
            </div>
          </nav>
        </div>
      )}

      <Dialog
        open={adding}
        onOpenChange={(open) => {
          if (!open) setAdding(false);
        }}
        title={t('locations.createTitle')}
      >
        <form onSubmit={createForm.handleSubmit(submitCreate)} className="grid gap-4" noValidate>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('locations.fields.code')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(createForm.formState.errors.code)}
            >
              <Input {...createForm.register('code')} autoComplete="off" className="font-mono" />
            </FormField>
            <FormField
              label={t('locations.fields.name')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(createForm.formState.errors.name)}
            >
              <Input {...createForm.register('name')} />
            </FormField>
          </div>
          <LocationAddressFields form={createForm} />
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
        title={t('locations.editTitle')}
      >
        <form onSubmit={editForm.handleSubmit(submitEdit)} className="grid gap-4" noValidate>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField label={t('locations.fields.code')}>
              <Input value={editing?.code ?? ''} readOnly className="font-mono" />
            </FormField>
            <FormField
              label={t('locations.fields.name')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(editForm.formState.errors.name)}
            >
              <Input {...editForm.register('name')} />
            </FormField>
            <FormField
              label={t('locations.fields.status')}
              error={message(editForm.formState.errors.status)}
            >
              <Select
                {...editForm.register('status')}
                options={LOCATION_STATUSES.map((value) => ({
                  value,
                  label: t(`locations.statuses.${value}`),
                }))}
              />
            </FormField>
          </div>
          <LocationAddressFields form={editForm} />
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

/** Address, contact and time zone; identical on the create and the edit dialog. */
function LocationAddressFields({ form }: { form: UseFormReturn<LocationFormValues> }) {
  const { t } = useTranslation();
  const errors = form.formState.errors;

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  return (
    <>
      <FormField label={t('locations.fields.addressLine')} error={message(errors.addressLine)}>
        <Input {...form.register('addressLine')} />
      </FormField>
      <div className="grid gap-4 md:grid-cols-2">
        <FormField label={t('locations.fields.district')} error={message(errors.district)}>
          <Input {...form.register('district')} />
        </FormField>
        <FormField label={t('locations.fields.city')} error={message(errors.city)}>
          <Input {...form.register('city')} />
        </FormField>
        <FormField label={t('locations.fields.postalCode')} error={message(errors.postalCode)}>
          <Input {...form.register('postalCode')} className="font-mono" />
        </FormField>
        <FormField label={t('locations.fields.countryCode')} error={message(errors.countryCode)}>
          <Input {...form.register('countryCode')} className="font-mono" maxLength={2} />
        </FormField>
        <FormField label={t('locations.fields.phone')} error={message(errors.phone)}>
          <Input {...form.register('phone')} autoComplete="off" />
        </FormField>
        <FormField label={t('locations.fields.latitude')} error={message(errors.latitude)}>
          <Input {...form.register('latitude')} inputMode="decimal" className="font-mono" />
        </FormField>
        <FormField label={t('locations.fields.longitude')} error={message(errors.longitude)}>
          <Input {...form.register('longitude')} inputMode="decimal" className="font-mono" />
        </FormField>
        <FormField label={t('locations.fields.timezone')} error={message(errors.timezone)}>
          <Input {...form.register('timezone')} className="font-mono" />
        </FormField>
      </div>
    </>
  );
}
