import type {
  Diagnosis,
  Encounter,
  EncounterType,
  HealthCase,
  InpatientStay,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
  Dialog,
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
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { useEffect, useState, type FormEvent } from 'react';

import { useClaimsOfCase } from '../claims/queries';
import { DocumentsPanel } from '../documents';
import { problemOf } from '../problems';
import { useProviderOrganizationId } from '../queries';
import { DiagnosisEditor } from './DiagnosisEditor';
import {
  useAdmit,
  useCase,
  useCloseCase,
  useCreateEncounter,
  useCreateReport,
  useDiagnoses,
  usePersonName,
  useReportsOfCase,
  useStaysOfCase,
} from './queries';
import {
  caseTone,
  claimTone,
  daysFromToday,
  nowLocal,
  qty,
  reportTone,
  stayTone,
  toInstant,
  today,
} from './words';

const ENCOUNTER_TYPES: EncounterType[] = ['OUTPATIENT', 'INPATIENT', 'EMERGENCY', 'TELEHEALTH'];
const OPEN_STAY = new Set<InpatientStay['status']>(['REQUESTED', 'AUTHORIZED', 'ADMITTED']);

/**
 * The primary diagnosis of one encounter, read only on the clinical projection: on the
 * financial one the list would answer 403, so it is not asked for and the cell says so.
 */
function EncounterDiagnoses({
  encounterId,
  clinical,
  onLoaded,
}: {
  encounterId: string;
  clinical: boolean;
  onLoaded: (encounterId: string, items: Diagnosis[]) => void;
}) {
  const { t } = useTranslation();
  const query = useDiagnoses(encounterId, clinical);
  const items = query.data;
  useEffect(() => {
    if (items) onLoaded(encounterId, items);
  }, [items, encounterId, onLoaded]);
  if (query.isPending) return <span className="text-fg-muted">…</span>;
  const primary = items?.find((d) => d.diagnosisType === 'PRIMARY') ?? items?.[0];
  if (!primary) return <span className="text-fg-muted">{t('health.encounters.noDiagnosis')}</span>;
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <span className="font-mono">{primary.code}</span>
      <span>{primary.display}</span>
      {primary.sensitive ? <Badge tone="warning">{t('health.diagnoses.sensitive')}</Badge> : null}
    </span>
  );
}

