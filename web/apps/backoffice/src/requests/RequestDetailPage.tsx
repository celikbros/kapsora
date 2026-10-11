import type {
  ServiceRequest,
  ServiceRequestDecision,
  ServiceRequestItem,
  ServiceRequestItems,
  ServiceRequestVersionSummary,
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
  HelpHint,
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
  OwnFileNotice,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { useServiceDefinitionOptions, useServiceName } from '../catalog/names';
import { DocumentsPanel } from '../documents/DocumentsPanel';
import { problemOf } from '../problems';
import {
  useRequest,
  useRequestCommands,
  useRequestEligibility,
  useRequestRules,
  useRequestVersions,
} from './queries';
import { RequestAuthorization } from './RequestAuthorization';
import { allowedCommands, isReturned, requestTone } from './status';

type Command = 'submit' | 'return' | 'reject' | 'approve' | 'partiallyApprove' | 'cancel';
const REASON_CODE = /^[A-Z][A-Z0-9_.:-]{1,79}$/;

/** A quantity as a person reads it: "1", not "1.000000". String work only; nothing is parsed. */
function qty(value: string | null | undefined): string {
  if (!value) return '—';
  return value.includes('.') ? value.replace(/0+$/, '').replace(/\.$/, '') : value;
}

function money(amount: string | null | undefined, currency: string | null | undefined): string {
  if (!amount) return '—';
  return formatMoney(amount, currency ?? 'TRY');
}

function ServiceCell({ definitionId }: { definitionId: string }) {
  const name = useServiceName(definitionId);
  return (
    <>
      {name === undefined
        ? '…'
        : (name ?? <code className="font-mono text-xs">{definitionId}</code>)}
    </>
  );
}

/** The lines, read-only. Money right-aligned and monospaced, never parsed. */
function ItemsTable({ items }: { items: ServiceRequestItem[] }) {
  const { t } = useTranslation();
  const decided = items.some((i) => i.status !== 'REQUESTED');
  return (
    <Table data-testid="request-items">
      <THead>
        <TR>
          <TH>{t('requests.items.line')}</TH>
          <TH>{t('requests.items.service')}</TH>
          <TH>{t('requests.items.unit')}</TH>
          <TH className="text-right">{t('requests.items.requestedQuantity')}</TH>
          <TH className="text-right">{t('requests.items.requestedAmount')}</TH>
          {decided ? (
            <>
              <TH className="text-right">{t('requests.items.approvedQuantity')}</TH>
              <TH className="text-right">{t('requests.items.approvedAmount')}</TH>
              <TH>{t('requests.items.status')}</TH>
            </>
          ) : null}
        </TR>
      </THead>
      <TBody>
        {items.map((item) => (
          <TR key={item.id}>
            <TD className="font-mono text-xs tabular-nums">{item.lineNo}</TD>
            <TD>
              <ServiceCell definitionId={item.serviceDefinitionId} />
            </TD>
            <TD>{t(`units.${item.unitType}`)}</TD>
            <TD className="text-right font-mono text-xs tabular-nums">
              {qty(item.requestedQuantity)}
            </TD>
            <TD className="text-right font-mono text-xs tabular-nums">
              {money(item.requestedAmount, item.currencyCode)}
            </TD>
            {decided ? (
              <>
                <TD className="text-right font-mono text-xs tabular-nums">
                  {qty(item.approvedQuantity)}
                </TD>
                <TD className="text-right font-mono text-xs tabular-nums">
                  {money(item.approvedAmount, item.currencyCode)}
                </TD>
                <TD>
                  <Badge
                    tone={
                      item.status === 'REJECTED'
                        ? 'danger'
                        : item.status === 'REQUESTED'
                          ? 'neutral'
                          : 'success'
                    }
                  >
                    {t(`requests.itemStatus.${item.status}`)}
                  </Badge>
                  {item.decisionReasonCode ? (
                    <p className="mt-1 font-mono text-xs">{item.decisionReasonCode}</p>
                  ) : null}
                </TD>
              </>
            ) : null}
          </TR>
        ))}
      </TBody>
    </Table>
  );
}

interface DraftLine {
  serviceDefinitionId: string;
  unitType: ServiceRequestItem['unitType'];
  requestedQuantity: string;
  requestedAmount: string;
}

/** A dense editable set is a table that edits in place; the set saves at once (DESIGN.md). */
function ItemsEditor({
  items,
  saving,
  onSave,
}: {
  items: ServiceRequestItem[];
  saving: boolean;
  onSave: (body: ServiceRequestItems) => void;
}) {
  const { t } = useTranslation();
  const options = useServiceDefinitionOptions();
  const [lines, setLines] = useState<DraftLine[]>(
    items.map((i) => ({
      serviceDefinitionId: i.serviceDefinitionId,
      unitType: i.unitType,
      requestedQuantity: i.requestedQuantity,
      requestedAmount: i.requestedAmount ?? '',
    })),
  );
  const update = (index: number, patch: Partial<DraftLine>) =>
    setLines((all) => all.map((l, i) => (i === index ? { ...l, ...patch } : l)));
  const valid =
    lines.length > 0 && lines.every((l) => l.serviceDefinitionId && l.requestedQuantity.trim());

  return (
    <div className="grid gap-3">
      <Table data-testid="request-items-editor">
        <THead>
          <TR>
            <TH>{t('requests.items.service')}</TH>
            <TH>{t('requests.items.unit')}</TH>
            <TH className="text-right">{t('requests.items.requestedQuantity')}</TH>
            <TH className="text-right">{t('requests.items.requestedAmount')}</TH>
            <TH>
              <span className="sr-only">{t('common.actions')}</span>
            </TH>
          </TR>
        </THead>
        <TBody>
          {lines.map((line, index) => (
            <TR key={index}>
              <TD>
                <Select
                  name={`items.${index}.serviceDefinitionId`}
                  aria-label={t('requests.items.service')}
                  value={line.serviceDefinitionId}
                  onChange={(e) => {
                    const picked = options.data?.find((o) => o.value === e.target.value);
                    update(index, {
                      serviceDefinitionId: e.target.value,
                      ...(picked?.unitType ? { unitType: picked.unitType } : {}),
                    });
                  }}
                  placeholder={t('common.none')}
                  options={(options.data ?? []).map((o) => ({ value: o.value, label: o.label }))}
                />
              </TD>
              <TD>{t(`units.${line.unitType}`)}</TD>
              <TD>
                <Input
                  name={`items.${index}.requestedQuantity`}
                  aria-label={t('requests.items.requestedQuantity')}
                  value={line.requestedQuantity}
                  onChange={(e) => update(index, { requestedQuantity: e.target.value })}
                  inputMode="decimal"
                  className="w-24 text-right font-mono"
                />
              </TD>
              <TD>
                <Input
                  name={`items.${index}.requestedAmount`}
                  aria-label={t('requests.items.requestedAmount')}
                  value={line.requestedAmount}
                  onChange={(e) => update(index, { requestedAmount: e.target.value })}
                  inputMode="decimal"
                  className="w-32 text-right font-mono"
                />
              </TD>
              <TD>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setLines((all) => all.filter((_, i) => i !== index))}
                >
                  {t('requests.items.remove')}
                </Button>
              </TD>
            </TR>
          ))}
        </TBody>
      </Table>
      <div className="flex justify-between">
        <Button
          variant="secondary"
          size="sm"
          onClick={() =>
            setLines((all) => [
              ...all,
              {
                serviceDefinitionId: '',
                unitType: 'SESSION',
                requestedQuantity: '1',
                requestedAmount: '',
              },
            ])
          }
        >
          {t('requests.items.add')}
        </Button>
        <Button
          size="sm"
          loading={saving}
          disabled={!valid}
          onClick={() =>
            onSave({
              items: lines.map((l) => ({
                serviceDefinitionId: l.serviceDefinitionId,
                unitType: l.unitType,
                requestedQuantity: l.requestedQuantity.trim(),
                ...(l.requestedAmount.trim()
                  ? { requestedAmount: l.requestedAmount.trim(), currencyCode: 'TRY' }
                  : {}),
              })),
            })
          }
        >
          {t('requests.items.save')}
        </Button>
      </div>
    </div>
  );
}

