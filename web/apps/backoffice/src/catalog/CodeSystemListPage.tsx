import {
  randomId,
  type CodeSystem,
  type CodeSystemAuthority,
  type CodeSystemStatus,
  type CreateCodeSystemRequest,
  type Problem,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Dialog,
  EmptyState,
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
import { Link } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { useRef, useState } from 'react';
import { useForm } from 'react-hook-form';

import { problemOf } from '../problems';
import { todayIso, useFieldMessage, useServerFieldErrors } from './forms';
import { useCodeSystems, useCreateCodeSystem } from './queries';
import {
  CODE_SYSTEM_AUTHORITIES,
  codeSystemFormSchema,
  emptyCodeSystemForm,
  type CodeSystemFormValues,
} from './schema';

const PAGE_SIZE = 50;

/**
 * The code systems the tenant reports under: SUT, HUV, ICD-10 and its own internal lists.
 * A system is identified by its code together with its version, because a publisher
 * reissues the same code system every year and last year's claim belongs to last year's
 * edition.
 */
export function CodeSystemListPage() {
  const { t } = useTranslation();
  const canManage = usePermission('catalog.manage');

  const [draftQ, setDraftQ] = useState('');
  const [q, setQ] = useState('');
  const [authority, setAuthority] = useState('');
  const [status, setStatus] = useState('');
  const [cursor, setCursor] = useState<string | null>(null);
  const [trail, setTrail] = useState<(string | null)[]>([]);
  const [creating, setCreating] = useState(false);

  const query = useCodeSystems({
    limit: PAGE_SIZE,
    ...(q ? { q } : {}),
    ...(authority ? { authority: authority as CodeSystemAuthority } : {}),
    ...(status ? { status: status as CodeSystemStatus } : {}),
    ...(cursor ? { cursor } : {}),
  });

  function resetPaging() {
    setCursor(null);
    setTrail([]);
  }

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, cursor]);
    setCursor(next);
  }

  function goPrevious() {
    setCursor(trail[trail.length - 1] ?? null);
    setTrail((rest) => rest.slice(0, -1));
  }

  const rows: CodeSystem[] = query.data?.items ?? [];

  return (
    <>
      <PageHeader
        title={t('codeSystems.title')}
        description={t('codeSystems.intro')}
        actions={
          canManage ? (
            <Button onClick={() => setCreating(true)}>{t('codeSystems.new')}</Button>
          ) : null
        }
      />

      <form
        onSubmit={(event) => {
          event.preventDefault();
          setQ(draftQ.trim());
          resetPaging();
        }}
        className="mb-4 flex flex-wrap items-end gap-2"
        role="search"
      >
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('catalog.search')}</span>
          <Input
            name="q"
            value={draftQ}
            onChange={(event) => setDraftQ(event.target.value)}
            className="w-64"
          />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('codeSystems.fields.authority')}</span>
          <Select
            name="authority"
            value={authority}
            onChange={(event) => {
              setAuthority(event.target.value);
              resetPaging();
            }}
            placeholder={t('catalog.allStatuses')}
            options={CODE_SYSTEM_AUTHORITIES.map((value) => ({
              value,
              label: t(`codeSystems.authorities.${value}`),
            }))}
            className="w-48"
          />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('catalog.statusFilter')}</span>
          <Select
            name="status"
            value={status}
            onChange={(event) => {
              setStatus(event.target.value);
              resetPaging();
            }}
            placeholder={t('catalog.allStatuses')}
            options={[
              { value: 'ACTIVE', label: t('catalog.active') },
              { value: 'INACTIVE', label: t('catalog.inactive') },
            ]}
            className="w-40"
          />
        </label>
        <Button type="submit" variant="secondary">
          {t('common.search')}
        </Button>
        {q || authority || status ? (
          <Button
            variant="ghost"
            onClick={() => {
              setDraftQ('');
              setQ('');
              setAuthority('');
              setStatus('');
              resetPaging();
            }}
          >
            {t('common.clear')}
          </Button>
        ) : null}
      </form>

      <ProblemAlert
        problem={query.error ? problemOf(query.error) : null}
        className="mb-4"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />

      {query.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <EmptyState title={t('codeSystems.empty')} />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="code-system-table">
            <THead>
              <TR>
                <TH>{t('codeSystems.fields.code')}</TH>
                <TH>{t('codeSystems.fields.version')}</TH>
                <TH>{t('codeSystems.fields.name')}</TH>
                <TH>{t('codeSystems.fields.authority')}</TH>
                <TH>{t('codeSystems.fields.validFrom')}</TH>
                <TH>{t('codeSystems.fields.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((system) => (
                <TR key={system.id}>
                  <TD>
                    <code className="font-mono text-xs">{system.code}</code>
                  </TD>
                  <TD>
                    <code className="font-mono text-xs">{system.version}</code>
                  </TD>
                  <TD>
                    <Link
                      to="/catalog/code-systems/$codeSystemId"
                      params={{ codeSystemId: system.id }}
                      className="font-medium underline-offset-2 hover:underline"
                    >
                      {system.name}
                    </Link>
                  </TD>
                  <TD>{t(`codeSystems.authorities.${system.authority}`)}</TD>
                  <TD>
                    {formatDate(system.validFrom)} –{' '}
                    {formatDate(system.validTo) || t('common.none')}
                  </TD>
                  <TD>
                    <Badge tone={system.status === 'ACTIVE' ? 'success' : 'neutral'}>
                      {system.status === 'ACTIVE' ? t('catalog.active') : t('catalog.inactive')}
                    </Badge>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
          <nav className="mt-3 flex items-center justify-between text-sm" aria-label="Sayfalama">
            <span className="text-fg-muted">
              {t('organizations.page', { n: trail.length + 1 })}
            </span>
            <div className="flex gap-2">
              <Button
                variant="secondary"
                size="sm"
                onClick={goPrevious}
                disabled={trail.length === 0}
              >
                {t('organizations.prevPage')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={goNext}
                disabled={!query.data?.nextCursor}
              >
                {t('organizations.nextPage')}
              </Button>
            </div>
          </nav>
        </>
      )}

      {canManage && creating ? <CodeSystemCreateDialog onClose={() => setCreating(false)} /> : null}
    </>
  );
}

