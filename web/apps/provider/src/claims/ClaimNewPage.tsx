import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Breadcrumb,
  Button,
  Card,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Spinner,
  useToast,
} from '@kapsora/ui';
import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { useCase, usePersonName } from '../health/queries';
import { today } from '../health/words';
import { problemOf } from '../problems';
import { useProviderOrganizationId } from '../queries';
import { LinesEditor, emptyLine, linesValid, toNewLines, type DraftLine } from './LinesEditor';
import { useCreateClaim } from './queries';

/**
 * A new claim, from a case. The case is what tells the claim whose it is and under which
 * plan and program: a provider may not list a member's enrollments or programs, so a claim
 * with no case would have nothing to stand on. Nothing here is priced: the draft carries
 * what the provider asks and the server answers on submit.
 */
export function ClaimNewPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const search = useSearch({ from: '/app/claims/new' });
  const providerOrganizationId = useProviderOrganizationId();
  const fromCase = useCase(search.caseId ?? '');
  const record = fromCase.data?.data ?? null;
  const personName = usePersonName(record?.personId);
  const create = useCreateClaim();

  const [from, setFrom] = useState(today());
  const [to, setTo] = useState(today());
  const [lines, setLines] = useState<DraftLine[]>([emptyLine()]);

  const ready =
    record !== null &&
    providerOrganizationId !== null &&
    from !== '' &&
    to !== '' &&
    linesValid(lines);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ready || !record || !providerOrganizationId) return;
    try {
      const created = await create.mutateAsync({
        personId: record.personId,
        enrollmentId: record.enrollmentId,
        programId: record.programId,
        providerOrganizationId,
        serviceDateFrom: from,
        serviceDateTo: to,
        channel: 'PROVIDER_PORTAL',
        caseId: record.id,
        lines: toNewLines(lines),
      });
      toast.notify({ tone: 'success', title: t('claims.create.created') });
      await navigate({ to: '/claims/$claimId', params: { claimId: created.data.id } });
    } catch {
      // Rendered below.
    }
  }

  const header = (
    <PageHeader
      title={t('claims.create.title')}
      description={t('claims.create.intro')}
      breadcrumb={
        <Breadcrumb
          items={[
            { label: t('claims.title'), render: (label) => <Link to="/claims">{label}</Link> },
            { label: t('claims.create.title') },
          ]}
        />
      }
    />
  );

  if (!search.caseId) {
    return (
      <>
        {header}
        <EmptyState
          title={t('claims.create.needsCase')}
          description={t('claims.create.needsCaseHint')}
          action={
            <Link to="/cases">
              <Button size="sm">{t('claims.create.chooseCase')}</Button>
            </Link>
          }
        />
      </>
    );
  }
  if (fromCase.isPending) {
    return (
      <>
        {header}
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      </>
    );
  }
  if (fromCase.error || !record) {
    return (
      <>
        {header}
        <ProblemAlert problem={problemOf(fromCase.error)} />
      </>
    );
  }

  return (
    <>
      {header}
      <form onSubmit={(e) => void submit(e)} className="grid gap-4" noValidate>
        <Card>
          <div className="grid gap-4 md:grid-cols-2">
            <p className="bg-info-soft text-fg rounded-md p-3 text-sm md:col-span-2" role="status">
              {t('claims.create.fromCase')}:{' '}
              <Link
                to="/cases/$caseId"
                params={{ caseId: record.id }}
                className="underline underline-offset-2"
              >
                {t(`health.caseType.${record.caseType}`)} · {formatDate(record.openedAt)}
              </Link>
            </p>
            <FormField label={t('claims.create.member')}>
              <Input
                name="member"
                value={personName === undefined ? '…' : (personName ?? '—')}
                readOnly
              />
            </FormField>
            <div className="hidden md:block" />
            <FormField
              label={t('claims.create.serviceDateFrom')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="serviceDateFrom"
                type="date"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
              />
            </FormField>
            <FormField
              label={t('claims.create.serviceDateTo')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="serviceDateTo"
                type="date"
                value={to}
                onChange={(e) => setTo(e.target.value)}
              />
            </FormField>
          </div>
        </Card>
        <Card>
          <h2 className="text-base font-semibold">{t('claims.lines.title')}</h2>
          <p className="text-fg-muted mb-3 mt-1 text-sm">{t('claims.lines.intro')}</p>
          <LinesEditor lines={lines} onChange={setLines} />
        </Card>
        <ProblemAlert problem={create.error ? problemOf(create.error) : null} />
        {ready ? (
          <div>
            <Button type="submit" loading={create.isPending}>
              {t('claims.create.submit')}
            </Button>
          </div>
        ) : null}
      </form>
    </>
  );
}
