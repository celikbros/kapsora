import {
  randomId,
  type CreatePractitionerRequest,
  type Practitioner,
  type PractitionerListQuery,
  type PractitionerLocationInput,
  type PractitionerStatus,
  type ProviderLocation,
  type UpdatePractitionerRequest,
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
import { useFieldArray, useForm } from 'react-hook-form';

import { problemOf } from '../problems';
import {
  useCreatePractitioner,
  usePractitioners,
  useProviderLocations,
  useReplacePractitionerLocations,
  useUpdatePractitioner,
} from './queries';
import { RegistrationSearchDialog } from './RegistrationSearchDialog';
import { useProviderNavigate } from './routes';
import {
  PRACTITIONER_ROLES,
  PRACTITIONER_STATUSES,
  REGISTRATION_AUTHORITIES,
  assignmentsFormSchema,
  emptyAssignmentRow,
  emptyPractitionerForm,
  parseIssueMessage,
  practitionerEditSchema,
  practitionerFormSchema,
  type AssignmentsFormValues,
  type PractitionerEditValues,
  type PractitionerFormValues,
} from './schema';

const PAGE_SIZE = 25;

/**
 * The people registered at this provider. A registration number is entered once, on
 * creation; afterwards only the masked form is ever shown, the edit form has no field for
 * it, and finding somebody by it goes through the step-up guarded dialog.
 */
export function PractitionersTab({ providerId }: { providerId: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useProviderNavigate();
  const canManage = usePermission('provider.practitioner.manage');
  const [q, setQ] = useState('');
  const [applied, setApplied] = useState('');
  const [status, setStatus] = useState('');
  const [cursor, setCursor] = useState<string | null>(null);
  const [trail, setTrail] = useState<string[]>([]);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Practitioner | null>(null);
  const [assigning, setAssigning] = useState<Practitioner | null>(null);
  const [searching, setSearching] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);

  const listQuery: PractitionerListQuery = {
    ...(applied ? { q: applied } : {}),
    ...(status ? { status: status as PractitionerStatus } : {}),
    ...(cursor ? { cursor } : {}),
    limit: PAGE_SIZE,
  };
  const practitioners = usePractitioners(providerId, listQuery);
  const locations = useProviderLocations(providerId, { limit: 200 });
  const create = useCreatePractitioner(providerId);
  const update = useUpdatePractitioner();

  const createForm = useForm<PractitionerFormValues>({
    resolver: zodResolver(practitionerFormSchema),
    defaultValues: emptyPractitionerForm,
    mode: 'onBlur',
  });
  const editForm = useForm<PractitionerEditValues>({
    resolver: zodResolver(practitionerEditSchema),
    defaultValues: {
      fullName: '',
      title: '',
      branchCode: '',
      status: 'ACTIVE',
      validFrom: '',
      validTo: '',
    },
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

  function openEdit(practitioner: Practitioner) {
    editForm.reset({
      fullName: practitioner.fullName,
      title: practitioner.title ?? '',
      branchCode: practitioner.branchCode ?? '',
      status: practitioner.status,
      validFrom: practitioner.validFrom ?? '',
      validTo: practitioner.validTo ?? '',
    });
    setProblem(null);
    setEditing(practitioner);
  }

  async function submitCreate(values: PractitionerFormValues) {
    setProblem(null);
    const body: CreatePractitionerRequest = {
      fullName: values.fullName,
      registrationAuthority: values.registrationAuthority,
      registrationNumber: values.registrationNumber,
      ...(values.title ? { title: values.title } : {}),
      ...(values.branchCode ? { branchCode: values.branchCode } : {}),
      ...(values.validFrom ? { validFrom: values.validFrom } : {}),
      ...(values.validTo ? { validTo: values.validTo } : {}),
    };
    try {
      await create.mutateAsync({ body, idempotencyKey: randomId() });
      toast.notify({ tone: 'success', title: t('practitioners.created') });
      // The number is dropped from the form the moment the server has it.
      createForm.reset(emptyPractitionerForm);
      setAdding(false);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitEdit(values: PractitionerEditValues) {
    if (!editing) return;
    setProblem(null);
    // The registration is absent on purpose: it is ended and re-registered, never rewritten.
    const patch: UpdatePractitionerRequest = {
      fullName: values.fullName,
      status: values.status,
      title: values.title === '' ? null : values.title,
      branchCode: values.branchCode === '' ? null : values.branchCode,
      validFrom: values.validFrom === '' ? null : values.validFrom,
      validTo: values.validTo === '' ? null : values.validTo,
    };
    try {
      await update.mutateAsync({
        practitionerId: editing.id,
        etag: `"${editing.rowVersion}"`,
        patch,
      });
      toast.notify({ tone: 'success', title: t('practitioners.updated') });
      setEditing(null);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  function handleFound(found: Practitioner) {
    if (found.providerId !== providerId) {
      void navigate({ to: '/providers/$providerId', params: { providerId: found.providerId } });
      return;
    }
    openEdit(found);
  }

  const rows: Practitioner[] = practitioners.data?.items ?? [];
  const locationRows: ProviderLocation[] = locations.data?.items ?? [];

  return (
    <Card>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <h2 className="text-base font-semibold">{t('practitioners.title')}</h2>
        {canManage ? (
          <div className="flex gap-2">
            <Button size="sm" variant="secondary" onClick={() => setSearching(true)}>
              {t('practitioners.search')}
            </Button>
            <Button size="sm" onClick={() => setAdding(true)}>
              {t('practitioners.new')}
            </Button>
          </div>
        ) : null}
      </div>

      <form
        className="mt-4 flex flex-wrap items-end gap-2"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
          setApplied(q.trim());
          resetPaging();
        }}
      >
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('practitioners.columns.name')}</span>
          <Input value={q} onChange={(e) => setQ(e.target.value)} className="w-48" />
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
            options={PRACTITIONER_STATUSES.map((value) => ({
              value,
              label: t(`practitioners.statuses.${value}`),
            }))}
            className="w-48"
          />
        </label>
        <Button type="submit" variant="secondary">
          {t('common.search')}
        </Button>
      </form>

      <ProblemAlert
        problem={problem ?? (practitioners.error ? problemOf(practitioners.error) : null)}
        className="mt-4"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void practitioners.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />

      {practitioners.isPending ? (
        <div className="text-fg-muted mt-4 flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <div className="mt-4">
          <EmptyState title={t('practitioners.empty')} />
        </div>
      ) : (
        <div className="mt-4">
          <Table data-testid="practitioner-table">
            <THead>
              <TR>
                <TH>{t('practitioners.columns.name')}</TH>
                <TH>{t('practitioners.columns.title')}</TH>
                <TH>{t('practitioners.columns.branch')}</TH>
                <TH>{t('practitioners.columns.registration')}</TH>
                <TH>{t('practitioners.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((practitioner) => (
                <TR key={practitioner.id}>
                  <TD>{practitioner.fullName}</TD>
                  <TD>{practitioner.title ?? t('common.none')}</TD>
                  <TD>
                    <code className="font-mono text-xs">
                      {practitioner.branchCode ?? t('common.none')}
                    </code>
                  </TD>
                  <TD>
                    <code className="font-mono text-xs">
                      {t(`practitioners.authorities.${practitioner.registrationAuthority}`)}{' '}
                      {practitioner.maskedRegistrationNumber}
                    </code>
                  </TD>
                  <TD>
                    <div className="flex items-center gap-2">
                      <Badge tone={statusTone(practitioner.status)}>
                        {t(`practitioners.statuses.${practitioner.status}`)}
                      </Badge>
                      {canManage ? (
                        <>
                          <Button size="sm" variant="ghost" onClick={() => openEdit(practitioner)}>
                            {t('organizations.edit')}
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => {
                              setProblem(null);
                              setAssigning(practitioner);
                            }}
                          >
                            {t('practitioners.assignments.title')}
                          </Button>
                        </>
                      ) : null}
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
          <nav
            className="mt-3 flex items-center justify-between text-sm"
            aria-labelledby="practitioner-page-label"
          >
            <span id="practitioner-page-label" className="text-fg-muted">
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
                disabled={!practitioners.data?.nextCursor}
                onClick={() => {
                  const next = practitioners.data?.nextCursor;
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
        title={t('practitioners.createTitle')}
      >
        <form onSubmit={createForm.handleSubmit(submitCreate)} className="grid gap-4" noValidate>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('practitioners.fields.fullName')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(createForm.formState.errors.fullName)}
            >
              <Input {...createForm.register('fullName')} />
            </FormField>
            <FormField
              label={t('practitioners.fields.title')}
              error={message(createForm.formState.errors.title)}
            >
              <Input {...createForm.register('title')} />
            </FormField>
            <FormField
              label={t('practitioners.fields.branchCode')}
              error={message(createForm.formState.errors.branchCode)}
            >
              <Input {...createForm.register('branchCode')} className="font-mono" />
            </FormField>
            <FormField
              label={t('practitioners.fields.registrationAuthority')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(createForm.formState.errors.registrationAuthority)}
            >
              <Select
                {...createForm.register('registrationAuthority')}
                options={REGISTRATION_AUTHORITIES.map((authority) => ({
                  value: authority,
                  label: t(`practitioners.authorities.${authority}`),
                }))}
              />
            </FormField>
          </div>
          <FormField
            label={t('practitioners.fields.registrationNumber')}
            required
            requiredLabel={t('common.requiredMark')}
            hint={t('practitioners.registrationHint')}
            error={message(createForm.formState.errors.registrationNumber)}
          >
            <Input
              {...createForm.register('registrationNumber')}
              inputMode="numeric"
              autoComplete="off"
              className="font-mono"
            />
          </FormField>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('practitioners.fields.validFrom')}
              error={message(createForm.formState.errors.validFrom)}
            >
              <Input {...createForm.register('validFrom')} type="date" />
            </FormField>
            <FormField
              label={t('practitioners.fields.validTo')}
              error={message(createForm.formState.errors.validTo)}
            >
              <Input {...createForm.register('validTo')} type="date" />
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
        title={t('practitioners.detailTitle')}
      >
        <form onSubmit={editForm.handleSubmit(submitEdit)} className="grid gap-4" noValidate>
          <FormField
            label={t('practitioners.fields.registrationNumber')}
            hint={t('practitioners.registrationHint')}
          >
            <Input value={editing?.maskedRegistrationNumber ?? ''} readOnly className="font-mono" />
          </FormField>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('practitioners.fields.fullName')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(editForm.formState.errors.fullName)}
            >
              <Input {...editForm.register('fullName')} />
            </FormField>
            <FormField
              label={t('practitioners.fields.title')}
              error={message(editForm.formState.errors.title)}
            >
              <Input {...editForm.register('title')} />
            </FormField>
            <FormField
              label={t('practitioners.fields.branchCode')}
              error={message(editForm.formState.errors.branchCode)}
            >
              <Input {...editForm.register('branchCode')} className="font-mono" />
            </FormField>
            <FormField
              label={t('practitioners.fields.status')}
              error={message(editForm.formState.errors.status)}
            >
              <Select
                {...editForm.register('status')}
                options={PRACTITIONER_STATUSES.map((value) => ({
                  value,
                  label: t(`practitioners.statuses.${value}`),
                }))}
              />
            </FormField>
            <FormField
              label={t('practitioners.fields.validFrom')}
              error={message(editForm.formState.errors.validFrom)}
            >
              <Input {...editForm.register('validFrom')} type="date" />
            </FormField>
            <FormField
              label={t('practitioners.fields.validTo')}
              error={message(editForm.formState.errors.validTo)}
            >
              <Input {...editForm.register('validTo')} type="date" />
            </FormField>
          </div>
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

      {assigning ? (
        <AssignmentsDialog
          key={assigning.id + String(assigning.rowVersion)}
          practitioner={assigning}
          locations={locationRows}
          onClose={() => setAssigning(null)}
        />
      ) : null}

      {canManage ? (
        <RegistrationSearchDialog
          open={searching}
          onClose={() => setSearching(false)}
          onFound={handleFound}
        />
      ) : null}
    </Card>
  );
}

/**
 * Where a practitioner works and in what role, written as a set: one PUT replaces the
 * whole list, and two rows for the same location and role in overlapping periods are
 * refused with PRACTITIONER_ASSIGNMENT_OVERLAP.
 */
function AssignmentsDialog({
  practitioner,
  locations,
  onClose,
}: {
  practitioner: Practitioner;
  locations: ProviderLocation[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const save = useReplacePractitionerLocations();
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);

  const form = useForm<AssignmentsFormValues>({
    resolver: zodResolver(assignmentsFormSchema),
    defaultValues: {
      items: (practitioner.locations ?? []).map((assignment) => ({
        locationId: assignment.locationId,
        role: assignment.role,
        validFrom: assignment.validFrom,
        validTo: assignment.validTo ?? '',
      })),
    },
    mode: 'onBlur',
  });
  const rows = useFieldArray({ control: form.control, name: 'items' });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function submit(values: AssignmentsFormValues) {
    setProblem(null);
    const items: PractitionerLocationInput[] = values.items.map((row) => ({
      locationId: row.locationId,
      role: row.role,
      validFrom: row.validFrom,
      ...(row.validTo ? { validTo: row.validTo } : {}),
    }));
    try {
      await save.mutateAsync({
        practitionerId: practitioner.id,
        etag: `"${practitioner.rowVersion}"`,
        items,
      });
      toast.notify({ tone: 'success', title: t('practitioners.assignments.saved') });
      onClose();
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      title={t('practitioners.assignments.title')}
      description={practitioner.fullName}
      className="w-[min(92vw,44rem)]"
    >
      <form
        onSubmit={form.handleSubmit(submit)}
        className="grid gap-4"
        noValidate
        data-testid="assignment-form"
      >
        <ProblemAlert problem={problem} />

        {rows.fields.length === 0 ? (
          <EmptyState title={t('practitioners.assignments.empty')} />
        ) : (
          <div className="grid gap-4">
            {rows.fields.map((field, index) => {
              const errors = form.formState.errors.items?.[index];
              return (
                <fieldset key={field.id} className="border-line grid gap-3 rounded-md border p-4">
                  <legend className="text-fg-muted px-1 text-xs font-medium">
                    {t('practitioners.assignments.columns.location')}
                  </legend>
                  <div className="grid gap-3 md:grid-cols-2">
                    <FormField
                      label={t('practitioners.assignments.columns.location')}
                      required
                      requiredLabel={t('common.requiredMark')}
                      error={message(errors?.locationId)}
                    >
                      <Select
                        {...form.register(`items.${index}.locationId`)}
                        placeholder={t('common.none')}
                        options={locations.map((location) => ({
                          value: location.id,
                          label: `${location.code} · ${location.name}`,
                        }))}
                      />
                    </FormField>
                    <FormField
                      label={t('practitioners.assignments.columns.role')}
                      error={message(errors?.role)}
                    >
                      <Select
                        {...form.register(`items.${index}.role`)}
                        options={PRACTITIONER_ROLES.map((role) => ({
                          value: role,
                          label: t(`practitioners.roles.${role}`),
                        }))}
                      />
                    </FormField>
                    <FormField
                      label={t('practitioners.fields.validFrom')}
                      required
                      requiredLabel={t('common.requiredMark')}
                      error={message(errors?.validFrom)}
                    >
                      <Input {...form.register(`items.${index}.validFrom`)} type="date" />
                    </FormField>
                    <FormField
                      label={t('practitioners.fields.validTo')}
                      error={message(errors?.validTo)}
                    >
                      <Input {...form.register(`items.${index}.validTo`)} type="date" />
                    </FormField>
                  </div>
                  <div className="flex justify-end">
                    <Button variant="ghost" size="sm" onClick={() => rows.remove(index)}>
                      {t('capabilities.remove')}
                    </Button>
                  </div>
                </fieldset>
              );
            })}
          </div>
        )}

        <div className="flex justify-between gap-2">
          <Button variant="secondary" onClick={() => rows.append(emptyAssignmentRow)}>
            {t('practitioners.assignments.add')}
          </Button>
          <div className="flex gap-2">
            <Button variant="secondary" onClick={onClose} disabled={save.isPending}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={save.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </div>
      </form>
    </Dialog>
  );
}
