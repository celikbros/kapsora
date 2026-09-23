import type { Claim, ClaimException, ClaimLine } from '@kapsora/api-client';
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
  useMinWidth,
  useToast,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState } from 'react';

import { usePersonName } from '../health/queries';
import { REASON_CODE, claimTone, qty } from '../health/words';
import { problemOf } from '../problems';
import { useServiceName } from '../queries';
import { LinesEditor, draftFrom, linesValid, toNewLines, type DraftLine } from './LinesEditor';
import {
  useClaim,
  useClaimCommands,
  useClaimVersion,
  useClaimVersions,
  useReadiness,
} from './queries';

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

/** The exceptions panel: why a person is looking at this claim at all, before anything else. */
function Exceptions({ items }: { items: ClaimException[] }) {
  const { t } = useTranslation();
  if (items.length === 0) return null;
  return (
    <Card data-testid="claim-exceptions">
      <h2 className="text-base font-semibold">{t('claims.exceptions.title')}</h2>
      <p className="text-fg-muted mt-1 text-sm">{t('claims.exceptions.intro')}</p>
      <ul className="mt-3 grid gap-2 text-sm">
        {items.map((e, index) => (
          <li key={index} className="flex flex-wrap items-baseline gap-2">
            {e.lineNo ? (
              <span className="text-fg-muted">{t('claims.exceptions.line', { n: e.lineNo })}</span>
            ) : null}
            <span className="font-medium">
              {t(`claims.exceptions.codes.${e.code}`, { defaultValue: e.code })}
            </span>
            <Badge tone="neutral">{t(`claims.stage.${e.stage}`)}</Badge>
            {e.detail ? <span className="text-fg-muted break-words">{e.detail}</span> : null}
          </li>
        ))}
      </ul>
    </Card>
  );
}

/**
 * The lines as decided, read-only. A line's decision sits under its own figures — the
 * approved quantity and amount, who pays what, the reason and which stage said so — so the
 * reader never matches a list at the foot of the page back to a row. Amounts are the
 * server's decimal strings and nothing here adds them up.
 */
export function LinesTable({
  lines,
  clinical,
  testId,
}: {
  lines: ClaimLine[];
  clinical: boolean;
  testId: string;
}) {
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const money = (value: string | null | undefined, currency: string) =>
    value ? formatMoney(value, currency) : '—';
  const decision = (line: ClaimLine) =>
    line.decision ? (
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
    ) : (
      <span className="text-fg-muted">{t('claims.lines.noDecision')}</span>
    );

  if (!wide) {
    return (
      <ol className="grid gap-3" data-testid={testId}>
        {lines.map((line) => (
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
            {clinical && line.description ? (
              <p className="break-words">{line.description}</p>
            ) : null}
            <dl className="grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)] gap-x-3 gap-y-1">
              <dt className="text-fg-muted">{t('claims.lines.asked')}</dt>
              <dd className="font-mono">{formatMoney(line.lineAmount, line.currencyCode)}</dd>
              <dt className="text-fg-muted">{t('claims.lines.approvedAmount')}</dt>
              <dd className="font-mono">
                {line.decision
                  ? formatMoney(line.decision.approvedAmount, line.currencyCode)
                  : t('claims.lines.unpriced')}
              </dd>
              <dt className="text-fg-muted">{t('claims.lines.payer')}</dt>
              <dd className="font-mono">{money(line.decision?.payerAmount, line.currencyCode)}</dd>
              <dt className="text-fg-muted">{t('claims.lines.member')}</dt>
              <dd className="font-mono">{money(line.decision?.memberAmount, line.currencyCode)}</dd>
            </dl>
            <div className="border-line border-t pt-2">{decision(line)}</div>
          </li>
        ))}
      </ol>
    );
  }

  return (
    <Table data-testid={testId}>
      <THead>
        <TR>
          <TH>{t('claims.lines.line')}</TH>
          <TH>{t('claims.lines.service')}</TH>
          {clinical ? <TH>{t('claims.lines.description')}</TH> : null}
          <TH className="text-right">{t('claims.lines.quantity')}</TH>
          <TH className="text-right">{t('claims.lines.asked')}</TH>
          <TH>{t('claims.lines.decision')}</TH>
          <TH className="text-right">{t('claims.lines.approvedAmount')}</TH>
          <TH className="text-right">{t('claims.lines.payer')}</TH>
          <TH className="text-right">{t('claims.lines.member')}</TH>
        </TR>
      </THead>
      <TBody>
        {lines.map((line) => (
          <TR key={line.id} data-line={line.lineNo}>
            <TD className="font-mono">{line.lineNo}</TD>
            <TD>
              <ServiceCell line={line} />
            </TD>
            {clinical ? <TD className="break-words">{line.description ?? '—'}</TD> : null}
            <TD className="text-right font-mono">
              {qty(line.quantity)} {t(`units.${line.unitType}`)}
            </TD>
            <TD className="text-right font-mono">
              {formatMoney(line.lineAmount, line.currencyCode)}
            </TD>
            <TD>{decision(line)}</TD>
            <TD className="text-right font-mono">
              {line.decision
                ? formatMoney(line.decision.approvedAmount, line.currencyCode)
                : t('claims.lines.unpriced')}
            </TD>
            <TD className="text-right font-mono">
              {money(line.decision?.payerAmount, line.currencyCode)}
            </TD>
            <TD className="text-right font-mono">
              {money(line.decision?.memberAmount, line.currencyCode)}
            </TD>
          </TR>
        ))}
      </TBody>
    </Table>
  );
}

