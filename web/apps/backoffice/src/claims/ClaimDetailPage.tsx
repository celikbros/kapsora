import type {
  AccessContext,
  Claim,
  ClaimDecisionKind,
  ClaimException,
  ClaimLine,
  ClaimLineDecisionInput,
  Diagnosis,
} from '@kapsora/api-client';
import { usePermission, useSelfPersonId } from '@kapsora/auth';
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
  Select,
  Spinner,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  Textarea,
  useMinWidth,
  useToast,
  OwnFileNotice,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState, type ReactNode } from 'react';

import { useServiceName } from '../catalog/names';
import { PurposeDialog } from '../health/PurposeDialog';
import { accessFor, needsPurpose, useAccessState } from '../health/access';
import { useCaseDiagnoses } from '../health/queries';
import { problemOf } from '../problems';
import { useOrganizationName, usePersonName } from './names';
import {
  useClaim,
  useClaimCommands,
  useClaimVersion,
  useClaimVersions,
  useReadiness,
} from './queries';
import { REASON_CODE, claimTone, qty } from './status';

type Stage = 'MEDICAL' | 'FINANCIAL' | null;
type ClaimCommand = 'approve' | 'reject' | 'return';

function ServiceCell({ line }: { line: ClaimLine }) {
  const name = useServiceName(line.serviceDefinitionId);
  if (line.serviceCode) {
    return (
      <>
        <span className="font-mono">{line.serviceCode}</span>
        {name ? <span className="text-fg-muted"> · {name}</span> : null}
      </>
    );
  }
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}

function DiagnosisCell({
  id,
  diagnoses,
}: {
  id: string | null | undefined;
  diagnoses: Diagnosis[];
}) {
  const { t } = useTranslation();
  if (!id) return <span className="text-fg-muted">—</span>;
  const found = diagnoses.find((d) => d.id === id);
  if (!found) return <span className="text-fg-muted">…</span>;
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <span className="font-mono">{found.code}</span>
      <span>{found.display}</span>
      {found.sensitive ? <Badge tone="warning">{t('health.diagnoses.sensitive')}</Badge> : null}
    </span>
  );
}

/**
 * The exceptions panel: why a person is looking at this claim, before anything else. An
 * exception's detail is rendered by what it means for that code — the other claim's
 * reference, the quantity left on the authorization, the rule that fired — never as a bare
 * value the reader has to guess at.
 */
