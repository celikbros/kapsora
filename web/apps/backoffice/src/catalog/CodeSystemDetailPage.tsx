import {
  randomId,
  type CodeSystem,
  type CodeValue,
  type CodeValueImportResult,
  type Problem,
  type UpdateCodeSystemRequest,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
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
  Textarea,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { Link, useParams } from '@tanstack/react-router';
import { useEffect, useState, type FormEvent } from 'react';
import { useForm } from 'react-hook-form';

import { problemOf } from '../problems';
import { todayIso, useFieldMessage, useServerFieldErrors } from './forms';
import { useCodeSystem, useCodeValues, useImportCodeValues, useUpdateCodeSystem } from './queries';
import {
  CODE_SYSTEM_AUTHORITIES,
  CODE_SYSTEM_STATUSES,
  codeSystemEditSchema,
  parseCodeValueBatch,
  type CodeSystemEditValues,
} from './schema';

const PAGE_SIZE = 50;

/**
 * One code system: what it is, and the codes it holds on a date. The date is the point of
 * the screen — a publisher retires a code at the end of a year and issues a replacement,
 * so a claim from last year has to be read against last year's list, not today's.
 */
export function CodeSystemDetailPage() {
  const { t } = useTranslation();
  const params: Record<string, string | undefined> = useParams({ strict: false });
  const codeSystemId = params['codeSystemId'] ?? '';
  const canManage = usePermission('catalog.manage');
  const query = useCodeSystem(codeSystemId);

  if (query.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (query.error || !query.data) {
    return (
      <ProblemAlert
        problem={problemOf(query.error)}
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
    );
  }

  const system = query.data.data;

  return (
    <>
      <PageHeader
        title={system.name}
        description={t('codeSystems.intro')}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('codeSystems.title'),
                render: (label) => <Link to="/catalog/code-systems">{label}</Link>,
              },
              { label: `${system.code} ${system.version}` },
            ]}
          />
        }
        actions={
          <Badge tone={system.status === 'ACTIVE' ? 'success' : 'neutral'}>
            {system.status === 'ACTIVE' ? t('catalog.active') : t('catalog.inactive')}
          </Badge>
        }
      />

      <div className="grid gap-6">
        {canManage ? (
          <CodeSystemForm system={system} etag={query.data.etag} />
        ) : (
          <CodeSystemCard system={system} />
        )}
        <CodeValuesPanel codeSystemId={codeSystemId} canManage={canManage} />
      </div>
    </>
  );
}

