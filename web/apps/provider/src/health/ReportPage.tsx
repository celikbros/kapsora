import type { MedicalReport, MedicalReportServiceInput } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, formatDateTime, formatMoney, useTranslation } from '@kapsora/i18n';
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
  Textarea,
  useToast,
} from '@kapsora/ui';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { DocumentsPanel } from '../documents';
import { problemOf } from '../problems';
import { useServiceDefinitions } from '../queries';
import {
  usePersonName,
  useCreateReport,
  useReport,
  useReportChain,
  useReportCommands,
} from './queries';
import { qty, reportTone, today } from './words';

function HeaderForm({ report, onSaved }: { report: MedicalReport; onSaved: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const commands = useReportCommands(report.id);
  const [type, setType] = useState(report.reportType ?? '');
  const [subtype, setSubtype] = useState(report.reportSubtype ?? '');
  const [issuedAt, setIssuedAt] = useState(report.issuedAt.slice(0, 10));
  const [validFrom, setValidFrom] = useState(report.validFrom.slice(0, 10));
  const [validTo, setValidTo] = useState(report.validTo.slice(0, 10));
  const [summary, setSummary] = useState(report.clinicalSummary ?? '');
  const ok = type.trim() !== '' && issuedAt !== '' && validFrom !== '' && validTo !== '';

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ok) return;
    try {
      await commands.patch.mutateAsync({
        rowVersion: report.rowVersion,
        body: {
          reportType: type.trim().toUpperCase(),
          issuedAt,
          validFrom,
          validTo,
          reportSubtype: subtype.trim() ? subtype.trim().toUpperCase() : null,
          clinicalSummary: summary.trim() ? summary.trim() : null,
          caseId: report.caseId ?? null,
          // The patch replaces the whole header: what is not sent is gone, and a report
          // that lost its issuing provider would vanish from that provider's own list.
          issuingProviderOrganizationId: report.issuingProviderOrganizationId ?? null,
          issuingPractitionerId: report.issuingPractitionerId ?? null,
        },
      });
      toast.notify({ tone: 'success', title: t('health.reports.header.saved') });
      onSaved();
    } catch {
      // Rendered below.
    }
  }

  return (
    <form
      onSubmit={(e) => void submit(e)}
      className="grid gap-3 md:grid-cols-3"
      noValidate
      data-testid="report-header-form"
    >
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
      <FormField label={t('health.reports.header.subtype')}>
        <Input
          name="reportSubtype"
          value={subtype}
          onChange={(e) => setSubtype(e.target.value)}
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
      <div className="md:col-span-3">
        <FormField
          label={t('health.reports.header.summary')}
          hint={t('health.reports.header.summaryHint')}
        >
          <Textarea
            name="clinicalSummary"
            value={summary}
            onChange={(e) => setSummary(e.target.value)}
            rows={3}
          />
        </FormField>
      </div>
      <div className="md:col-span-3">
        <ProblemAlert problem={commands.patch.error ? problemOf(commands.patch.error) : null} />
      </div>
      <div className="flex justify-end md:col-span-3">
        <Button type="submit" size="sm" loading={commands.patch.isPending} disabled={!ok}>
          {t('health.reports.header.save')}
        </Button>
      </div>
    </form>
  );
}

interface DraftService {
  serviceDefinitionId: string;
  coveredQuantity: string;
  coveredAmount: string;
  notes: string;
}