function Exceptions({ items }: { items: ClaimException[] }) {
  const { t } = useTranslation();
  return (
    <Card data-testid="claim-exceptions">
      <h2 className="text-base font-semibold">{t('claims.exceptions.title')}</h2>
      {items.length === 0 ? (
        <p className="text-fg-muted mt-1 text-sm">{t('claims.exceptions.none')}</p>
      ) : (
        <ul className="mt-3 grid gap-2 text-sm">
          {items.map((e, index) => (
            <li key={index} className="flex flex-wrap items-baseline gap-2">
              {e.lineNo ? (
                <span className="text-fg-muted">
                  {t('claims.exceptions.line', { n: e.lineNo })}
                </span>
              ) : null}
              <span className="font-medium">
                {t(`claims.exceptions.codes.${e.code}`, { defaultValue: e.code })}
              </span>
              <Badge tone="neutral">{t(`claims.stage.${e.stage}`)}</Badge>
              {e.detail ? (
                <span className="text-fg-muted break-words" data-testid="exception-detail">
                  {t(`claims.exceptions.detail.${e.code}`, {
                    defaultValue: t('claims.exceptions.detail.other'),
                  })}
                  : <span className="font-mono">{e.detail}</span>
                </span>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function DecisionSummary({ line }: { line: ClaimLine }) {
  const { t } = useTranslation();
  if (!line.decision) return <span className="text-fg-muted">{t('claims.lines.noDecision')}</span>;
  return (
    <div className="grid gap-0.5">
      <span>
        {t(`claims.decision.${line.decision.decision}`)}{' '}
        <Badge tone="neutral">{t(`claims.stage.${line.decision.stage}`)}</Badge>
      </span>
      <span className="text-fg-muted text-xs">
        <span className="font-mono">{line.decision.reasonCode}</span>
        {line.decision.reasonText ? ` · ${line.decision.reasonText}` : ''}
      </span>
    </div>
  );
}

interface DraftDecision {
  decision: ClaimDecisionKind;
  approvedQuantity: string;
  approvedAmount: string;
  payerAmount: string;
  memberAmount: string;
  reasonCode: string;
  reasonText: string;
}

function initialDecision(line: ClaimLine): DraftDecision {
  const d = line.decision;
  return {
    decision: d?.decision ?? 'APPROVED',
    approvedQuantity: d?.approvedQuantity ?? line.quantity,
    approvedAmount: d?.approvedAmount ?? line.lineAmount,
    payerAmount: d?.payerAmount ?? line.lineAmount,
    memberAmount: d?.memberAmount ?? '0',
    reasonCode: '',
    reasonText: '',
  };
}

function money(value: string | null | undefined, currency: string): string {
  return value ? formatMoney(value, currency) : '—';
}

/** A small labelled control for the decision block: the label is visible, not only spoken. */
function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid gap-1 text-xs">
      <span className="text-fg-muted">{label}</span>
      {children}
    </label>
  );
}

/**
 * The lines, laid out for the projection the server sent. The clinical layout carries the
 * diagnosis and the description and asks a clinical yes or no per line; the financial one
 * carries the money and asks for figures. Neither is the other with columns hidden: a column
 * that would be empty for every row does not exist. From `md` up the lines are a table; below
 * it, one block per line with the decision under its figures, so the answer is never inside
 * a horizontal scroller.
 */
function Lines({
  claim,
  stage,
  diagnoses,
  access,
}: {
  claim: Claim;
  stage: Stage;
  diagnoses: Diagnosis[];
  access: AccessContext | undefined;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const wide = useMinWidth(768);
  const commands = useClaimCommands(claim.id, access);
  const clinical = claim.projection === 'CLINICAL';
  const [drafts, setDrafts] = useState<Record<number, DraftDecision>>(() =>
    Object.fromEntries(claim.lines.map((l) => [l.lineNo, initialDecision(l)])),
  );
  const [comment, setComment] = useState('');
  const [attempted, setAttempted] = useState(false);
  const update = (lineNo: number, patch: Partial<DraftDecision>) =>
    setDrafts((all) => ({ ...all, [lineNo]: { ...all[lineNo]!, ...patch } }));

  const needsFigures = (d: DraftDecision) => stage === 'FINANCIAL' && d.decision !== 'REJECTED';
  const figuresMissing = (d: DraftDecision) =>
    needsFigures(d) &&
    [d.approvedQuantity, d.approvedAmount, d.payerAmount, d.memberAmount].some(
      (v) => v.trim() === '',
    );
  const reasonMissing = (d: DraftDecision) => !REASON_CODE.test(d.reasonCode);
  const invalidLines = claim.lines.filter((l) => {
    const d = drafts[l.lineNo]!;
    return reasonMissing(d) || figuresMissing(d);
  });

  async function save() {
    setAttempted(true);
    if (invalidLines.length > 0) return;
    // The medical stage answers yes or no per line on clinical grounds and never types a
    // figure: an approved line keeps the figures the pricing gave it, a refused one carries
    // zeros, and the money is the financial stage's to decide.
    const decisions: ClaimLineDecisionInput[] = claim.lines.map((l) => {
      const d = drafts[l.lineNo]!;
      const refused = d.decision === 'REJECTED';
      return {
        lineNo: l.lineNo,
        decision: d.decision,
        approvedQuantity: refused ? '0' : d.approvedQuantity.trim(),
        approvedAmount: refused ? '0' : d.approvedAmount.trim(),
        payerAmount: refused ? '0' : d.payerAmount.trim(),
        memberAmount: refused ? '0' : d.memberAmount.trim(),
        reasonCode: d.reasonCode,
        reasonText: d.reasonText.trim() ? d.reasonText.trim() : null,
      };
    });
    try {
      await commands.decide.mutateAsync({
        rowVersion: claim.rowVersion,
        body: { decisions, reviewComment: comment.trim() ? comment.trim() : null },
      });
      toast.notify({ tone: 'success', title: t('claims.review.saved') });
    } catch {
      // Rendered below.
    }
  }

  const medicalKinds: ClaimDecisionKind[] = ['APPROVED', 'REJECTED'];
  const financialKinds: ClaimDecisionKind[] = ['APPROVED', 'PARTIALLY_APPROVED', 'CUT', 'REJECTED'];

  /** The decision block of one line: the same controls in the table cell and in the stacked row. */
  const decisionBlock = (line: ClaimLine) => {
    const d = drafts[line.lineNo]!;
    const showReasonError = attempted && reasonMissing(d);
    return (
      <div className="grid min-w-[16rem] gap-2" data-testid={`decision-${line.lineNo}`}>
        <Field label={t('claims.review.decision')}>
          <Select
            name={`decisions.${line.lineNo}.decision`}
            aria-label={`${t('claims.review.decision')} ${line.lineNo}`}
            value={d.decision}
            onChange={(e) => update(line.lineNo, { decision: e.target.value as ClaimDecisionKind })}
            options={(stage === 'MEDICAL' ? medicalKinds : financialKinds).map((k) => ({
              value: k,
              label: t(`claims.decision.${k}`),
            }))}
          />
        </Field>
        {needsFigures(d) ? (
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('claims.review.approvedQuantity')}>
              <Input
                name={`decisions.${line.lineNo}.approvedQuantity`}
                aria-label={`${t('claims.review.approvedQuantity')} ${line.lineNo}`}
                value={d.approvedQuantity}
                onChange={(e) => update(line.lineNo, { approvedQuantity: e.target.value })}
                inputMode="decimal"
                className="text-right font-mono"
              />
            </Field>
            <Field label={t('claims.review.approvedAmount')}>
              <Input
                name={`decisions.${line.lineNo}.approvedAmount`}
                aria-label={`${t('claims.review.approvedAmount')} ${line.lineNo}`}
                value={d.approvedAmount}
                onChange={(e) => update(line.lineNo, { approvedAmount: e.target.value })}
                inputMode="decimal"
                className="text-right font-mono"
              />
            </Field>
            <Field label={t('claims.review.payerAmount')}>
              <Input
                name={`decisions.${line.lineNo}.payerAmount`}
                aria-label={`${t('claims.review.payerAmount')} ${line.lineNo}`}
                value={d.payerAmount}
                onChange={(e) => update(line.lineNo, { payerAmount: e.target.value })}
                inputMode="decimal"
                className="text-right font-mono"
              />
            </Field>
            <Field label={t('claims.review.memberAmount')}>
              <Input
                name={`decisions.${line.lineNo}.memberAmount`}
                aria-label={`${t('claims.review.memberAmount')} ${line.lineNo}`}
                value={d.memberAmount}
                onChange={(e) => update(line.lineNo, { memberAmount: e.target.value })}
                inputMode="decimal"
                className="text-right font-mono"
              />
            </Field>
          </div>
        ) : null}
        <Field label={t('claims.review.reasonCode')}>
          <Input
            name={`decisions.${line.lineNo}.reasonCode`}
            aria-label={`${t('claims.review.reasonCode')} ${line.lineNo}`}
            value={d.reasonCode}
            onChange={(e) => update(line.lineNo, { reasonCode: e.target.value.toUpperCase() })}
            className="font-mono"
            autoComplete="off"
            spellCheck={false}
            aria-invalid={showReasonError || undefined}
          />
        </Field>
        {showReasonError ? (
          <p role="alert" className="text-danger text-xs">
            {t('claims.review.reasonRequired')}
          </p>
        ) : null}
        {attempted && figuresMissing(d) ? (
          <p role="alert" className="text-danger text-xs">
            {t('claims.review.figuresRequired')}
          </p>
        ) : null}
        <Field label={t('claims.review.reasonText')}>
          <Input
            name={`decisions.${line.lineNo}.reasonText`}
            aria-label={`${t('claims.review.reasonText')} ${line.lineNo}`}
            value={d.reasonText}
            onChange={(e) => update(line.lineNo, { reasonText: e.target.value })}
          />
        </Field>
      </div>
    );
  };

  const decisionCell = (line: ClaimLine) =>
    stage ? decisionBlock(line) : <DecisionSummary line={line} />;

  const table = (
    <Table data-testid="claim-lines" data-projection={claim.projection}>
      <THead>
        <TR>
          <TH>{t('claims.lines.line')}</TH>
          <TH>{t('claims.lines.service')}</TH>
          {clinical ? <TH>{t('claims.lines.diagnosis')}</TH> : null}
          {clinical ? <TH>{t('claims.lines.description')}</TH> : null}
          <TH className="text-right">{t('claims.lines.quantity')}</TH>
          {!clinical ? <TH className="text-right">{t('claims.lines.asked')}</TH> : null}
          {!clinical ? <TH className="text-right">{t('claims.lines.contract')}</TH> : null}
          {!clinical ? <TH className="text-right">{t('claims.lines.approvedAmount')}</TH> : null}
          {!clinical ? <TH className="text-right">{t('claims.lines.payer')}</TH> : null}
          {!clinical ? <TH className="text-right">{t('claims.lines.member')}</TH> : null}
          <TH>{t('claims.lines.decision')}</TH>
        </TR>
      </THead>
      <TBody>
        {claim.lines.map((line) => (
          <TR key={line.id} data-line={line.lineNo}>
            <TD className="font-mono">{line.lineNo}</TD>
            <TD>
              <ServiceCell line={line} />
            </TD>
            {clinical ? (
              <TD>
                <DiagnosisCell id={line.diagnosisId} diagnoses={diagnoses} />
              </TD>
            ) : null}
            {clinical ? <TD className="break-words">{line.description ?? '—'}</TD> : null}
            <TD className="text-right font-mono">
              {qty(line.quantity)} {t(`units.${line.unitType}`)}
            </TD>
            {!clinical ? (
              <TD className="text-right font-mono">
                {formatMoney(line.lineAmount, line.currencyCode)}
              </TD>
            ) : null}
            {!clinical ? (
              <TD className="text-right font-mono">
                {money(line.decision?.contractAmount, line.currencyCode)}
              </TD>
            ) : null}
            {!clinical ? (
              <TD className="text-right font-mono">
                {money(line.decision?.approvedAmount, line.currencyCode)}
              </TD>
            ) : null}
            {!clinical ? (
              <TD className="text-right font-mono">
                {money(line.decision?.payerAmount, line.currencyCode)}
              </TD>
            ) : null}
            {!clinical ? (
              <TD className="text-right font-mono">
                {money(line.decision?.memberAmount, line.currencyCode)}
              </TD>
            ) : null}
            <TD>{decisionCell(line)}</TD>
          </TR>
        ))}
      </TBody>
    </Table>
  );

  const stacked = (
    <ol className="grid gap-3" data-testid="claim-lines" data-projection={claim.projection}>
      {claim.lines.map((line) => (
        <li
          key={line.id}
          className="border-line grid gap-2 rounded-md border p-3 text-sm"
          data-line={line.lineNo}
        >
          <div className="flex items-baseline gap-2">
            <span className="font-mono">{line.lineNo}</span>
            <span className="min-w-0 flex-1">
              <ServiceCell line={line} />
            </span>
            <span className="font-mono">
              {qty(line.quantity)} {t(`units.${line.unitType}`)}
            </span>
          </div>
          {clinical ? (
            <dl className="grid grid-cols-[minmax(0,6rem)_minmax(0,1fr)] gap-x-3 gap-y-1">
              <dt className="text-fg-muted">{t('claims.lines.diagnosis')}</dt>
              <dd>
                <DiagnosisCell id={line.diagnosisId} diagnoses={diagnoses} />
              </dd>
              <dt className="text-fg-muted">{t('claims.lines.description')}</dt>
              <dd className="break-words">{line.description ?? '—'}</dd>
            </dl>
          ) : (
            <dl className="grid grid-cols-[minmax(0,6rem)_minmax(0,1fr)] gap-x-3 gap-y-1">
              <dt className="text-fg-muted">{t('claims.lines.asked')}</dt>
              <dd className="font-mono">{formatMoney(line.lineAmount, line.currencyCode)}</dd>
              <dt className="text-fg-muted">{t('claims.lines.contract')}</dt>
              <dd className="font-mono">
                {money(line.decision?.contractAmount, line.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('claims.lines.approvedAmount')}</dt>
              <dd className="font-mono">
                {money(line.decision?.approvedAmount, line.currencyCode)}
              </dd>
              <dt className="text-fg-muted">{t('claims.lines.payer')}</dt>
              <dd className="font-mono">{money(line.decision?.payerAmount, line.currencyCode)}</dd>
              <dt className="text-fg-muted">{t('claims.lines.member')}</dt>
              <dd className="font-mono">{money(line.decision?.memberAmount, line.currencyCode)}</dd>
            </dl>
          )}
          <div className="border-line border-t pt-2">{decisionCell(line)}</div>
        </li>
      ))}
    </ol>
  );

  return (
    <div className="grid gap-3">
      {wide ? table : stacked}
      {stage ? (
        <>
          {stage === 'FINANCIAL' ? (
            <p className="text-fg-muted text-sm">{t('claims.review.splitHint')}</p>
          ) : null}
          <FormField label={t('claims.review.comment')}>
            <Textarea
              name="reviewComment"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              rows={2}
            />
          </FormField>
          {attempted && invalidLines.length > 0 ? (
            <p role="alert" className="text-danger text-sm">
              {t('claims.review.reasonRequired')}
            </p>
          ) : null}
          <ProblemAlert problem={commands.decide.error ? problemOf(commands.decide.error) : null} />
          <div className="flex justify-end">
            <Button
              loading={commands.decide.isPending}
              onClick={() => void save()}
              data-testid="save-decisions"
            >
              {t('claims.review.submit')}
            </Button>
          </div>
        </>
      ) : null}
    </div>
  );
}

/**
 * One claim, one page: its sections are decided by the projection the server sent. The
 * exceptions first (why is this in front of me), then the lines laid out for the caller's
 * half, then the claim-level command alone at the foot. A sensitive case asks for a purpose
 * in a real dialog before the clinical half opens; declining is a real path to the
 * financial half, with a sentence saying why the diagnosis is not on the page.
 */
export function ClaimDetailPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const { claimId } = useParams({ from: '/app/claims/$claimId' });
  const gate = useAccessState(claimId);
  const access = accessFor(gate.state);
  const query = useClaim(claimId, access);
  const canMedical = usePermission('claim.medical.review');
  const canFinancial = usePermission('claim.financial.review');
  const self = useSelfPersonId();
  const commands = useClaimCommands(claimId, access);
  const claim = query.data?.data;
  const personName = usePersonName(claim?.personId);
  const providerName = useOrganizationName(claim?.providerOrganizationId);
  const diagnoses = useCaseDiagnoses(
    claim?.projection === 'CLINICAL' ? (claim.caseId ?? '') : '',
    access,
  );
  const previousNo = claim && claim.currentVersionNo > 1 ? claim.currentVersionNo - 1 : null;
  const previous = useClaimVersion(
    claimId,
    claim?.status === 'RETURNED' ? previousNo : null,
    access,
  );
  const versions = useClaimVersions(claimId);
  const decided = claim?.status === 'APPROVED' || claim?.status === 'PARTIALLY_APPROVED';
  const readiness = useReadiness(claimId, Boolean(decided));
  const [command, setCommand] = useState<ClaimCommand | null>(null);
  const [reasonCode, setReasonCode] = useState('');
  const [reasonText, setReasonText] = useState('');

  const asking = needsPurpose(query.error) && !gate.state.purpose && !gate.state.declined;

  if (asking) {
    return (
      <PurposeDialog
        open
        defaultPurpose="CLAIM_REVIEW"
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
  if (query.error || !claim) {
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

  // A reviewer's own claim: readable, never decidable by them.
  const own = self !== null && claim.personId === self;
  const stage: Stage = own
    ? null
    : claim.status === 'PENDING_MEDICAL' && canMedical
      ? 'MEDICAL'
      : claim.status === 'PENDING_FINANCIAL' && canFinancial
        ? 'FINANCIAL'
        : null;
  const reviewing = claim.status === 'PENDING_MEDICAL' || claim.status === 'PENDING_FINANCIAL';
  const mayFinish =
    !own &&
    reviewing &&
    ((claim.status === 'PENDING_MEDICAL' && canMedical) ||
      (claim.status === 'PENDING_FINANCIAL' && canFinancial));
  const allDecided = claim.lines.every((l) => Boolean(l.decision));

  function closeDialog() {
    setCommand(null);
    setReasonCode('');
    setReasonText('');
    commands.approve.reset();
    commands.reject.reset();
    commands.returnForCorrection.reset();
  }
  async function run() {
    if (!command || !claim) return;
    const text = reasonText.trim();
    try {
      if (command === 'approve') {
        await commands.approve.mutateAsync({
          rowVersion: claim.rowVersion,
          body: { reasonCode, reasonText: text || null },
        });
      } else if (command === 'reject') {
        await commands.reject.mutateAsync({
          rowVersion: claim.rowVersion,
          body: { reasonCode, reasonText: text || null },
        });
      } else {
        await commands.returnForCorrection.mutateAsync({
          rowVersion: claim.rowVersion,
          body: { reasonCode, reasonText: text || null },
        });
      }
      toast.notify({
        tone: 'success',
        title: t(
          `claims.toast.${command === 'return' ? 'returned' : command === 'approve' ? 'approved' : 'rejected'}`,
        ),
      });
      closeDialog();
    } catch {
      // The dialog shows the problem.
    }
  }
  const pending =
    commands.approve.isPending ||
    commands.reject.isPending ||
    commands.returnForCorrection.isPending;
  const commandError =
    commands.approve.error ?? commands.reject.error ?? commands.returnForCorrection.error;

  return (
    <>
      <PageHeader
        title={claim.reference}
        description={[
          personName === undefined ? '…' : personName,
          providerName === undefined ? '…' : providerName,
          `${formatDate(claim.serviceDateFrom)} – ${formatDate(claim.serviceDateTo)}`,
          t('claims.detail.version', { n: claim.currentVersionNo }),
        ]
          .filter(Boolean)
          .join(' · ')}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('claims.title'),
                render: (label) => (
                  <Link to="/claims" search={{}}>
                    {label}
                  </Link>
                ),
              },
              { label: claim.reference },
            ]}
          />
        }
        actions={
          <div className="flex flex-wrap items-center gap-2" data-testid="claim-commands">
            <Badge tone={claimTone(claim.status)} data-testid="claim-status">
              {t(`claims.status.${claim.status}`)}
            </Badge>
            {own && reviewing ? <OwnFileNotice /> : null}
            {mayFinish && allDecided ? (
              <Button size="sm" onClick={() => setCommand('approve')}>
                {t('claims.commands.approve')}
              </Button>
            ) : null}
            {mayFinish ? (
              <>
                <Button size="sm" variant="secondary" onClick={() => setCommand('return')}>
                  {t('claims.commands.return')}
                </Button>
                <Button size="sm" variant="danger" onClick={() => setCommand('reject')}>
                  {t('claims.commands.reject')}
                </Button>
              </>
            ) : null}
          </div>
        }
      />

      {gate.state.declined && claim.projection === 'FINANCIAL' ? (
        <p
          role="status"
          className="bg-info-soft text-fg mb-4 rounded-md p-3 text-sm"
          data-testid="declined-note"
        >
          {t('review.purpose.declinedNote')}
        </p>
      ) : claim.projection === 'CLINICAL' && gate.state.purpose ? (
        <p
          role="status"
          className="bg-info-soft text-fg mb-4 rounded-md p-3 text-sm"
          data-testid="clinical-note"
        >
          {t('claims.detail.clinicalNote')}
        </p>
      ) : null}

      <div className="grid gap-4">
        <Exceptions items={claim.exceptions} />

        <Card>
          <h2 className="text-base font-semibold">
            {stage ? t('claims.review.title') : t('claims.lines.title')}
          </h2>
          <p className="text-fg-muted mb-3 mt-1 text-sm">
            {stage === 'MEDICAL'
              ? claim.projection === 'CLINICAL'
                ? t('claims.review.medicalIntro')
                : t('claims.review.medicalIntroFinancial')
              : stage === 'FINANCIAL'
                ? t('claims.review.financialIntro')
                : t('claims.lines.intro')}
          </p>
          {reviewing && !stage && (canMedical || canFinancial) ? (
            <p role="status" className="bg-warning-soft text-fg mb-3 rounded-md p-3 text-sm">
              {t('claims.review.notYourStage')}
            </p>
          ) : null}
          <Lines
            key={`${claim.rowVersion}-${claim.projection}`}
            claim={claim}
            stage={stage}
            diagnoses={diagnoses.data ?? []}
            access={access}
          />
        </Card>

        {claim.status === 'RETURNED' && previousNo ? (
          <Card data-testid="correction">
            <h2 className="text-base font-semibold">{t('claims.returned.title')}</h2>
            <p className="text-fg-muted mt-1 text-sm">
              {t('claims.returned.intro', { prev: previousNo, next: claim.currentVersionNo })}
            </p>
            {previous.isPending ? (
              <p className="text-fg-muted mt-2 text-sm">{t('common.loading')}</p>
            ) : previous.data ? (
              <div className="mt-3">
                <h3 className="mb-2 text-sm font-semibold">
                  {t('claims.returned.previous', { n: previousNo })}
                </h3>
                <Table data-testid="previous-lines">
                  <THead>
                    <TR>
                      <TH>{t('claims.lines.line')}</TH>
                      <TH>{t('claims.lines.service')}</TH>
                      <TH className="text-right">{t('claims.lines.quantity')}</TH>
                      <TH className="text-right">{t('claims.lines.asked')}</TH>
                      <TH>{t('claims.lines.decision')}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {previous.data.lines.map((line) => (
                      <TR key={line.id}>
                        <TD className="font-mono">{line.lineNo}</TD>
                        <TD>
                          <ServiceCell line={line} />
                        </TD>
                        <TD className="text-right font-mono">{qty(line.quantity)}</TD>
                        <TD className="text-right font-mono">
                          {formatMoney(line.lineAmount, line.currencyCode)}
                        </TD>
                        <TD>
                          <DecisionSummary line={line} />
                        </TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </div>
            ) : (
              <div className="mt-2">
                <ProblemAlert problem={problemOf(previous.error)} />
              </div>
            )}
          </Card>
        ) : null}

        {decided ? (
          <Card data-testid="readiness">
            <h2 className="text-base font-semibold">{t('claims.readiness.title')}</h2>
            {readiness.isPending ? (
              <p className="text-fg-muted mt-2 text-sm">{t('common.loading')}</p>
            ) : readiness.error ? (
              <div className="mt-2">
                <ProblemAlert problem={problemOf(readiness.error)} />
              </div>
            ) : readiness.data ? (
              <>
                <p className="mt-1 text-sm font-medium" data-testid="readiness-verdict">
                  {readiness.data.ready
                    ? t('claims.readiness.ready')
                    : t('claims.readiness.notReady')}
                </p>
                <dl className="mt-3 grid gap-x-6 gap-y-1 text-sm md:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]">
                  <dt className="text-fg-muted">{t('claims.readiness.approvedTotal')}</dt>
                  <dd className="font-mono">
                    {formatMoney(readiness.data.approvedTotal, readiness.data.currencyCode)}
                  </dd>
                  <dt className="text-fg-muted">{t('claims.readiness.payerTotal')}</dt>
                  <dd className="font-mono">
                    {formatMoney(readiness.data.payerTotal, readiness.data.currencyCode)}
                  </dd>
                  <dt className="text-fg-muted">{t('claims.readiness.memberTotal')}</dt>
                  <dd className="font-mono">
                    {formatMoney(readiness.data.memberTotal, readiness.data.currencyCode)}
                  </dd>
                  <dt className="text-fg-muted">{t('claims.readiness.decided')}</dt>
                  <dd className="font-mono">
                    {readiness.data.decidedLineCount} / {readiness.data.lineCount}
                  </dd>
                </dl>
                {readiness.data.blockers.length > 0 ? (
                  <ul className="mt-3 list-disc pl-5 text-sm">
                    {readiness.data.blockers.map((b) => (
                      <li key={b}>{t(`claims.readiness.blocker.${b}`)}</li>
                    ))}
                  </ul>
                ) : null}
              </>
            ) : null}
          </Card>
        ) : null}

        {(versions.data?.length ?? 0) > 1 ? (
          <Card>
            <h2 className="text-base font-semibold">{t('claims.versions.title')}</h2>
            <div className="mt-3">
              <Table data-testid="claim-versions">
                <THead>
                  <TR>
                    <TH>{t('claims.versions.columns.version')}</TH>
                    <TH>{t('claims.versions.columns.status')}</TH>
                    <TH>{t('claims.versions.columns.submittedAt')}</TH>
                    <TH>{t('claims.versions.columns.returnedAt')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {versions.data!.map((v) => (
                    <TR key={v.id}>
                      <TD className="font-mono">v{v.versionNo}</TD>
                      <TD>{t(`claims.versions.status.${v.status}`)}</TD>
                      <TD>{v.submittedAt ? formatDateTime(v.submittedAt) : '—'}</TD>
                      <TD>{v.returnedAt ? formatDateTime(v.returnedAt) : '—'}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          </Card>
        ) : null}
      </div>

      <Dialog
        open={command !== null}
        onOpenChange={(open) => {
          if (!open) closeDialog();
        }}
        title={command ? t(`claims.reasonDialog.${command}`) : ''}
        actions={
          <>
            <Button variant="secondary" onClick={closeDialog}>
              {t('common.cancel')}
            </Button>
            <Button
              variant={command === 'reject' ? 'danger' : 'primary'}
              loading={pending}
              disabled={!REASON_CODE.test(reasonCode)}
              onClick={() => void run()}
            >
              {command ? t(`claims.commands.${command}`) : ''}
            </Button>
          </>
        }
      >
        <div className="grid gap-3">
          <FormField
            label={t('claims.reasonDialog.reasonCode')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              name="reasonCode"
              value={reasonCode}
              onChange={(e) => setReasonCode(e.target.value.toUpperCase())}
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
            />
          </FormField>
          <FormField label={t('claims.reasonDialog.reasonText')}>
            <Textarea
              name="reasonText"
              value={reasonText}
              onChange={(e) => setReasonText(e.target.value)}
              rows={2}
            />
          </FormField>
          <ProblemAlert problem={commandError ? problemOf(commandError) : null} />
        </div>
      </Dialog>
    </>
  );
}