function CodeSystemForm({ system, etag }: { system: CodeSystem; etag: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const update = useUpdateCodeSystem(system.id);
  const [problem, setProblem] = useState<Problem | null>(null);

  const form = useForm<CodeSystemEditValues>({
    resolver: zodResolver(codeSystemEditSchema),
    defaultValues: {
      name: system.name,
      authority: system.authority,
      licensed: system.licensed,
      status: system.status,
      validTo: system.validTo ?? '',
    },
    mode: 'onBlur',
  });
  useServerFieldErrors(form, problem);
  const message = useFieldMessage();

  useEffect(() => {
    form.reset({
      name: system.name,
      authority: system.authority,
      licensed: system.licensed,
      status: system.status,
      validTo: system.validTo ?? '',
    });
  }, [system, form]);

  async function submit(values: CodeSystemEditValues) {
    setProblem(null);
    const validTo = values.validTo === '' ? null : values.validTo;
    const patch: UpdateCodeSystemRequest = {
      ...(values.name !== system.name ? { name: values.name } : {}),
      ...(values.authority !== system.authority ? { authority: values.authority } : {}),
      ...(values.licensed !== system.licensed ? { licensed: values.licensed } : {}),
      ...(values.status !== system.status ? { status: values.status } : {}),
      ...(validTo !== (system.validTo ?? null) ? { validTo } : {}),
    };
    if (Object.keys(patch).length === 0) return;
    try {
      await update.mutateAsync({ etag, patch });
      toast.notify({ tone: 'success', title: t('codeSystems.updated') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  return (
    <Card>
      <form
        onSubmit={form.handleSubmit(submit)}
        className="grid gap-5"
        noValidate
        data-testid="code-system-form"
      >
        <ProblemAlert problem={problem} hideFieldErrors />
        <div className="grid gap-4 md:grid-cols-2">
          <FormField label={t('codeSystems.fields.code')} hint={t('catalog.codeImmutableHint')}>
            <Input value={system.code} readOnly className="font-mono" />
          </FormField>
          <FormField label={t('codeSystems.fields.version')}>
            <Input value={system.version} readOnly className="font-mono" />
          </FormField>
          <FormField
            label={t('codeSystems.fields.name')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(form.formState.errors.name)}
          >
            <Input {...form.register('name')} />
          </FormField>
          <FormField label={t('codeSystems.fields.authority')}>
            <Select
              {...form.register('authority')}
              options={CODE_SYSTEM_AUTHORITIES.map((value) => ({
                value,
                label: t(`codeSystems.authorities.${value}`),
              }))}
            />
          </FormField>
          <FormField label={t('codeSystems.fields.validFrom')}>
            <Input value={system.validFrom} readOnly type="date" />
          </FormField>
          <FormField
            label={t('codeSystems.fields.validTo')}
            error={message(form.formState.errors.validTo)}
          >
            <Input {...form.register('validTo')} type="date" />
          </FormField>
          <FormField label={t('codeSystems.fields.status')}>
            <Select
              {...form.register('status')}
              options={CODE_SYSTEM_STATUSES.map((value) => ({
                value,
                label: value === 'ACTIVE' ? t('catalog.active') : t('catalog.inactive'),
              }))}
            />
          </FormField>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" {...form.register('licensed')} />
          {t('codeSystems.fields.licensed')}
        </label>
        <p className="text-fg-muted text-xs">{t('codeSystems.licensedHint')}</p>
        <div className="flex justify-end">
          <Button type="submit" loading={update.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function CodeSystemCard({ system }: { system: CodeSystem }) {
  const { t } = useTranslation();
  return (
    <Card>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
        <dt className="text-fg-muted">{t('codeSystems.fields.code')}</dt>
        <dd className="font-mono">
          {system.code} {system.version}
        </dd>
        <dt className="text-fg-muted">{t('codeSystems.fields.name')}</dt>
        <dd>{system.name}</dd>
        <dt className="text-fg-muted">{t('codeSystems.fields.authority')}</dt>
        <dd>{t(`codeSystems.authorities.${system.authority}`)}</dd>
        <dt className="text-fg-muted">{t('codeSystems.fields.validFrom')}</dt>
        <dd>
          {formatDate(system.validFrom)} – {formatDate(system.validTo) || t('common.none')}
        </dd>
        <dt className="text-fg-muted">{t('codeSystems.fields.licensed')}</dt>
        <dd>{system.licensed ? t('common.yes') : t('common.no')}</dd>
      </dl>
    </Card>
  );
}

/** The codes of the system as they stood on a date, searchable and paged. */
function CodeValuesPanel({
  codeSystemId,
  canManage,
}: {
  codeSystemId: string;
  canManage: boolean;
}) {
  const { t } = useTranslation();
  const [asOf, setAsOf] = useState(todayIso());
  const [draftQ, setDraftQ] = useState('');
  const [q, setQ] = useState('');
  const [cursor, setCursor] = useState<string | null>(null);
  const [trail, setTrail] = useState<(string | null)[]>([]);
  const [importing, setImporting] = useState(false);

  const query = useCodeValues(codeSystemId, {
    limit: PAGE_SIZE,
    ...(asOf ? { asOf } : {}),
    ...(q ? { q } : {}),
    ...(cursor ? { cursor } : {}),
  });

  function resetPaging() {
    setCursor(null);
    setTrail([]);
  }

  function search(event: FormEvent) {
    event.preventDefault();
    setQ(draftQ.trim());
    resetPaging();
  }

  const rows: CodeValue[] = query.data?.items ?? [];

  return (
    <Card>
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <h2 className="text-base font-semibold">{t('codeSystems.values.title')}</h2>
        {canManage ? (
          <Button variant="secondary" onClick={() => setImporting(true)}>
            {t('codeSystems.values.import')}
          </Button>
        ) : null}
      </div>

      <form onSubmit={search} className="mb-4 flex flex-wrap items-end gap-2" role="search">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('codeSystems.values.asOf')}</span>
          <Input
            name="asOf"
            type="date"
            value={asOf}
            onChange={(event) => {
              setAsOf(event.target.value);
              resetPaging();
            }}
            className="w-48"
            aria-describedby="code-values-as-of-hint"
          />
          <span id="code-values-as-of-hint" className="text-fg-muted text-xs">
            {t('codeSystems.values.asOfHint')}
          </span>
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('codeSystems.values.search')}</span>
          <Input
            name="q"
            value={draftQ}
            onChange={(event) => setDraftQ(event.target.value)}
            className="w-64"
          />
        </label>
        <Button type="submit" variant="secondary">
          {t('common.search')}
        </Button>
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
        <EmptyState title={t('codeSystems.values.empty')} />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="code-value-table">
            <THead>
              <TR>
                <TH>{t('codeSystems.values.columns.code')}</TH>
                <TH>{t('codeSystems.values.columns.display')}</TH>
                <TH>{t('codeSystems.values.columns.period')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((value) => (
                <TR key={value.id}>
                  <TD>
                    <code className="font-mono text-xs">{value.code}</code>
                  </TD>
                  <TD>{value.display}</TD>
                  <TD>
                    {formatDate(value.validFrom)} – {formatDate(value.validTo) || t('common.none')}
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
                onClick={() => {
                  setCursor(trail[trail.length - 1] ?? null);
                  setTrail((rest) => rest.slice(0, -1));
                }}
                disabled={trail.length === 0}
              >
                {t('organizations.prevPage')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => {
                  const next = query.data?.nextCursor;
                  if (!next) return;
                  setTrail((previous) => [...previous, cursor]);
                  setCursor(next);
                }}
                disabled={!query.data?.nextCursor}
              >
                {t('organizations.nextPage')}
              </Button>
            </div>
          </nav>
        </>
      )}

      {canManage && importing ? (
        <ImportValuesDialog codeSystemId={codeSystemId} onClose={() => setImporting(false)} />
      ) : null}
    </Card>
  );
}

/**
 * A pasted batch of codes. The call is all or nothing, so a batch with one bad row writes
 * nothing and comes back naming the row; the operator fixes the paste and tries again.
 */
function ImportValuesDialog({
  codeSystemId,
  onClose,
}: {
  codeSystemId: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const importValues = useImportCodeValues(codeSystemId);
  const [text, setText] = useState('');
  const [parseError, setParseError] = useState<string | null>(null);
  const [problem, setProblem] = useState<Problem | null>(null);
  const [result, setResult] = useState<CodeValueImportResult | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setParseError(null);
    setProblem(null);
    setResult(null);
    const parsed = parseCodeValueBatch(text);
    if ('error' in parsed) {
      setParseError(parsed.error);
      return;
    }
    try {
      const imported = await importValues.mutateAsync({
        items: parsed.rows,
        // A fresh key per attempt: each paste is its own command.
        idempotencyKey: randomId(),
      });
      setResult(imported);
      if (imported.errors.length === 0) {
        toast.notify({
          tone: 'success',
          title: t('codeSystems.values.imported', {
            created: imported.created,
            updated: imported.updated,
            skipped: imported.skipped,
          }),
        });
        onClose();
      }
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title={t('codeSystems.values.importTitle')}
    >
      <form onSubmit={submit} className="grid gap-4" noValidate data-testid="import-values-form">
        <ProblemAlert problem={problem} />
        <FormField
          label={t('codeSystems.values.title')}
          required
          requiredLabel={t('common.requiredMark')}
          hint={t('codeSystems.values.importHint')}
          error={parseError ? fieldErrorMessage(t, parseError) : undefined}
        >
          <Textarea
            value={text}
            onChange={(event) => setText(event.target.value)}
            rows={10}
            className="font-mono"
            spellCheck={false}
          />
        </FormField>

        {result ? <ImportSummary result={result} /> : null}

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose} disabled={importValues.isPending}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={importValues.isPending}>
            {t('codeSystems.values.import')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** What the batch did, row by row when the server refused it. */
function ImportSummary({ result }: { result: CodeValueImportResult }) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-2" data-testid="import-result">
      <p className="text-sm font-medium">
        {t('codeSystems.values.imported', {
          created: result.created,
          updated: result.updated,
          skipped: result.skipped,
        })}
      </p>
      {result.errors.length > 0 ? (
        <ul className="text-danger grid gap-1 text-sm">
          {result.errors.map((error, index) => (
            <li key={`${error.index}-${error.field}-${index}`}>
              <code className="font-mono text-xs">
                {error.index + 1}. {error.field}
              </code>{' '}
              {fieldErrorMessage(t, error.code, error.message)}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