function EncounterForm({ caseId, onDone }: { caseId: string; onDone: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const create = useCreateEncounter(caseId);
  const [type, setType] = useState<EncounterType>('OUTPATIENT');
  const [startedAt, setStartedAt] = useState(nowLocal());
  const [endedAt, setEndedAt] = useState('');
  const [branch, setBranch] = useState('');
  const [notes, setNotes] = useState('');

  async function submit(event: FormEvent) {
    event.preventDefault();
    try {
      await create.mutateAsync({
        encounterType: type,
        startedAt: toInstant(startedAt),
        ...(endedAt ? { endedAt: toInstant(endedAt) } : {}),
        ...(branch.trim() ? { branchCode: branch.trim().toUpperCase() } : {}),
        ...(notes.trim() ? { notesClinical: notes.trim() } : {}),
      });
      toast.notify({ tone: 'success', title: t('health.encounters.created') });
      onDone();
    } catch {
      // Rendered below.
    }
  }

  return (
    <form
      onSubmit={(e) => void submit(e)}
      className="mt-4 grid gap-3 border-t pt-4 md:grid-cols-2"
      noValidate
      data-testid="encounter-form"
    >
      <FormField
        label={t('health.encounters.form.type')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Select
          name="encounterType"
          value={type}
          onChange={(e) => setType(e.target.value as EncounterType)}
          options={ENCOUNTER_TYPES.map((x) => ({
            value: x,
            label: t(`health.encounters.type.${x}`),
          }))}
        />
      </FormField>
      <FormField
        label={t('health.encounters.form.branch')}
        hint={t('health.encounters.form.branchHint')}
      >
        <Input
          name="branchCode"
          value={branch}
          onChange={(e) => setBranch(e.target.value)}
          className="font-mono uppercase"
          autoComplete="off"
          spellCheck={false}
        />
      </FormField>
      <FormField
        label={t('health.encounters.form.startedAt')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="startedAt"
          type="datetime-local"
          value={startedAt}
          onChange={(e) => setStartedAt(e.target.value)}
        />
      </FormField>
      <FormField
        label={t('health.encounters.form.endedAt')}
        hint={t('health.encounters.form.ongoing')}
      >
        <Input
          name="endedAt"
          type="datetime-local"
          value={endedAt}
          onChange={(e) => setEndedAt(e.target.value)}
        />
      </FormField>
      <div className="md:col-span-2">
        <FormField label={t('health.encounters.form.notes')}>
          <Textarea
            name="notesClinical"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            rows={2}
          />
        </FormField>
      </div>
      <div className="md:col-span-2">
        <ProblemAlert problem={create.error ? problemOf(create.error) : null} />
      </div>
      <div className="flex justify-end gap-2 md:col-span-2">
        <Button variant="secondary" size="sm" onClick={onDone}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" size="sm" loading={create.isPending} disabled={!startedAt}>
          {t('health.encounters.form.submit')}
        </Button>
      </div>
    </form>
  );
}

/**
 * A report is born with what the reviewer will judge it by: its type and the dates it is
 * valid for. The rest — the services, the summary, the file — is written on the report
 * itself; asking for them here would be asking twice.
 */
function ReportCreateForm({
  record,
  onDone,
}: {
  record: HealthCase;
  onDone: (reportId: string) => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const create = useCreateReport();
  const providerOrganizationId = useProviderOrganizationId();
  const [type, setType] = useState('');
  const [issuedAt, setIssuedAt] = useState(today());
  const [validFrom, setValidFrom] = useState(today());
  const [validTo, setValidTo] = useState(daysFromToday(30));
  const ok = type.trim() !== '' && issuedAt !== '' && validFrom !== '' && validTo !== '';

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ok) return;
    try {
      const created = await create.mutateAsync({
        personId: record.personId,
        caseId: record.id,
        reportType: type.trim().toUpperCase(),
        issuedAt,
        validFrom,
        validTo,
        ...(providerOrganizationId
          ? { issuingProviderOrganizationId: providerOrganizationId }
          : {}),
      });
      toast.notify({ tone: 'success', title: t('health.reports.created') });
      onDone(created.data.id);
    } catch {
      // Rendered below.
    }
  }

  return (
    <form
      onSubmit={(e) => void submit(e)}
      className="mt-4 grid gap-3 border-t pt-4 md:grid-cols-4"
      noValidate
      data-testid="report-create-form"
    >
      <p className="text-fg-muted text-sm md:col-span-4">{t('health.reports.createForm.intro')}</p>
      <FormField
        label={t('health.reports.header.type')}
        hint={t('health.reports.header.typeHint')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="reportType"
          value={type}
          onChange={(e) => setType(e.target.value)}
          className="font-mono uppercase"
          autoComplete="off"
          spellCheck={false}
        />
      </FormField>
      <FormField
        label={t('health.reports.header.issuedAt')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="issuedAt"
          type="date"
          value={issuedAt}
          onChange={(e) => setIssuedAt(e.target.value)}
        />
      </FormField>
      <FormField
        label={t('health.reports.header.validFrom')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="validFrom"
          type="date"
          value={validFrom}
          onChange={(e) => setValidFrom(e.target.value)}
        />
      </FormField>
      <FormField
        label={t('health.reports.header.validTo')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="validTo"
          type="date"
          value={validTo}
          onChange={(e) => setValidTo(e.target.value)}
        />
      </FormField>
      <div className="md:col-span-4">
        <ProblemAlert problem={create.error ? problemOf(create.error) : null} />
      </div>
      <div className="flex justify-end md:col-span-4">
        <Button type="submit" size="sm" loading={create.isPending} disabled={!ok}>
          {t('health.reports.createForm.submit')}
        </Button>
      </div>
    </form>
  );
}

function AdmitForm({
  record,
  diagnoses,
  onDone,
}: {
  record: HealthCase;
  diagnoses: Diagnosis[];
  onDone: (stayId: string) => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const admit = useAdmit();
  const providerOrganizationId = useProviderOrganizationId();
  const [admissionAt, setAdmissionAt] = useState(nowLocal());
  const [days, setDays] = useState('3');
  const [diagnosisId, setDiagnosisId] = useState('');
  const n = Number(days);
  const ok = admissionAt !== '' && Number.isInteger(n) && n > 0 && providerOrganizationId !== null;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ok || !providerOrganizationId) return;
    try {
      const created = await admit.mutateAsync({
        caseId: record.id,
        providerOrganizationId,
        admissionAt: toInstant(admissionAt),
        estimatedDays: n,
        ...(diagnosisId ? { admissionDiagnosisId: diagnosisId } : {}),
      });
      toast.notify({ tone: 'success', title: t('health.stays.form.created') });
      onDone(created.data.id);
    } catch {
      // Rendered below.
    }
  }

  return (
    <form
      onSubmit={(e) => void submit(e)}
      className="mt-4 grid gap-3 border-t pt-4 md:grid-cols-3"
      noValidate
      data-testid="admit-form"
    >
      <p className="text-fg-muted text-sm md:col-span-3">{t('health.stays.form.intro')}</p>
      <FormField
        label={t('health.stays.form.admissionAt')}
        hint={t('health.stays.form.windowHint')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="admissionAt"
          type="datetime-local"
          value={admissionAt}
          onChange={(e) => setAdmissionAt(e.target.value)}
        />
      </FormField>
      <FormField
        label={t('health.stays.form.estimatedDays')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Input
          name="estimatedDays"
          inputMode="numeric"
          value={days}
          onChange={(e) => setDays(e.target.value)}
          className="w-24 text-right font-mono"
        />
      </FormField>
      <FormField
        label={t('health.stays.form.diagnosis')}
        hint={t('health.stays.form.diagnosisHint')}
      >
        <Select
          name="admissionDiagnosisId"
          value={diagnosisId}
          onChange={(e) => setDiagnosisId(e.target.value)}
          placeholder={t('common.none')}
          options={diagnoses.map((d) => ({ value: d.id, label: `${d.code} · ${d.display}` }))}
        />
      </FormField>
      <div className="md:col-span-3">
        <ProblemAlert problem={admit.error ? problemOf(admit.error) : null} />
      </div>
      <div className="flex justify-end md:col-span-3">
        <Button type="submit" size="sm" loading={admit.isPending} disabled={!ok}>
          {t('health.stays.form.submit')}
        </Button>
      </div>
    </form>
  );
}

/**
 * The case as a story: the encounters and what was found, the documents, and then the
 * report, the admission and the claim as its chapters. The server decides which half a
 * caller sees; this page renders what arrived and says, once, when the clinical half did
 * not.
 */
export function CasePage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const { caseId } = useParams({ from: '/app/cases/$caseId' });
  const query = useCase(caseId);
  const canManage = usePermission('health.case.manage');
  const canReport = usePermission('health.medical_report.manage');
  const canClaim = usePermission('claim.create');
  const reports = useReportsOfCase(caseId);
  const stays = useStaysOfCase(caseId);
  const claims = useClaimsOfCase(caseId);
  const close = useCloseCase(caseId);
  const personName = usePersonName(query.data?.data.personId);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [admitting, setAdmitting] = useState(false);
  const [creatingReport, setCreatingReport] = useState(false);
  const [closing, setClosing] = useState(false);
  const [byEncounter, setByEncounter] = useState<Record<string, Diagnosis[]>>({});
  const onLoaded = (encounterId: string, items: Diagnosis[]) =>
    setByEncounter((all) => (all[encounterId] === items ? all : { ...all, [encounterId]: items }));

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
  const record = query.data.data;
  const clinical = record.projection === 'CLINICAL';
  const open = record.status === 'OPEN';
  const everyEncounterEnded = record.encounters.every((e) => Boolean(e.endedAt));
  const diagnoses = Object.values(byEncounter).flat();
  const openStay = (stays.data?.items ?? []).find((s) => OPEN_STAY.has(s.status));
  // The chapter headers carry the chapter's state: the newest report's status, the open
  // stay's status, how many claims are still moving. A case with an open stay and a
  // returned claim must not look like an empty one from the first viewport.
  const latestReport = [...(reports.data?.items ?? [])].sort((a, b) =>
    b.createdAt.localeCompare(a.createdAt),
  )[0];
  const openClaims = (claims.data?.items ?? []).filter(
    (c) =>
      !['APPROVED', 'PARTIALLY_APPROVED', 'REJECTED', 'CANCELLED', 'SETTLED'].includes(c.status),
  ).length;

  async function closeCase() {
    try {
      await close.mutateAsync({ rowVersion: record.rowVersion });
      toast.notify({ tone: 'success', title: t('health.case.closed') });
      setClosing(false);
    } catch {
      // The dialog shows the problem.
    }
  }

  const title = personName === undefined ? '…' : (personName ?? t('health.case.title'));

  return (
    <>
      <PageHeader
        title={title}
        description={`${t(`health.caseType.${record.caseType}`)} · ${t('health.case.openedAt')} ${formatDate(record.openedAt)}`}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('health.cases.title'),
                render: (label) => <Link to="/cases">{label}</Link>,
              },
              { label: title },
            ]}
          />
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={caseTone(record.status)} data-testid="case-status">
              {t(`health.caseStatus.${record.status}`)}
            </Badge>
            {clinical && record.sensitivity === 'SENSITIVE' ? (
              <Badge tone="warning">{t('health.sensitivity.SENSITIVE')}</Badge>
            ) : null}
            {open && canManage && everyEncounterEnded && record.encounters.length > 0 ? (
              <Button size="sm" variant="secondary" onClick={() => setClosing(true)}>
                {t('health.case.close')}
              </Button>
            ) : null}
          </div>
        }
      />

      {!clinical ? (
        <p
          role="status"
          className="bg-info-soft text-fg mb-4 rounded-md p-3 text-sm"
          data-testid="financial-note"
        >
          {t('health.case.financialNote')}
        </p>
      ) : record.sensitivity === 'SENSITIVE' ? (
        <p role="status" className="bg-warning-soft text-fg mb-4 rounded-md p-3 text-sm">
          {t('health.case.sensitiveNote')}
        </p>
      ) : null}

      <div className="grid gap-4">
        <Card>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-base font-semibold">{t('health.case.sections.encounters')}</h2>
            {open && canManage && !adding ? (
              <Button size="sm" variant="secondary" onClick={() => setAdding(true)}>
                {t('health.encounters.add')}
              </Button>
            ) : null}
          </div>
          {record.encounters.length === 0 ? (
            <p className="text-fg-muted mt-2 text-sm">{t('health.encounters.empty')}</p>
          ) : (
            <div className="mt-3">
              <Table data-testid="encounter-table">
                <THead>
                  <TR>
                    <TH>{t('health.encounters.columns.startedAt')}</TH>
                    <TH>{t('health.encounters.columns.endedAt')}</TH>
                    <TH>{t('health.encounters.columns.type')}</TH>
                    {clinical ? <TH>{t('health.encounters.columns.branch')}</TH> : null}
                    {clinical ? <TH>{t('health.encounters.columns.diagnosis')}</TH> : null}
                    {clinical && canManage && open ? (
                      <TH>
                        <span className="sr-only">{t('common.actions')}</span>
                      </TH>
                    ) : null}
                  </TR>
                </THead>
                <TBody>
                  {record.encounters.map((encounter: Encounter) => (
                    <EncounterRows
                      key={encounter.id}
                      encounter={encounter}
                      clinical={clinical}
                      editable={clinical && canManage && open}
                      editing={editing === encounter.id}
                      onEdit={() => setEditing(editing === encounter.id ? null : encounter.id)}
                      caseId={record.id}
                      current={byEncounter[encounter.id] ?? []}
                      onLoaded={onLoaded}
                    />
                  ))}
                </TBody>
              </Table>
            </div>
          )}
          {adding ? <EncounterForm caseId={record.id} onDone={() => setAdding(false)} /> : null}
        </Card>

        <Card>
          <h2 className="text-base font-semibold">{t('health.case.sections.documents')}</h2>
          <DocumentsPanel aggregateType="HEALTH_CASE" aggregateId={record.id} readOnly={!open} />
        </Card>

        <Card>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="flex flex-wrap items-center gap-2 text-base font-semibold">
              {t('health.case.sections.reports')}
              {latestReport ? (
                <Badge tone={reportTone(latestReport.status)}>
                  {t(`health.reports.status.${latestReport.status}`)}
                </Badge>
              ) : null}
            </h2>
            {open && canReport && clinical && !creatingReport ? (
              <Button size="sm" variant="secondary" onClick={() => setCreatingReport(true)}>
                {t('health.reports.create')}
              </Button>
            ) : null}
          </div>
          {reports.isPending ? (
            <p className="text-fg-muted mt-2 text-sm">{t('common.loading')}</p>
          ) : (reports.data?.items.length ?? 0) === 0 ? (
            <p className="text-fg-muted mt-2 text-sm">{t('health.reports.empty')}</p>
          ) : (
            <div className="mt-3">
              <Table data-testid="case-reports">
                <THead>
                  <TR>
                    <TH>{t('health.reports.columns.reference')}</TH>
                    <TH>{t('health.reports.columns.status')}</TH>
                    <TH>{t('health.reports.columns.version')}</TH>
                    <TH>{t('health.reports.columns.valid')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {reports.data!.items.map((r) => (
                    <TR key={r.id}>
                      <TD>
                        <Link
                          to="/reports/$reportId"
                          params={{ reportId: r.id }}
                          className="font-mono underline-offset-2 hover:underline"
                        >
                          {r.reference}
                        </Link>
                      </TD>
                      <TD>
                        <Badge tone={reportTone(r.status)}>
                          {t(`health.reports.status.${r.status}`)}
                        </Badge>
                      </TD>
                      <TD className="font-mono">v{r.versionNo}</TD>
                      <TD>
                        {formatDate(r.validFrom)} – {formatDate(r.validTo)}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
          {creatingReport ? (
            <ReportCreateForm
              record={record}
              onDone={(reportId) => {
                setCreatingReport(false);
                void navigate({ to: '/reports/$reportId', params: { reportId } });
              }}
            />
          ) : null}
        </Card>

        <Card>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="flex flex-wrap items-center gap-2 text-base font-semibold">
              {t('health.case.sections.stays')}
              {openStay ? (
                <Badge tone={stayTone(openStay.status)}>
                  {t(`health.stays.status.${openStay.status}`)}
                </Badge>
              ) : null}
            </h2>
            {openStay ? (
              <Link to="/stays/$stayId" params={{ stayId: openStay.id }}>
                <Button size="sm" variant="secondary">
                  {t('health.case.sections.stayOpen')}
                </Button>
              </Link>
            ) : open && canManage && !admitting && !stays.isPending ? (
              <Button size="sm" variant="secondary" onClick={() => setAdmitting(true)}>
                {t('health.stays.admit')}
              </Button>
            ) : null}
          </div>
          {stays.isPending ? (
            <p className="text-fg-muted mt-2 text-sm">{t('common.loading')}</p>
          ) : (stays.data?.items.length ?? 0) === 0 ? (
            <p className="text-fg-muted mt-2 text-sm">{t('health.stays.empty')}</p>
          ) : (
            <div className="mt-3">
              <Table data-testid="case-stays">
                <THead>
                  <TR>
                    <TH>{t('health.stays.columns.admissionAt')}</TH>
                    <TH>{t('health.stays.columns.status')}</TH>
                    <TH className="text-right">{t('health.stays.columns.days')}</TH>
                    <TH>{t('health.stays.columns.expected')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {stays.data!.items.map((s) => (
                    <TR key={s.id} data-status={s.status}>
                      <TD>
                        <Link
                          to="/stays/$stayId"
                          params={{ stayId: s.id }}
                          className="underline-offset-2 hover:underline"
                        >
                          {formatDateTime(s.admissionAt)}
                        </Link>
                      </TD>
                      <TD>
                        <Badge tone={stayTone(s.status)}>
                          {t(`health.stays.status.${s.status}`)}
                        </Badge>
                      </TD>
                      <TD className="text-right font-mono">
                        {qty(s.authorizedDays)} / {qty(s.actualDays)}
                      </TD>
                      <TD>{formatDate(s.expectedDischargeAt)}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
          {admitting ? (
            <AdmitForm
              record={record}
              diagnoses={diagnoses}
              onDone={(stayId) => {
                setAdmitting(false);
                void navigate({ to: '/stays/$stayId', params: { stayId } });
              }}
            />
          ) : null}
        </Card>

        <Card>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="flex flex-wrap items-center gap-2 text-base font-semibold">
              {t('health.case.sections.claims')}
              {openClaims > 0 ? (
                <Badge tone="info">{t('health.case.sections.claimsOpen', { n: openClaims })}</Badge>
              ) : null}
            </h2>
            {canClaim ? (
              <Link to="/claims/new" search={{ caseId: record.id }}>
                <Button size="sm" variant="secondary">
                  {t('claims.new')}
                </Button>
              </Link>
            ) : null}
          </div>
          {claims.isPending ? (
            <p className="text-fg-muted mt-2 text-sm">{t('common.loading')}</p>
          ) : (claims.data?.items.length ?? 0) === 0 ? (
            <p className="text-fg-muted mt-2 text-sm">{t('claims.empty')}</p>
          ) : (
            <div className="mt-3">
              <Table data-testid="case-claims">
                <THead>
                  <TR>
                    <TH>{t('claims.columns.reference')}</TH>
                    <TH>{t('claims.columns.status')}</TH>
                    <TH>{t('claims.columns.serviceDates')}</TH>
                    <TH className="text-right">{t('claims.columns.lines')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {claims.data!.items.map((c) => (
                    <TR key={c.id}>
                      <TD>
                        <Link
                          to="/claims/$claimId"
                          params={{ claimId: c.id }}
                          className="font-mono underline-offset-2 hover:underline"
                        >
                          {c.reference}
                        </Link>
                      </TD>
                      <TD>
                        <Badge tone={claimTone(c.status)}>{t(`claims.status.${c.status}`)}</Badge>
                      </TD>
                      <TD>
                        {formatDate(c.serviceDateFrom)} – {formatDate(c.serviceDateTo)}
                      </TD>
                      <TD className="text-right font-mono">{c.lines.length}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
        </Card>
      </div>

      <Dialog
        open={closing}
        onOpenChange={setClosing}
        title={t('health.case.close')}
        description={t('health.case.closeConfirm')}
        actions={
          <>
            <Button variant="secondary" onClick={() => setClosing(false)}>
              {t('common.cancel')}
            </Button>
            <Button loading={close.isPending} onClick={() => void closeCase()}>
              {t('health.case.close')}
            </Button>
          </>
        }
      >
        <ProblemAlert problem={close.error ? problemOf(close.error) : null} />
      </Dialog>
    </>
  );
}

/** One encounter as a row, and — when it is being edited — its diagnosis editor as the row below. */
function EncounterRows({
  encounter,
  clinical,
  editable,
  editing,
  onEdit,
  caseId,
  current,
  onLoaded,
}: {
  encounter: Encounter;
  clinical: boolean;
  editable: boolean;
  editing: boolean;
  onEdit: () => void;
  caseId: string;
  current: Diagnosis[];
  onLoaded: (encounterId: string, items: Diagnosis[]) => void;
}) {
  const { t } = useTranslation();
  const columns = 3 + (clinical ? 2 : 0) + (editable ? 1 : 0);
  return (
    <>
      <TR data-testid="encounter-row">
        <TD>{formatDateTime(encounter.startedAt)}</TD>
        <TD>
          {encounter.endedAt
            ? formatDateTime(encounter.endedAt)
            : t('health.encounters.form.ongoing')}
        </TD>
        <TD>{t(`health.encounters.type.${encounter.encounterType}`)}</TD>
        {clinical ? <TD className="font-mono">{encounter.branchCode ?? '—'}</TD> : null}
        {clinical ? (
          <TD>
            <EncounterDiagnoses
              encounterId={encounter.id}
              clinical={clinical}
              onLoaded={onLoaded}
            />
          </TD>
        ) : null}
        {editable ? (
          <TD>
            <Button size="sm" variant="ghost" onClick={onEdit} aria-expanded={editing}>
              {t('health.encounters.editDiagnoses')}
            </Button>
          </TD>
        ) : null}
      </TR>
      {editing ? (
        <TR>
          <TD colSpan={columns}>
            <DiagnosisEditor
              caseId={caseId}
              encounterId={encounter.id}
              current={current}
              onDone={onEdit}
            />
          </TD>
        </TR>
      ) : null}
    </>
  );
}
