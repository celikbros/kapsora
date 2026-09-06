import type { MedicalReport } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, formatDateTime, formatMoney, useTranslation } from '@kapsora/i18n';
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
import { Link, useParams } from '@tanstack/react-router';
import { useState } from 'react';

import { useOrganizationName, usePersonName } from '../claims/names';
import { useReport, useReportCommands } from '../claims/queries';
import { REASON_CODE, qty, reportTone } from '../claims/status';
import { DocumentsPanel } from '../documents/DocumentsPanel';
import { problemOf } from '../problems';
import { PurposeDialog } from './PurposeDialog';
import { accessFor, needsPurpose, useAccessState } from './access';

/**
 * The medical reviewer's screen for a treatment report: the clinical projection — the
 * summary, the type, the services with their limits, the linked documents — and the
 * decision. A sensitive case asks for a purpose first; declining lands on the financial
 * half, which is not enough to decide on, and the page says so.
 */
export function ReportReviewPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const { reportId } = useParams({ from: '/app/medical-reports/$reportId' });
  const gate = useAccessState(reportId);
  const access = accessFor(gate.state);
  const query = useReport(reportId, access);
  const canReview = usePermission('health.medical_report.review');
  const commands = useReportCommands(reportId);
  const report: MedicalReport | undefined = query.data?.data;
  const personName = usePersonName(report?.personId);
  const providerName = useOrganizationName(report?.issuingProviderOrganizationId);
  const [command, setCommand] = useState<'approve' | 'reject' | null>(null);
  const [reasonCode, setReasonCode] = useState('');
  const [comment, setComment] = useState('');

  const asking = needsPurpose(query.error) && !gate.state.purpose && !gate.state.declined;
  if (asking) {
    return (
      <PurposeDialog
        open
        defaultPurpose="MEDICAL_REVIEW"
        onGrant={(purpose, reason) => gate.grant(purpose, reason)}
        onDecline={() => gate.decline()}
      />
    );
  }
  if (query.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (query.error || !report) {
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
  const clinical = report.projection === 'CLINICAL';
  const canStart = canReview && report.status === 'SUBMITTED';
  const canDecide = canReview && clinical && report.status === 'UNDER_REVIEW';

  function closeDialog() {
    setCommand(null);
    setReasonCode('');
    setComment('');
    commands.approve.reset();
    commands.reject.reset();
  }
  async function start() {
    try {
      await commands.startReview.mutateAsync({ rowVersion: report!.rowVersion });
      toast.notify({ tone: 'success', title: t('review.report.started') });
    } catch (err) {
      toast.notify({ tone: 'danger', title: problemOf(err).title || t('problems.UNKNOWN') });
    }
  }
  async function decide() {
    if (!command || !report) return;
    const text = comment.trim();
    try {
      if (command === 'approve') {
        await commands.approve.mutateAsync({
          rowVersion: report.rowVersion,
          body: { reviewComment: text || null },
        });
      } else {
        await commands.reject.mutateAsync({
          rowVersion: report.rowVersion,
          body: { rejectReasonCode: reasonCode, reviewComment: text || null },
        });
      }
      toast.notify({
        tone: 'success',
        title: t(command === 'approve' ? 'review.report.approved' : 'review.report.rejected'),
      });
      closeDialog();
    } catch {
      // The dialog shows the problem.
    }
  }
  const commandError = commands.approve.error ?? commands.reject.error;

  return (
    <>
      <PageHeader
        title={`${report.reference} · v${report.versionNo}`}
        description={[
          personName === undefined ? '…' : personName,
          providerName === undefined ? '…' : providerName,
        ]
          .filter(Boolean)
          .join(' · ')}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('review.report.listTitle'),
                render: (label) => <Link to="/medical-reports">{label}</Link>,
              },
              { label: report.reference },
            ]}
          />
        }
        actions={
          <div className="flex flex-wrap items-center gap-2" data-testid="report-commands">
            <Badge tone={reportTone(report.status)} data-testid="report-status">
              {t(`health.reports.status.${report.status}`)}
            </Badge>
            {canStart ? (
              <Button
                size="sm"
                loading={commands.startReview.isPending}
                onClick={() => void start()}
              >
                {t('review.report.start')}
              </Button>
            ) : null}
            {canDecide ? (
              <>
                <Button size="sm" onClick={() => setCommand('approve')}>
                  {t('review.report.approve')}
                </Button>
                <Button size="sm" variant="danger" onClick={() => setCommand('reject')}>
                  {t('review.report.reject')}
                </Button>
              </>
            ) : null}
          </div>
        }
      />

      {gate.state.declined && !clinical ? (
        <p
          role="status"
          className="bg-info-soft text-fg mb-4 rounded-md p-3 text-sm"
          data-testid="declined-note"
        >
          {t('review.purpose.declinedNote')}
        </p>
      ) : !clinical ? (
        <p role="status" className="bg-info-soft text-fg mb-4 rounded-md p-3 text-sm">
          {t('health.reports.financialNote')}
        </p>
      ) : null}

      <div className="grid gap-4">
        <Card>
          <h2 className="text-base font-semibold">{t('review.report.intro')}</h2>
          <dl className="mt-3 grid gap-x-6 gap-y-1 text-sm md:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]">
            {clinical ? (
              <>
                <dt className="text-fg-muted">{t('review.report.type')}</dt>
                <dd className="font-mono">
                  {report.reportType || '—'}
                  {report.reportSubtype ? ` / ${report.reportSubtype}` : ''}
                </dd>
              </>
            ) : null}
            <dt className="text-fg-muted">{t('review.report.issuedAt')}</dt>
            <dd>{formatDate(report.issuedAt)}</dd>
            <dt className="text-fg-muted">{t('review.report.valid')}</dt>
            <dd>
              {formatDate(report.validFrom)} – {formatDate(report.validTo)}
            </dd>
            {report.reviewedAt ? (
              <>
                <dt className="text-fg-muted">{t('health.reports.decision.reviewedAt')}</dt>
                <dd>{formatDateTime(report.reviewedAt)}</dd>
              </>
            ) : null}
            {report.rejectReasonCode ? (
              <>
                <dt className="text-fg-muted">{t('health.reports.decision.reason')}</dt>
                <dd className="font-mono">{report.rejectReasonCode}</dd>
              </>
            ) : null}
          </dl>
        </Card>

        {clinical ? (
          <Card>
            <h2 className="text-base font-semibold">{t('review.report.summary')}</h2>
            <p className="mt-2 break-words text-sm" data-testid="report-summary">
              {report.clinicalSummary || t('review.report.noSummary')}
            </p>
            {report.reviewComment ? (
              <p className="text-fg-muted mt-3 text-sm">
                {t('review.report.comment')}: {report.reviewComment}
              </p>
            ) : null}
          </Card>
        ) : null}

        <Card>
          <h2 className="text-base font-semibold">{t('review.report.services')}</h2>
          {report.services.length === 0 ? (
            <p className="text-fg-muted mt-2 text-sm">{t('health.reports.services.empty')}</p>
          ) : (
            <div className="mt-3">
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
            </div>
          )}
        </Card>

        {clinical ? (
          <Card>
            <h2 className="text-base font-semibold">{t('review.report.documents')}</h2>
            <DocumentsPanel aggregateType="MEDICAL_REPORT" aggregateId={report.id} readOnly />
          </Card>
        ) : null}
      </div>

      <Dialog
        open={command !== null}
        onOpenChange={(open) => {
          if (!open) closeDialog();
        }}
        title={command === 'approve' ? t('review.report.approve') : t('review.report.reject')}
        actions={
          <>
            <Button variant="secondary" onClick={closeDialog}>
              {t('common.cancel')}
            </Button>
            <Button
              variant={command === 'reject' ? 'danger' : 'primary'}
              loading={commands.approve.isPending || commands.reject.isPending}
              disabled={command === 'reject' && !REASON_CODE.test(reasonCode)}
              onClick={() => void decide()}
            >
              {command === 'approve' ? t('review.report.approve') : t('review.report.reject')}
            </Button>
          </>
        }
      >
        <div className="grid gap-3">
          {command === 'reject' ? (
            <FormField
              label={t('review.report.reasonCode')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="rejectReasonCode"
                value={reasonCode}
                onChange={(e) => setReasonCode(e.target.value.toUpperCase())}
                className="font-mono"
                autoComplete="off"
                spellCheck={false}
              />
            </FormField>
          ) : null}
          <FormField label={t('review.report.comment')} hint={t('review.report.commentHint')}>
            <Textarea
              name="reviewComment"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              rows={3}
            />
          </FormField>
          <ProblemAlert problem={commandError ? problemOf(commandError) : null} />
        </div>
      </Dialog>
    </>
  );
}
