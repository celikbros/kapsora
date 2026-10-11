import type { EligibilityCheckRequest, HealthCaseType } from '@kapsora/api-client';
import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Breadcrumb,
  Button,
  Card,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  useToast,
} from '@kapsora/ui';
import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { useLayoutEffect, useRef, useState, type FormEvent } from 'react';

import { MemberPicker, type PickedMember } from '../MemberPicker';
import { problemOf } from '../problems';
import {
  useLiveEligibility,
  useProviderOrganizationId,
  useRequest,
  useServiceDefinitions,
} from '../queries';
import { useOpenCase } from './queries';
import { today } from './words';

const CASE_TYPES: HealthCaseType[] = ['OUTPATIENT', 'INPATIENT', 'CHRONIC', 'MATERNITY', 'OTHER'];

/**
 * Opening a case: the person and the plan they are covered under, and what kind of care
 * this is. From a request the first two are already known and the form says so. On its
 * own, the desk names the member the way it does everywhere else — by name — and the
 * enrollment comes from the eligibility check, the one thing a provider may ask about a
 * member's coverage: it names the service the care is about, and the check answers with
 * the enrollment, or with the candidates when there is more than one to choose from.
 */
export function CaseOpenPage() {
  const search = useSearch({ from: '/app/cases/new' });
  const requestId = search.requestId ?? '';
  return <CaseOpenForm key={requestId} requestId={requestId} />;
}