/** One dialog for every command that takes a reason: the wording and the tone differ per command. */
function ReasonDialog({
  command,
  onClose,
  onSubmit,
  pending,
  error,
}: {
  command: Command | null;
  onClose: () => void;
  onSubmit: (reasonCode: string, reasonText: string) => void;
  pending: boolean;
  error: unknown;
}) {
  const { t } = useTranslation();
  const [code, setCode] = useState('');
  const [text, setText] = useState('');
  if (!command) return null;
  const needsText = command === 'return';
  const ok = REASON_CODE.test(code) && (!needsText || text.trim().length > 0);
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={t(`requests.commands.${command}`)}
      description={t(`requests.confirm.${command}`)}
    >
      <form
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          onSubmit(code, text.trim());
        }}
        className="grid gap-4"
        noValidate
      >
        <ProblemAlert problem={error ? problemOf(error) : null} />
        <FormField
          label={t('requests.reasonCode')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          <Input
            name="reasonCode"
            value={code}
            onChange={(e) => setCode(e.target.value.toUpperCase())}
            className="font-mono"
            autoComplete="off"
            spellCheck={false}
          />
        </FormField>
        <FormField
          label={t('requests.reasonText')}
          required={needsText}
          requiredLabel={t('common.requiredMark')}
        >
          <Textarea
            name="reasonText"
            rows={3}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
        </FormField>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            type="submit"
            loading={pending}
            disabled={!ok}
            variant={command === 'reject' || command === 'cancel' ? 'danger' : 'primary'}
          >
            {t(`requests.commands.${command}`)}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

interface LineDecision {
  lineNo: number;
  status: 'APPROVED' | 'PARTIALLY_APPROVED' | 'REJECTED';
  approvedQuantity: string;
  approvedAmount: string;
  decisionReasonCode: string;
}

/** A partial approval names every line: silence is not a decision. */
function PartialApprovalDialog({
  items,
  onClose,
  onSubmit,
  pending,
  error,
}: {
  items: ServiceRequestItem[];
  onClose: () => void;
  onSubmit: (body: ServiceRequestDecision) => void;
  pending: boolean;
  error: unknown;
}) {
  const { t } = useTranslation();
  const [reasonCode, setReasonCode] = useState('PARTIAL_APPROVAL');
  const [lines, setLines] = useState<LineDecision[]>(
    items.map((i) => ({
      lineNo: i.lineNo,
      status: 'APPROVED',
      approvedQuantity: i.requestedQuantity,
      approvedAmount: i.requestedAmount ?? '',
      decisionReasonCode: '',
    })),
  );
  const update = (lineNo: number, patch: Partial<LineDecision>) =>
    setLines((all) => all.map((l) => (l.lineNo === lineNo ? { ...l, ...patch } : l)));
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={t('requests.commands.partiallyApprove')}
      description={t('requests.confirm.partiallyApprove')}
      className="max-w-3xl"
    >
      <form
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          onSubmit({
            reasonCode,
            items: lines.map((l) => ({
              lineNo: l.lineNo,
              status: l.status,
              ...(l.status !== 'REJECTED' && l.approvedQuantity.trim()
                ? { approvedQuantity: l.approvedQuantity.trim() }
                : {}),
              ...(l.status !== 'REJECTED' && l.approvedAmount.trim()
                ? { approvedAmount: l.approvedAmount.trim() }
                : {}),
              ...(l.decisionReasonCode.trim()
                ? { decisionReasonCode: l.decisionReasonCode.trim() }
                : {}),
            })),
          });
        }}
        className="grid gap-4"
        noValidate
      >
        <ProblemAlert problem={error ? problemOf(error) : null} />
        <Table data-testid="partial-approval-table">
          <THead>
            <TR>
              <TH>{t('requests.items.line')}</TH>
              <TH>{t('requests.items.status')}</TH>
              <TH className="text-right">{t('requests.items.approvedQuantity')}</TH>
              <TH className="text-right">{t('requests.items.approvedAmount')}</TH>
              <TH>{t('requests.items.reason')}</TH>
            </TR>
          </THead>
          <TBody>
            {lines.map((line) => (
              <TR key={line.lineNo}>
                <TD className="font-mono text-xs tabular-nums">{line.lineNo}</TD>
                <TD>
                  <Select
                    name={`lines.${line.lineNo}.status`}
                    aria-label={t('requests.items.status')}
                    value={line.status}
                    onChange={(e) =>
                      update(line.lineNo, { status: e.target.value as LineDecision['status'] })
                    }
                    options={(['APPROVED', 'PARTIALLY_APPROVED', 'REJECTED'] as const).map((s) => ({
                      value: s,
                      label: t(`requests.itemStatus.${s}`),
                    }))}
                  />
                </TD>
                <TD>
                  {line.status !== 'REJECTED' ? (
                    <Input
                      aria-label={t('requests.items.approvedQuantity')}
                      value={line.approvedQuantity}
                      onChange={(e) => update(line.lineNo, { approvedQuantity: e.target.value })}
                      inputMode="decimal"
                      className="w-24 text-right font-mono"
                    />
                  ) : null}
                </TD>
                <TD>
                  {line.status !== 'REJECTED' ? (
                    <Input
                      aria-label={t('requests.items.approvedAmount')}
                      value={line.approvedAmount}
                      onChange={(e) => update(line.lineNo, { approvedAmount: e.target.value })}
                      inputMode="decimal"
                      className="w-32 text-right font-mono"
                    />
                  ) : null}
                </TD>
                <TD>
                  <Input
                    aria-label={t('requests.items.reason')}
                    value={line.decisionReasonCode}
                    onChange={(e) =>
                      update(line.lineNo, { decisionReasonCode: e.target.value.toUpperCase() })
                    }
                    className="font-mono"
                    autoComplete="off"
                    spellCheck={false}
                  />
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
        <FormField
          label={t('requests.reasonCode')}
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
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={pending} disabled={!REASON_CODE.test(reasonCode)}>
            {t('requests.commands.partiallyApprove')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** What the system said: the evaluation the submit was decided against, in its own words. */
function SystemSaid({ request }: { request: ServiceRequest }) {
  const { t } = useTranslation();
  const eligibility = useRequestEligibility(request.eligibilityEvaluationId);
  const rules = useRequestRules(request.ruleEvaluationId);
  const fired = (rules.data?.results ?? []).filter((r) => r.matched);

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <div>
        {/* The mark sits beside the heading, not inside it: it is not part of the name. */}
        <div className="flex items-baseline gap-1.5">
          <h3 className="text-sm font-medium">{t('requests.eligibility.title')}</h3>
          <HelpHint term="uygunluk" />
        </div>
        {!request.eligibilityEvaluationId ? (
          <p className="text-fg-muted mt-1 text-sm">{t('requests.eligibility.none')}</p>
        ) : eligibility.isPending ? (
          <Spinner />
        ) : eligibility.error || !eligibility.data ? (
          <p className="text-fg-muted mt-1 text-sm">{t('requests.eligibility.unavailable')}</p>
        ) : (
          <div className="mt-1 grid gap-2 text-sm">
            <Badge tone={eligibility.data.result.eligible ? 'success' : 'danger'}>
              {t(
                `eligibility.outcomes.${eligibility.data.result.eligible ? 'ELIGIBLE' : 'INELIGIBLE'}`,
              )}
            </Badge>
            <ul className="grid gap-1">
              {eligibility.data.result.explanations.map((e, i) => (
                <li
                  key={i}
                  className={
                    e.severity === 'ERROR'
                      ? 'text-danger'
                      : e.severity === 'WARNING'
                        ? 'text-warning'
                        : undefined
                  }
                >
                  <code className="mr-2 font-mono text-xs">{e.code}</code>
                  {e.message}
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
      <div>
        {/* The mark sits beside the heading, not inside it: it is not part of the name. */}
        <div className="flex items-baseline gap-1.5">
          <h3 className="text-sm font-medium">{t('requests.rules.title')}</h3>
          <HelpHint term="kural" />
        </div>
        {!request.ruleEvaluationId ? (
          <p className="text-fg-muted mt-1 text-sm">{t('requests.rules.none')}</p>
        ) : rules.isPending ? (
          <Spinner />
        ) : rules.error || !rules.data ? (
          <p className="text-fg-muted mt-1 text-sm">{t('requests.rules.unavailable')}</p>
        ) : fired.length === 0 ? (
          <p className="text-fg-muted mt-1 text-sm">{t('requests.rules.none')}</p>
        ) : (
          <ul className="mt-1 grid gap-1 text-sm">
            {fired.map((r) => (
              <li key={r.sequence}>
                <code className="mr-2 font-mono text-xs">{r.ruleCode}</code>
                <span className="text-fg-muted">{r.explanationCode}</span>
                {r.actionType ? (
                  <span className="ml-2 font-mono text-xs">{r.actionType}</span>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function VersionsList({ versions }: { versions: ServiceRequestVersionSummary[] }) {
  const { t } = useTranslation();
  return (
    <ol className="grid gap-2 text-sm">
      {versions.map((v) => (
        <li key={v.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <span className="font-mono text-xs tabular-nums">
            {t('requests.versions.no')} {v.versionNo}
          </span>
          <Badge tone={v.status === 'DRAFT' ? 'info' : 'neutral'}>
            {t(`requests.versionStatus.${v.status}`)}
          </Badge>
          <span className="text-fg-muted">
            {t('requests.versions.createdAt')}: {formatDateTime(v.createdAt)}
          </span>
          {v.submittedAt ? (
            <span className="text-fg-muted">
              {t('requests.columns.submittedAt')}: {formatDateTime(v.submittedAt)}
            </span>
          ) : null}
          {v.returnedAt ? (
            <span>
              {t('requests.versions.returnedAt')}: {formatDateTime(v.returnedAt)}
              {v.returnReasonCode ? (
                <code className="ml-2 font-mono text-xs">{v.returnReasonCode}</code>
              ) : null}
              {v.returnReasonText ? <span className="ml-2">— {v.returnReasonText}</span> : null}
            </span>
          ) : null}
        </li>
      ))}
    </ol>
  );
}

/**
 * The request as a story: what was asked for, what the system said, what a person
 * decided, what is still missing — in that order down the page (DESIGN.md). Status
 * decides which commands exist; a command the server would refuse is absent, not
 * disabled. Return and reject look different because they ask for different things.
 */
export function RequestDetailPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const { requestId } = useParams({ from: '/app/requests/$requestId' });
  const query = useRequest(requestId);
  const versions = useRequestVersions(requestId);
  const commands = useRequestCommands(requestId);
  const canCreate = usePermission('service_request.create');
  const canSubmit = usePermission('service_request.submit');
  const canReview = usePermission('service_request.review');
  const canCancel = usePermission('service_request.cancel');
  const self = useSelfPersonId();
  const [dialog, setDialog] = useState<Command | null>(null);

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

  const request = query.data.data;
  const etag = query.data.etag;
  // The names come with the row (WP-I5-05 section 2.6) rather than from two more reads.
  const personName = request.personDisplayName;
  const providerName = request.providerDisplayName;
  const allowed = allowedCommands(request.status);
  const returned = isReturned(request);
  const decided =
    request.status === 'APPROVED' ||
    request.status === 'PARTIALLY_APPROVED' ||
    request.status === 'REJECTED' ||
    request.status === 'CANCELLED' ||
    request.status === 'ELIGIBILITY_FAILED' ||
    request.status === 'EXPIRED' ||
    request.status === 'CLOSED';
  // A closed request takes no more documents: the form is absent, not refused.
  const closed =
    request.status === 'REJECTED' ||
    request.status === 'CANCELLED' ||
    request.status === 'EXPIRED' ||
    request.status === 'CLOSED';
  const lastReturned = (versions.data ?? []).find((v) => v.returnedAt);
  // A reviewer's own file: they may read it, but the decision is somebody else's.
  const own = self !== null && request.personId === self;
  const ownPending = own && canReview && (allowed.return || allowed.reject || allowed.approve);
  const offer = {
    submit: allowed.submit && canSubmit,
    return: allowed.return && canReview && !own,
    reject: allowed.reject && canReview && !own,
    approve: allowed.approve && canReview && !own,
    partiallyApprove: allowed.partiallyApprove && canReview && !own,
    cancel: allowed.cancel && canCancel,
  };
  const anyCommand = Object.values(offer).some(Boolean);

  async function run(command: Command, reasonCode = '', reasonText = '') {
    const body = reasonText ? { reasonCode, reasonText } : { reasonCode };
    try {
      let result: ServiceRequest;
      switch (command) {
        case 'submit':
          result = (await commands.submit.mutateAsync({ etag })).data;
          break;
        case 'return':
          result = (await commands.returnForCorrection.mutateAsync({ etag, body })).data;
          break;
        case 'reject':
          result = (await commands.reject.mutateAsync({ etag, body })).data;
          break;
        case 'approve':
          result = (await commands.approve.mutateAsync({ etag, body: { reasonCode } })).data;
          break;
        case 'cancel':
          result = (await commands.cancel.mutateAsync({ etag, body })).data;
          break;
        default:
          return;
      }
      setDialog(null);
      // The status that came back is the decision — never the one the button implied.
      toast.notify({ tone: 'success', title: t(`requests.status.${result.status}`) });
    } catch {
      // The dialog's alert carries the problem.
    }
  }

  const pending =
    commands.submit.isPending ||
    commands.returnForCorrection.isPending ||
    commands.reject.isPending ||
    commands.approve.isPending ||
    commands.cancel.isPending;
  const dialogError =
    dialog === 'return'
      ? commands.returnForCorrection.error
      : dialog === 'reject'
        ? commands.reject.error
        : dialog === 'cancel'
          ? commands.cancel.error
          : dialog === 'approve'
            ? commands.approve.error
            : dialog === 'submit'
              ? commands.submit.error
              : null;

  return (
    <>
      <PageHeader
        title={request.reference}
        description={[personName, providerName].filter(Boolean).join(' · ') || undefined}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('requests.title'),
                render: (label) => (
                  <Link to="/requests" search={{}}>
                    {label}
                  </Link>
                ),
              },
              { label: request.reference },
            ]}
          />
        }
        actions={
          <span data-testid="request-status">
            <Badge tone={requestTone(request.status)}>
              {t(`requests.status.${request.status}`)}
            </Badge>
          </span>
        }
      />

      {returned ? (
        <section
          aria-labelledby="request-returned"
          className="bg-warning-soft border-warning/40 mb-4 rounded-md border p-4 text-sm"
          data-testid="returned-panel"
        >
          <h2 id="request-returned" className="font-semibold">
            {t('requests.returned.title')}
          </h2>
          <p className="text-fg-muted mt-1">{t('requests.returned.body')}</p>
          <p className="mt-2">
            <code className="font-mono text-xs">{request.returnReasonCode}</code>
            {lastReturned?.returnReasonText ? (
              <span className="ml-2">{lastReturned.returnReasonText}</span>
            ) : null}
          </p>
        </section>
      ) : null}

      {request.status === 'REJECTED' ? (
        <section
          aria-labelledby="request-rejected"
          className="bg-surface-2 border-line-strong mb-4 rounded-md border p-4 text-sm"
          data-testid="rejected-panel"
        >
          <h2 id="request-rejected" className="font-semibold">
            {t('requests.rejected.title')}
          </h2>
          <p className="text-fg-muted mt-1">{t('requests.rejected.body')}</p>
          <p className="mt-2">
            <code className="font-mono text-xs">{request.rejectReasonCode}</code>
            {request.reviewComment ? <span className="ml-2">{request.reviewComment}</span> : null}
          </p>
        </section>
      ) : null}

      {ownPending ? <OwnFileNotice className="mb-4" /> : null}
      {anyCommand ? (
        <div className="mb-4 flex flex-wrap gap-2" data-testid="request-commands">
          {offer.submit ? (
            <Button onClick={() => setDialog('submit')}>{t('requests.commands.submit')}</Button>
          ) : null}
          {offer.approve ? (
            <Button onClick={() => setDialog('approve')}>{t('requests.commands.approve')}</Button>
          ) : null}
          {offer.partiallyApprove ? (
            <Button variant="secondary" onClick={() => setDialog('partiallyApprove')}>
              {t('requests.commands.partiallyApprove')}
            </Button>
          ) : null}
          {offer.return ? (
            <Button variant="secondary" onClick={() => setDialog('return')}>
              {t('requests.commands.return')}
            </Button>
          ) : null}
          {offer.reject ? (
            <Button variant="danger" onClick={() => setDialog('reject')}>
              {t('requests.commands.reject')}
            </Button>
          ) : null}
          {offer.cancel ? (
            <Button variant="ghost" onClick={() => setDialog('cancel')}>
              {t('requests.commands.cancel')}
            </Button>
          ) : null}
        </div>
      ) : null}

      <div className="grid gap-4">
        {decided && request.status !== 'REJECTED' && request.status !== 'ELIGIBILITY_FAILED' ? (
          <RequestAuthorization key={request.id} request={request} />
        ) : null}
        <Card>
          <h2 className="text-base font-semibold">{t('requests.sections.asked')}</h2>
          <dl className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-1 text-sm">
            <dt className="text-fg-muted">{t('requests.columns.type')}</dt>
            <dd>{t(`requests.type.${request.requestType}`)}</dd>
            <dt className="text-fg-muted">{t('requests.columns.channel')}</dt>
            <dd>{t(`requests.channel.${request.channel}`)}</dd>
            <dt className="text-fg-muted">{t('requests.columns.serviceDate')}</dt>
            <dd>{formatDate(request.serviceDate)}</dd>
            {request.submittedAt ? (
              <>
                <dt className="text-fg-muted">{t('requests.columns.submittedAt')}</dt>
                <dd>{formatDateTime(request.submittedAt)}</dd>
              </>
            ) : null}
          </dl>
          <div className="mt-4">
            {allowed.edit && canCreate ? (
              <>
                <ProblemAlert
                  problem={commands.items.error ? problemOf(commands.items.error) : null}
                  className="mb-3"
                />
                <ItemsEditor
                  key={request.rowVersion}
                  items={request.items}
                  saving={commands.items.isPending}
                  onSave={(body) => void commands.items.mutateAsync({ etag, body })}
                />
              </>
            ) : (
              <ItemsTable items={request.items} />
            )}
          </div>
        </Card>

        <Card>
          <h2 className="mb-3 text-base font-semibold">{t('requests.sections.said')}</h2>
          <SystemSaid request={request} />
        </Card>

        <Card>
          <h2 className="mb-3 text-base font-semibold">{t('requests.sections.decided')}</h2>
          {versions.isPending ? (
            <Spinner />
          ) : (versions.data ?? []).length === 0 ? (
            <p className="text-fg-muted text-sm">{t('common.none')}</p>
          ) : (
            <VersionsList versions={versions.data ?? []} />
          )}
          <div className="border-line mt-4 border-t pt-3 text-sm" data-testid="request-decision">
            <h3 className="font-medium">{t('requests.decision.title')}</h3>
            <p className="mt-1">{t(`requests.decision.${decided ? request.status : 'none'}`)}</p>
            {request.rejectReasonCode ? (
              <p className="mt-1">
                <code className="font-mono text-xs">{request.rejectReasonCode}</code>
              </p>
            ) : null}
            {request.reviewComment ? <p className="mt-1">{request.reviewComment}</p> : null}
            {request.closedAt ? (
              <p className="text-fg-muted mt-1">
                {t('requests.decision.closedAt')}: {formatDateTime(request.closedAt)}
              </p>
            ) : null}
          </div>
        </Card>

        <Card>
          <h2 className="mb-1 text-base font-semibold">{t('requests.sections.missing')}</h2>
          {request.requiredDocumentTypes === null || request.requiredDocumentTypes === undefined ? (
            <p className="text-fg-muted mb-3 text-sm">{t('requests.eligibility.none')}</p>
          ) : request.requiredDocumentTypes.length === 0 ? (
            <p className="text-fg-muted mb-3 text-sm">{t('requests.requiredDocuments.none')}</p>
          ) : null}
          <DocumentsPanel
            aggregateType="SERVICE_REQUEST"
            aggregateId={request.id}
            requiredTypes={request.requiredDocumentTypes}
            readOnly={closed}
          />
        </Card>
      </div>

      {dialog === 'submit' || dialog === 'approve' ? (
        <Dialog
          open
          onOpenChange={(open) => !open && setDialog(null)}
          title={t(`requests.commands.${dialog}`)}
          description={t(`requests.confirm.${dialog}`)}
          actions={
            <>
              <Button variant="secondary" onClick={() => setDialog(null)}>
                {t('common.cancel')}
              </Button>
              <Button
                loading={pending}
                onClick={() =>
                  void run(dialog, dialog === 'approve' ? 'APPROVED_AS_REQUESTED' : '')
                }
              >
                {t(`requests.commands.${dialog}`)}
              </Button>
            </>
          }
        >
          <ProblemAlert problem={dialogError ? problemOf(dialogError) : null} />
        </Dialog>
      ) : null}
      {dialog === 'return' || dialog === 'reject' || dialog === 'cancel' ? (
        <ReasonDialog
          command={dialog}
          onClose={() => setDialog(null)}
          onSubmit={(code, text) => void run(dialog, code, text)}
          pending={pending}
          error={dialogError}
        />
      ) : null}
      {dialog === 'partiallyApprove' ? (
        <PartialApprovalDialog
          items={request.items}
          onClose={() => setDialog(null)}
          pending={commands.partiallyApprove.isPending}
          error={commands.partiallyApprove.error}
          onSubmit={(body) =>
            void commands.partiallyApprove
              .mutateAsync({ etag, body })
              .then((r) => {
                setDialog(null);
                toast.notify({ tone: 'success', title: t(`requests.status.${r.data.status}`) });
              })
              .catch(() => undefined)
          }
        />
      ) : null}
    </>
  );
}