/** Mounted only while it is open, so every dialog starts on a clean form. */
function CodeSystemCreateDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const create = useCreateCodeSystem();
  const [problem, setProblem] = useState<Problem | null>(null);
  // One key per dialog instance: a retried submit replays instead of creating a twin.
  const idempotencyKey = useRef(randomId());

  const form = useForm<CodeSystemFormValues>({
    resolver: zodResolver(codeSystemFormSchema),
    defaultValues: emptyCodeSystemForm(todayIso()),
    mode: 'onBlur',
  });
  useServerFieldErrors(form, problem);
  const message = useFieldMessage();

  async function submit(values: CodeSystemFormValues) {
    setProblem(null);
    const body: CreateCodeSystemRequest = {
      code: values.code,
      name: values.name,
      version: values.version,
      authority: values.authority,
      licensed: values.licensed,
      validFrom: values.validFrom,
      ...(values.validTo ? { validTo: values.validTo } : {}),
    };
    try {
      await create.mutateAsync({ body, idempotencyKey: idempotencyKey.current });
      toast.notify({ tone: 'success', title: t('codeSystems.created') });
      onClose();
    } catch (err) {
      setProblem(problemOf(err));
      // Nothing was written, so the next attempt starts a fresh command.
      idempotencyKey.current = randomId();
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title={t('codeSystems.createTitle')}
    >
      <form
        onSubmit={(event) => void form.handleSubmit(submit)(event)}
        className="grid gap-4"
        noValidate
        data-testid="code-system-create-form"
      >
        <ProblemAlert problem={problem} hideFieldErrors />
        <div className="grid gap-4 md:grid-cols-2">
          <FormField
            label={t('codeSystems.fields.code')}
            required
            requiredLabel={t('common.requiredMark')}
            hint={t('catalog.codeImmutableHint')}
            error={message(form.formState.errors.code)}
          >
            <Input {...form.register('code')} autoComplete="off" className="font-mono" />
          </FormField>
          <FormField
            label={t('codeSystems.fields.version')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.version)}
          >
            <Input {...form.register('version')} autoComplete="off" className="font-mono" />
          </FormField>
        </div>
        <FormField
          label={t('codeSystems.fields.name')}
          required
          requiredLabel={t('common.requiredMark')}
          error={message(form.formState.errors.name)}
        >
          <Input {...form.register('name')} />
        </FormField>
        <div className="grid gap-4 md:grid-cols-2">
          <FormField
            label={t('codeSystems.fields.validFrom')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.validFrom)}
          >
            <Input {...form.register('validFrom')} type="date" />
          </FormField>
          <FormField
            label={t('codeSystems.fields.validTo')}
            error={message(form.formState.errors.validTo)}
          >
            <Input {...form.register('validTo')} type="date" />
          </FormField>
        </div>
        <FormField label={t('codeSystems.fields.authority')}>
          <Select
            {...form.register('authority')}
            options={CODE_SYSTEM_AUTHORITIES.map((value) => ({
              value,
              label: t(`codeSystems.authorities.${value}`),
            }))}
          />
        </FormField>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" {...form.register('licensed')} />
          {t('codeSystems.fields.licensed')}
        </label>
        <p className="text-fg-muted text-xs">{t('codeSystems.licensedHint')}</p>
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose} disabled={create.isPending}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={create.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