function CaseOpenForm({ requestId }: { requestId: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const providerOrganizationId = useProviderOrganizationId();
  const request = useRequest(requestId);
  const definitions = useServiceDefinitions();
  const open = useOpenCase();
  const active = useRef(true);
  const submitting = useRef(false);

  useLayoutEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);

  const [picked, setPicked] = useState<PickedMember | null>(null);
  const [serviceDefinitionId, setServiceDefinitionId] = useState('');
  const [chosenCandidate, setChosenCandidate] = useState('');
  const [caseType, setCaseType] = useState<HealthCaseType>('OUTPATIENT');
  const [openedAt, setOpenedAt] = useState(today());

  // A request already names the person and the enrollment; the form takes them as read.
  const linked = requestId !== '';
  const requestReadReady =
    linked &&
    !request.isPending &&
    !request.isFetching &&
    !request.error &&
    request.data?.data.id === requestId;
  const fromRequest = requestReadReady ? request.data!.data : null;
  const member: PickedMember | null = fromRequest
    ? { id: fromRequest.personId, displayName: fromRequest.personDisplayName }
    : picked;

  const checkBody: EligibilityCheckRequest | null =
    !linked && member && serviceDefinitionId && openedAt
      ? {
          personId: member.id,
          serviceDate: openedAt,
          serviceItems: [{ serviceDefinitionId, quantity: '1' }],
          ...(chosenCandidate ? { enrollmentId: chosenCandidate } : {}),
          ...(providerOrganizationId ? { providerOrganizationId } : {}),
        }
      : null;
  const check = useLiveEligibility(checkBody);
  const candidates = check.data?.enrollmentCandidates ?? [];
  const chosenIsCandidate = candidates.some((c) => c.enrollmentId === chosenCandidate);
  const eligibilityReady =
    !linked &&
    checkBody !== null &&
    !check.isPending &&
    !check.isFetching &&
    !check.error &&
    !!check.data;
  const enrollmentId = fromRequest
    ? fromRequest.enrollmentId
    : eligibilityReady
      ? (check.data?.enrollmentId ?? (chosenIsCandidate ? chosenCandidate : ''))
      : '';
  const ready =
    member !== null &&
    enrollmentId !== '' &&
    openedAt !== '' &&
    !open.isPending &&
    (linked ? requestReadReady : eligibilityReady);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ready || !member || !active.current || submitting.current) return;
    submitting.current = true;
    try {
      const created = await open.mutateAsync({
        personId: member.id,
        enrollmentId,
        caseType,
        openedAt: new Date(openedAt).toISOString(),
        ...(providerOrganizationId ? { providerOrganizationId } : {}),
        ...(fromRequest
          ? { serviceRequestId: fromRequest.id, programId: fromRequest.programId }
          : {}),
      });
      if (active.current) {
        toast.notify({ tone: 'success', title: t('health.openCase.created') });
        await navigate({ to: '/cases/$caseId', params: { caseId: created.data.id } });
      }
    } catch {
      // The problem is rendered below the form.
    } finally {
      submitting.current = false;
    }
  }

  return (
    <>
      <PageHeader
        title={t('health.openCase.title')}
        description={t('health.openCase.intro')}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('health.cases.title'),
                render: (label) => <Link to="/cases">{label}</Link>,
              },
              { label: t('health.openCase.title') },
            ]}
          />
        }
      />
      <Card className="max-w-2xl">
        {linked && (request.isPending || request.isFetching) ? (
          <p
            className="text-fg-muted flex items-center gap-2 text-sm"
            role="status"
            aria-busy="true"
          >
            <Spinner /> {t('common.loading')}
          </p>
        ) : linked && (request.error || !requestReadReady) ? (
          <ProblemAlert
            problem={problemOf(request.error)}
            actions={
              <Button size="sm" variant="secondary" onClick={() => void request.refetch()}>
                {t('common.retry')}
              </Button>
            }
          />
        ) : (
          <form onSubmit={(e) => void submit(e)} className="grid gap-4" noValidate>
            {fromRequest ? (
              <p className="bg-info-soft text-fg rounded-md p-3 text-sm" role="status">
                {t('health.openCase.fromRequest', { reference: fromRequest.reference })}
              </p>
            ) : null}
            {fromRequest ? (
              <FormField label={t('health.openCase.member')}>
                <Input name="member" value={fromRequest.personDisplayName} readOnly />
              </FormField>
            ) : (
              <MemberPicker
                member={picked}
                onPick={(m) => {
                  setPicked(m);
                  setChosenCandidate('');
                }}
              />
            )}
            <FormField
              label={t('health.openCase.openedAt')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="openedAt"
                type="date"
                value={openedAt}
                onChange={(e) => setOpenedAt(e.target.value)}
              />
            </FormField>
            {member && !fromRequest ? (
              <FormField
                label={t('health.openCase.service')}
                hint={t('health.openCase.serviceHint')}
                required
                requiredLabel={t('common.requiredMark')}
              >
                <Select
                  name="serviceDefinitionId"
                  value={serviceDefinitionId}
                  onChange={(e) => {
                    setServiceDefinitionId(e.target.value);
                    setChosenCandidate('');
                  }}
                  placeholder={t('common.none')}
                  options={(definitions.data ?? []).map((d) => ({
                    value: d.value,
                    label: d.label,
                  }))}
                />
              </FormField>
            ) : null}
            {checkBody && check.isPending ? (
              <p className="text-fg-muted text-sm" aria-live="polite">
                {t('health.openCase.checking')}
              </p>
            ) : null}
            {checkBody && check.error ? (
              <ProblemAlert
                problem={problemOf(check.error)}
                actions={
                  <Button size="sm" variant="secondary" onClick={() => void check.refetch()}>
                    {t('common.retry')}
                  </Button>
                }
              />
            ) : null}
            {candidates.length > 0 ? (
              <FormField
                label={t('health.openCase.enrollment')}
                hint={t('health.openCase.candidatesHint')}
                required
                requiredLabel={t('common.requiredMark')}
              >
                <Select
                  name="enrollmentId"
                  value={chosenCandidate}
                  onChange={(e) => setChosenCandidate(e.target.value)}
                  placeholder={t('common.none')}
                  options={candidates.map((c) => ({
                    value: c.enrollmentId,
                    label: `${c.planName} (${c.planCode}) · ${formatDate(c.validFrom)}${
                      c.validTo ? ` – ${formatDate(c.validTo)}` : ''
                    }`,
                  }))}
                />
              </FormField>
            ) : null}
            {checkBody && check.data && !check.data.enrollmentId && candidates.length === 0 ? (
              <p role="status" className="bg-warning-soft text-fg rounded-md p-3 text-sm">
                {t('health.openCase.noEnrollment')}
              </p>
            ) : null}
            <FormField
              label={t('health.openCase.type')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Select
                name="caseType"
                value={caseType}
                onChange={(e) => setCaseType(e.target.value as HealthCaseType)}
                options={CASE_TYPES.map((type) => ({
                  value: type,
                  label: t(`health.caseType.${type}`),
                }))}
              />
            </FormField>
            <ProblemAlert problem={open.error ? problemOf(open.error) : null} />
            {ready ? (
              <div>
                <Button type="submit" loading={open.isPending}>
                  {t('health.openCase.submit')}
                </Button>
              </div>
            ) : null}
          </form>
        )}
      </Card>
    </>
  );
}