function ServicesEditor({ report }: { report: MedicalReport }) {
  const { t } = useTranslation();
  const toast = useToast();
  const commands = useReportCommands(report.id);
  const options = useServiceDefinitions();
  const [rows, setRows] = useState<DraftService[]>(
    report.services.map((s) => ({
      serviceDefinitionId: s.serviceDefinitionId,
      coveredQuantity: s.coveredQuantity ?? '',
      coveredAmount: s.coveredAmount ?? '',
      notes: s.notes ?? '',
    })),
  );
  const update = (index: number, patch: Partial<DraftService>) =>
    setRows((all) => all.map((r, i) => (i === index ? { ...r, ...patch } : r)));
  const ok = rows.length > 0 && rows.every((r) => r.serviceDefinitionId !== '');

  async function save() {
    const items: MedicalReportServiceInput[] = rows.map((r) => ({
      serviceDefinitionId: r.serviceDefinitionId,
      coveredQuantity: r.coveredQuantity.trim() ? r.coveredQuantity.trim() : null,
      coveredAmount: r.coveredAmount.trim() ? r.coveredAmount.trim() : null,
      currencyCode: r.coveredAmount.trim() ? 'TRY' : null,
      notes: r.notes.trim() ? r.notes.trim() : null,
    }));
    try {
      await commands.putServices.mutateAsync({ rowVersion: report.rowVersion, body: { items } });
      toast.notify({ tone: 'success', title: t('health.reports.services.saved') });
    } catch {
      // Rendered below.
    }
  }

  return (
    <div className="grid gap-3">
      {rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('health.reports.services.empty')}</p>
      ) : (
        <Table data-testid="report-services-editor">
          <THead>
            <TR>
              <TH>{t('health.reports.services.service')}</TH>
              <TH className="text-right">{t('health.reports.services.quantity')}</TH>
              <TH className="text-right">{t('health.reports.services.amount')}</TH>
              <TH>{t('health.reports.services.notes')}</TH>
              <TH>
                <span className="sr-only">{t('common.actions')}</span>
              </TH>
            </TR>
          </THead>
          <TBody>
            {rows.map((row, index) => (
              <TR key={index}>
                <TD>
                  <Select
                    name={`services.${index}.serviceDefinitionId`}
                    aria-label={t('health.reports.services.service')}
                    value={row.serviceDefinitionId}
                    onChange={(e) => update(index, { serviceDefinitionId: e.target.value })}
                    placeholder={t('common.none')}
                    options={(options.data ?? []).map((o) => ({ value: o.value, label: o.label }))}
                  />
                </TD>
                <TD>
                  <Input
                    name={`services.${index}.coveredQuantity`}
                    aria-label={t('health.reports.services.quantity')}
                    value={row.coveredQuantity}
                    onChange={(e) => update(index, { coveredQuantity: e.target.value })}
                    inputMode="decimal"
                    className="w-24 text-right font-mono"
                  />
                </TD>
                <TD>
                  <Input
                    name={`services.${index}.coveredAmount`}
                    aria-label={t('health.reports.services.amount')}
                    value={row.coveredAmount}
                    onChange={(e) => update(index, { coveredAmount: e.target.value })}
                    inputMode="decimal"
                    className="w-32 text-right font-mono"
                  />
                </TD>
                <TD>
                  <Input
                    name={`services.${index}.notes`}
                    aria-label={t('health.reports.services.notes')}
                    value={row.notes}
                    onChange={(e) => update(index, { notes: e.target.value })}
                  />
                </TD>
                <TD>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setRows((all) => all.filter((_, i) => i !== index))}
                  >
                    {t('health.reports.services.remove')}
                  </Button>
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
      <ProblemAlert
        problem={commands.putServices.error ? problemOf(commands.putServices.error) : null}
      />
      <div className="flex justify-between">
        <Button
          variant="secondary"
          size="sm"
          onClick={() =>
            setRows((all) => [
              ...all,
              { serviceDefinitionId: '', coveredQuantity: '', coveredAmount: '', notes: '' },
            ])
          }
        >
          {t('health.reports.services.add')}
        </Button>
        {rows.length > 0 ? (
          <Button
            size="sm"
            loading={commands.putServices.isPending}
            disabled={!ok}
            onClick={() => void save()}
          >
            {t('health.reports.services.save')}
          </Button>
        ) : null}
      </div>
    </div>
  );
}

/**
 * A treatment report: what a doctor said this person needs, for how long. A draft is
 * edited here; once submitted it is what the reviewer saw, and a change is a new version
 * that stands beside the decided one rather than replacing it.
 */
export function ReportPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const { reportId } = useParams({ from: '/app/reports/$reportId' });
  const query = useReport(reportId);
  const canManage = usePermission('health.medical_report.manage');
  const commands = useReportCommands(reportId);
  const createReport = useCreateReport();
  const personName = usePersonName(query.data?.data.personId);
  const chain = useReportChain(query.data?.data.rootReportId);

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
        page
        problem={problemOf(query.error)}
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
    );
  }
  const report = query.data.data;
  const clinical = report.projection === 'CLINICAL';
  const draft = report.status === 'DRAFT';
  const decided =
    report.status === 'APPROVED' || report.status === 'REJECTED' || report.status === 'SUPERSEDED';
  const cancellable = report.status === 'DRAFT' || report.status === 'SUBMITTED';
  const laterDraft = (chain.data ?? []).some((r) => r.versionNo > report.versionNo);
  const canCorrect =
    canManage &&
    clinical &&
    (report.status === 'APPROVED' || report.status === 'REJECTED') &&
    !laterDraft;

  async function run(action: 'submit' | 'cancel') {
    try {
      await commands[action].mutateAsync({ rowVersion: report.rowVersion });
      toast.notify({
        tone: 'success',
        title: t(action === 'submit' ? 'health.reports.submitted' : 'health.reports.cancelled'),
      });
    } catch (err) {
      toast.notify({ tone: 'danger', title: problemOf(err).title || t('problems.UNKNOWN') });
    }
  }

  async function correct() {
    try {
      const created = await createReport.mutateAsync({
        personId: report.personId,
        supersedesReportId: report.id,
        caseId: report.caseId ?? null,
        ...(report.issuingProviderOrganizationId
          ? { issuingProviderOrganizationId: report.issuingProviderOrganizationId }
          : {}),
      });
      toast.notify({ tone: 'success', title: t('health.reports.correctionOpened') });
      await navigate({ to: '/reports/$reportId', params: { reportId: created.data.id } });
    } catch (err) {
      toast.notify({ tone: 'danger', title: problemOf(err).title || t('problems.UNKNOWN') });
    }
  }

  return (
    <>
      <PageHeader
        title={`${report.reference} · v${report.versionNo}`}
        description={personName === undefined ? '…' : (personName ?? undefined)}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('health.cases.title'),
                render: (label) => <Link to="/cases">{label}</Link>,
              },
              ...(report.caseId
                ? [
                    {
                      label: t('health.case.title'),
                      render: (label: string) => (
                        <Link to="/cases/$caseId" params={{ caseId: report.caseId! }}>
                          {label}
                        </Link>
                      ),
                    },
                  ]
                : []),
              { label: report.reference },
            ]}
          />
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={reportTone(report.status)} data-testid="report-status">
              {t(`health.reports.status.${report.status}`)}
            </Badge>
            {draft && canManage && clinical ? (
              <Button
                size="sm"
                loading={commands.submit.isPending}
                onClick={() => void run('submit')}
              >
                {t('health.reports.commands.submit')}
              </Button>
            ) : null}
            {cancellable && canManage ? (
              <Button
                size="sm"
                variant="secondary"
                loading={commands.cancel.isPending}
                onClick={() => void run('cancel')}
              >
                {t('health.reports.commands.cancel')}
              </Button>
            ) : null}
            {canCorrect ? (
              <Button
                size="sm"
                variant="secondary"
                loading={createReport.isPending}
                onClick={() => void correct()}
              >
                {t('health.reports.commands.correct')}
              </Button>
            ) : null}
          </div>
        }
      />

      {!clinical ? (
        <p role="status" className="bg-info-soft text-fg mb-4 rounded-md p-3 text-sm">
          {t('health.reports.financialNote')}
        </p>
      ) : null}

      <div className="grid gap-4">
        {decided ? (
          <Card data-testid="report-decision">
            <h2 className="text-base font-semibold">{t('health.reports.decision.title')}</h2>
            <p className="mt-1 text-sm">{t(`health.reports.decision.${report.status}`)}</p>
            <dl className="mt-3 grid gap-x-6 gap-y-1 text-sm md:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]">
              {report.rejectReasonCode ? (
                <>
                  <dt className="text-fg-muted">{t('health.reports.decision.reason')}</dt>
                  <dd className="font-mono">{report.rejectReasonCode}</dd>
                </>
              ) : null}
              {report.reviewComment ? (
                <>
                  <dt className="text-fg-muted">{t('health.reports.decision.comment')}</dt>
                  <dd className="break-words">{report.reviewComment}</dd>
                </>
              ) : null}
              {report.reviewedAt ? (
                <>
                  <dt className="text-fg-muted">{t('health.reports.decision.reviewedAt')}</dt>
                  <dd>{formatDateTime(report.reviewedAt)}</dd>
                </>
              ) : null}
            </dl>
          </Card>
        ) : null}

        <Card>
          <h2 className="text-base font-semibold">{t('health.reports.header.title')}</h2>
          {draft && canManage && clinical ? (
            <div className="mt-3">
              <HeaderForm report={report} onSaved={() => undefined} />
            </div>
          ) : (
            <dl className="mt-3 grid gap-x-6 gap-y-1 text-sm md:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]">
              {clinical ? (
                <>
                  <dt className="text-fg-muted">{t('health.reports.header.type')}</dt>
                  <dd className="font-mono">
                    {report.reportType || '—'}
                    {report.reportSubtype ? ` / ${report.reportSubtype}` : ''}
                  </dd>
                </>
              ) : null}
              <dt className="text-fg-muted">{t('health.reports.header.issuedAt')}</dt>
              <dd>{formatDate(report.issuedAt)}</dd>
              <dt className="text-fg-muted">{t('health.reports.columns.valid')}</dt>
              <dd>
                {formatDate(report.validFrom)} – {formatDate(report.validTo)}
              </dd>
              {clinical ? (
                <>
                  <dt className="text-fg-muted">{t('health.reports.header.summary')}</dt>
                  <dd className="break-words" data-testid="report-summary">
                    {report.clinicalSummary || '—'}
                  </dd>
                </>
              ) : null}
            </dl>
          )}
          {!draft ? (
            <p className="text-fg-muted mt-3 text-sm">{t('health.reports.frozenNote')}</p>
          ) : null}
        </Card>

        <Card>
          <h2 className="text-base font-semibold">{t('health.reports.services.title')}</h2>
          <p className="text-fg-muted mb-3 mt-1 text-sm">{t('health.reports.services.intro')}</p>
          {draft && canManage && clinical ? (
            <ServicesEditor key={report.rowVersion} report={report} />
          ) : report.services.length === 0 ? (
            <p className="text-fg-muted text-sm">{t('health.reports.services.empty')}</p>
          ) : (
            <Table data-testid="report-services">
              <THead>
                <TR>
                  <TH>{t('health.reports.services.service')}</TH>
                  <TH className="text-right">{t('health.reports.services.quantity')}</TH>
                  <TH className="text-right">{t('health.reports.services.amount')}</TH>
                  {clinical ? <TH>{t('health.reports.services.notes')}</TH> : null}
                </TR>
              </THead>
              <TBody>
                {report.services.map((s) => (
                  <TR key={s.id}>
                    <TD>
                      <span className="font-mono">{s.serviceCode}</span>
                      <span className="text-fg-muted"> · {s.serviceName}</span>
                    </TD>
                    <TD className="text-right font-mono">{qty(s.coveredQuantity)}</TD>
                    <TD className="text-right font-mono">
                      {s.coveredAmount
                        ? formatMoney(s.coveredAmount, s.currencyCode ?? 'TRY')
                        : '—'}
                    </TD>
                    {clinical ? <TD>{s.notes ?? '—'}</TD> : null}
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </Card>

        {clinical ? (
          <Card>
            <h2 className="text-base font-semibold">{t('health.reports.documents.title')}</h2>
            {draft ? (
              <p className="text-fg-muted mb-3 mt-1 text-sm">
                {t('health.reports.documents.intro')}
              </p>
            ) : null}
            <DocumentsPanel
              aggregateType="MEDICAL_REPORT"
              aggregateId={report.id}
              requiredTypes={['MEDICAL_REPORT']}
              readOnly={!draft}
            />
          </Card>
        ) : null}

        {(chain.data?.length ?? 0) > 1 ? (
          <Card>
            <h2 className="text-base font-semibold">{t('health.reports.versions.title')}</h2>
            <div className="mt-3">
              <Table data-testid="report-versions">
                <THead>
                  <TR>
                    <TH>{t('health.reports.columns.version')}</TH>
                    <TH>{t('health.reports.columns.status')}</TH>
                    <TH>{t('health.reports.columns.issuedAt')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {chain.data!.map((r) => (
                    <TR key={r.id}>
                      <TD className="font-mono">
                        {r.id === report.id ? (
                          <>
                            v{r.versionNo} · {t('health.reports.versions.current')}
                          </>
                        ) : (
                          <Link
                            to="/reports/$reportId"
                            params={{ reportId: r.id }}
                            className="underline-offset-2 hover:underline"
                          >
                            v{r.versionNo}
                          </Link>
                        )}
                      </TD>
                      <TD>
                        <Badge tone={reportTone(r.status)}>
                          {t(`health.reports.status.${r.status}`)}
                        </Badge>
                      </TD>
                      <TD>{formatDate(r.issuedAt)}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          </Card>
        ) : null}
      </div>
    </>
  );
}

export { today };