function DraftLines({
  claim,
  commands,
  busy,
}: {
  claim: Claim;
  commands: ReturnType<typeof useClaimCommands>;
  busy: boolean;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const [lines, setLines] = useState<DraftLine[]>(draftFrom(claim.lines));
  async function save() {
    try {
      await commands.putLines.mutateAsync({
        rowVersion: claim.rowVersion,
        body: { lines: toNewLines(lines) },
      });
      toast.notify({ tone: 'success', title: t('claims.lines.saved') });
    } catch {
      // Rendered below.
    }
  }
  return (
    <div className="grid gap-3">
      <fieldset disabled={busy} aria-busy={busy}>
        <LinesEditor
          lines={lines}
          onChange={setLines}
          {...(claim.projection === 'FINANCIAL'
            ? {
                fixedServices: claim.lines.map((line) => ({
                  value: line.serviceDefinitionId,
                  label: line.serviceCode ?? line.serviceDefinitionId,
                  unitType: line.unitType,
                })),
              }
            : {})}
        />
      </fieldset>
      <ProblemAlert problem={commands.putLines.error ? problemOf(commands.putLines.error) : null} />
      <div className="flex justify-end">
        <Button
          size="sm"
          loading={commands.putLines.isPending}
          disabled={busy || !linesValid(lines)}
          onClick={() => void save()}
        >
          {t('claims.lines.save')}
        </Button>
      </div>
    </div>
  );
}

/**
 * One claim, from the provider's side. A draft is edited here and submitted; after that
 * every line shows the decision it was given. A returned claim opens a correction: the
 * decided version stands above the draft, line by line, so what changed is visible without
 * a diff. The invoice-readiness panel is the server's answer, blockers and totals alike.
 */
export function ClaimPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const { claimId } = useParams({ from: '/app/claims/$claimId' });
  const query = useClaim(claimId);
  const canEdit = usePermission('claim.create');
  const canSubmit = usePermission('claim.submit');
  const canCancel = usePermission('claim.cancel');
  const commands = useClaimCommands(claimId);
  const busy = Object.values(commands).some((command) => command.isPending);
  const personName = usePersonName(query.data?.data.personId);
  const claim = query.data?.data;
  const previousNo =
    claim && claim.status === 'RETURNED' && claim.currentVersionNo > 1
      ? claim.currentVersionNo - 1
      : null;
  const previous = useClaimVersion(claimId, previousNo);
  const versions = useClaimVersions(claimId);
  const decided = claim?.status === 'APPROVED' || claim?.status === 'PARTIALLY_APPROVED';
  const readiness = useReadiness(claimId, Boolean(decided));
  const [cancelling, setCancelling] = useState(false);
  const [reasonCode, setReasonCode] = useState('');

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
  const clinical = claim.projection === 'CLINICAL';
  const editable = (claim.status === 'DRAFT' || claim.status === 'RETURNED') && canEdit;
  const submittable =
    (claim.status === 'DRAFT' || claim.status === 'RETURNED') &&
    canSubmit &&
    claim.lines.length > 0;
  const cancellable =
    canCancel &&
    (claim.status === 'DRAFT' ||
      claim.status === 'RETURNED' ||
      claim.status === 'PENDING_MEDICAL' ||
      claim.status === 'PENDING_FINANCIAL');

  async function submit() {
    if (busy) return;
    try {
      await commands.submit.mutateAsync({ rowVersion: claim!.rowVersion });
      toast.notify({ tone: 'success', title: t('claims.toast.submitted') });
    } catch (err) {
      toast.notify({ tone: 'danger', title: problemOf(err).title || t('problems.UNKNOWN') });
    }
  }
  async function cancel() {
    try {
      await commands.cancel.mutateAsync({ rowVersion: claim!.rowVersion, reasonCode });
      toast.notify({ tone: 'success', title: t('claims.toast.cancelled') });
      setCancelling(false);
    } catch {
      // The dialog shows the problem.
    }
  }

  return (
    <>
      <PageHeader
        title={claim.reference}
        description={[
          personName === undefined ? '…' : personName,
          `${formatDate(claim.serviceDateFrom)} – ${formatDate(claim.serviceDateTo)}`,
          t('claims.detail.version', { n: claim.currentVersionNo }),
        ]
          .filter(Boolean)
          .join(' · ')}
        breadcrumb={
          <Breadcrumb
            items={[
              { label: t('claims.title'), render: (label) => <Link to="/claims">{label}</Link> },
              { label: claim.reference },
            ]}
          />
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={claimTone(claim.status)} data-testid="claim-status">
              {t(`claims.status.${claim.status}`)}
            </Badge>
            {submittable ? (
              <Button
                size="sm"
                loading={commands.submit.isPending}
                disabled={busy}
                onClick={() => void submit()}
              >
                {t('claims.commands.submit')}
              </Button>
            ) : null}
            {cancellable ? (
              <Button
                size="sm"
                variant="secondary"
                disabled={busy}
                onClick={() => setCancelling(true)}
              >
                {t('claims.commands.cancel')}
              </Button>
            ) : null}
          </div>
        }
      />

      <div className="grid gap-4">
        <Exceptions items={claim.exceptions} />

        {claim.status === 'RETURNED' && previousNo ? (
          <Card data-testid="correction">
            <h2 className="text-base font-semibold">{t('claims.returned.title')}</h2>
            <p className="text-fg-muted mt-1 text-sm">
              {t('claims.returned.intro', { prev: previousNo, next: claim.currentVersionNo })}
            </p>
            {claim.returnReasonCode ? (
              <p className="mt-2 text-sm">
                <span className="text-fg-muted">{t('claims.returned.reason')}: </span>
                <span className="font-mono">{claim.returnReasonCode}</span>
              </p>
            ) : null}
            <div className="mt-4 grid min-w-0 gap-4">
              <section aria-labelledby="previous-version" className="min-w-0">
                <h3 id="previous-version" className="mb-2 text-sm font-semibold">
                  {t('claims.returned.previous', { n: previousNo })}
                </h3>
                {previous.isPending ? (
                  <p className="text-fg-muted text-sm">{t('common.loading')}</p>
                ) : previous.data ? (
                  <LinesTable
                    lines={previous.data.lines}
                    clinical={previous.data.projection === 'CLINICAL'}
                    testId="previous-lines"
                  />
                ) : (
                  <ProblemAlert problem={problemOf(previous.error)} />
                )}
              </section>
              <section aria-labelledby="current-version" className="min-w-0">
                <h3 id="current-version" className="mb-2 text-sm font-semibold">
                  {t('claims.returned.current', { n: claim.currentVersionNo })}
                </h3>
                {editable ? (
                  <DraftLines
                    key={claim.rowVersion}
                    claim={claim}
                    commands={commands}
                    busy={busy}
                  />
                ) : (
                  <LinesTable lines={claim.lines} clinical={clinical} testId="claim-lines" />
                )}
              </section>
            </div>
          </Card>
        ) : (
          <Card>
            <h2 className="text-base font-semibold">{t('claims.lines.title')}</h2>
            <p className="text-fg-muted mb-3 mt-1 text-sm">{t('claims.lines.intro')}</p>
            {claim.status === 'DRAFT' && editable ? (
              <DraftLines key={claim.rowVersion} claim={claim} commands={commands} busy={busy} />
            ) : claim.lines.length === 0 ? (
              <p className="text-fg-muted text-sm">{t('claims.lines.empty')}</p>
            ) : (
              <LinesTable lines={claim.lines} clinical={clinical} testId="claim-lines" />
            )}
          </Card>
        )}

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
                  <div className="mt-3">
                    <h3 className="text-sm font-semibold">{t('claims.readiness.blockers')}</h3>
                    <ul className="mt-1 list-disc pl-5 text-sm">
                      {readiness.data.blockers.map((b) => (
                        <li key={b}>{t(`claims.readiness.blocker.${b}`)}</li>
                      ))}
                    </ul>
                  </div>
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
        open={cancelling}
        onOpenChange={setCancelling}
        title={t('claims.reasonDialog.cancel')}
        actions={
          <>
            <Button variant="secondary" onClick={() => setCancelling(false)}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="danger"
              loading={commands.cancel.isPending}
              disabled={busy || !REASON_CODE.test(reasonCode)}
              onClick={() => void cancel()}
            >
              {t('claims.commands.cancel')}
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
              name="cancelReasonCode"
              value={reasonCode}
              onChange={(e) => setReasonCode(e.target.value.toUpperCase())}
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
            />
          </FormField>
          <ProblemAlert problem={commands.cancel.error ? problemOf(commands.cancel.error) : null} />
        </div>
      </Dialog>
    </>
  );
}
