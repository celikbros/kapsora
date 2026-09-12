import type { InpatientStay, StaySegmentInput, StaySegmentType } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
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
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState } from 'react';

import { problemOf } from '../problems';
import { usePersonName, useReconciliation, useStay, useStayCommands } from './queries';
import { REASON_CODE, nowLocal, qty, stayTone, toInstant } from './words';

const SEGMENT_TYPES: StaySegmentType[] = ['WARD', 'ICU', 'SURGERY', 'OBSERVATION', 'COMPANION'];

interface DraftSegment {
  segmentType: StaySegmentType;
  startsAt: string;
  endsAt: string;
  roomCode: string;
  bedCode: string;
}

function local(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function SegmentsEditor({ stay }: { stay: InpatientStay }) {
  const { t } = useTranslation();
  const toast = useToast();
  const commands = useStayCommands(stay.id);
  const [rows, setRows] = useState<DraftSegment[]>(
    stay.segments.map((s) => ({
      segmentType: s.segmentType,
      startsAt: local(s.startsAt),
      endsAt: local(s.endsAt),
      roomCode: s.roomCode ?? '',
      bedCode: s.bedCode ?? '',
    })),
  );
  const update = (index: number, patch: Partial<DraftSegment>) =>
    setRows((all) => all.map((r, i) => (i === index ? { ...r, ...patch } : r)));
  const ok = rows.length > 0 && rows.every((r) => r.startsAt !== '');

  async function save() {
    const items: StaySegmentInput[] = rows.map((r) => ({
      segmentType: r.segmentType,
      startsAt: toInstant(r.startsAt),
      endsAt: r.endsAt ? toInstant(r.endsAt) : null,
      roomCode: r.roomCode.trim() ? r.roomCode.trim() : null,
      bedCode: r.bedCode.trim() ? r.bedCode.trim() : null,
    }));
    try {
      await commands.putSegments.mutateAsync({ rowVersion: stay.rowVersion, body: { items } });
      toast.notify({ tone: 'success', title: t('health.stays.segments.saved') });
    } catch {
      // Rendered below.
    }
  }

  return (
    <div className="grid gap-3">
      {rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('health.stays.segments.empty')}</p>
      ) : (
        <Table data-testid="segments-editor">
          <THead>
            <TR>
              <TH>{t('health.stays.segments.type')}</TH>
              <TH>{t('health.stays.segments.startsAt')}</TH>
              <TH>{t('health.stays.segments.endsAt')}</TH>
              <TH>{t('health.stays.segments.room')}</TH>
              <TH>{t('health.stays.segments.bed')}</TH>
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
                    name={`segments.${index}.type`}
                    aria-label={t('health.stays.segments.type')}
                    value={row.segmentType}
                    onChange={(e) =>
                      update(index, { segmentType: e.target.value as StaySegmentType })
                    }
                    options={SEGMENT_TYPES.map((x) => ({
                      value: x,
                      label: t(`health.stays.segments.types.${x}`),
                    }))}
                  />
                </TD>
                <TD>
                  <Input
                    name={`segments.${index}.startsAt`}
                    aria-label={t('health.stays.segments.startsAt')}
                    type="datetime-local"
                    value={row.startsAt}
                    onChange={(e) => update(index, { startsAt: e.target.value })}
                  />
                </TD>
                <TD>
                  <Input
                    name={`segments.${index}.endsAt`}
                    aria-label={t('health.stays.segments.endsAt')}
                    type="datetime-local"
                    value={row.endsAt}
                    onChange={(e) => update(index, { endsAt: e.target.value })}
                  />
                </TD>
                <TD>
                  <Input
                    name={`segments.${index}.room`}
                    aria-label={t('health.stays.segments.room')}
                    value={row.roomCode}
                    onChange={(e) => update(index, { roomCode: e.target.value })}
                    className="w-20 font-mono"
                  />
                </TD>
                <TD>
                  <Input
                    name={`segments.${index}.bed`}
                    aria-label={t('health.stays.segments.bed')}
                    value={row.bedCode}
                    onChange={(e) => update(index, { bedCode: e.target.value })}
                    className="w-20 font-mono"
                  />
                </TD>
                <TD>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setRows((all) => all.filter((_, i) => i !== index))}
                  >
                    {t('health.stays.segments.remove')}
                  </Button>
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
      <ProblemAlert
        problem={commands.putSegments.error ? problemOf(commands.putSegments.error) : null}
      />
      <div className="flex justify-between">
        <Button
          variant="secondary"
          size="sm"
          onClick={() =>
            setRows((all) => [
              ...all,
              { segmentType: 'WARD', startsAt: nowLocal(), endsAt: '', roomCode: '', bedCode: '' },
            ])
          }
        >
          {t('health.stays.segments.add')}
        </Button>
        {rows.length > 0 ? (
          <Button
            size="sm"
            loading={commands.putSegments.isPending}
            disabled={!ok}
            onClick={() => void save()}
          >
            {t('health.stays.segments.save')}
          </Button>
        ) : null}
      </div>
    </div>
  );
}

/**
 * The admission: what was promised in days, what was used, what was given back — and the
 * segments that say where the person actually was. Uzat is offered only while nothing is
 * undecided, because the server would refuse a second open extension and a button the
 * server refuses is a question the desk cannot answer.
 */
export function StayPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const { stayId } = useParams({ from: '/app/stays/$stayId' });
  const query = useStay(stayId);
  const canManage = usePermission('health.case.manage');
  const commands = useStayCommands(stayId);
  const personName = usePersonName(query.data?.data.personId);
  const status = query.data?.data.status;
  const reconciliation = useReconciliation(stayId, status === 'DISCHARGED');
  const [dialog, setDialog] = useState<'extend' | 'discharge' | 'cancel' | null>(null);
  const [days, setDays] = useState('1');
  const [reasonCode, setReasonCode] = useState('');
  const [reasonText, setReasonText] = useState('');
  const [dischargeAt, setDischargeAt] = useState(nowLocal());

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
  const stay = query.data.data;
  const clinical = stay.projection === 'CLINICAL';
  const active = stay.status === 'AUTHORIZED' || stay.status === 'ADMITTED';
  const undecided = stay.extensions.some((e) => e.status === 'REQUESTED');
  const canExtend = canManage && active && !undecided;
  const canDischarge = canManage && stay.status === 'ADMITTED';
  const canCancel = canManage && (stay.status === 'REQUESTED' || stay.status === 'AUTHORIZED');
  const n = Number(days);
  const extendOk = Number.isInteger(n) && n > 0 && REASON_CODE.test(reasonCode);

  function close() {
    setDialog(null);
    setReasonCode('');
    setReasonText('');
    commands.extend.reset();
    commands.discharge.reset();
    commands.cancel.reset();
  }

  async function extend() {
    try {
      await commands.extend.mutateAsync({
        rowVersion: stay.rowVersion,
        body: {
          additionalDays: n,
          reasonCode,
          ...(reasonText.trim() ? { reasonText: reasonText.trim() } : {}),
        },
      });
      toast.notify({ tone: 'success', title: t('health.stays.extend.created') });
      close();
    } catch {
      // The dialog shows the problem.
    }
  }
  async function discharge() {
    try {
      await commands.discharge.mutateAsync({
        rowVersion: stay.rowVersion,
        body: { dischargeAt: toInstant(dischargeAt) },
      });
      toast.notify({ tone: 'success', title: t('health.stays.discharge.done') });
      close();
    } catch {
      // The dialog shows the problem.
    }
  }
  async function cancel() {
    try {
      await commands.cancel.mutateAsync({ rowVersion: stay.rowVersion, reasonCode });
      toast.notify({ tone: 'success', title: t('health.stays.cancel.done') });
      close();
    } catch {
      // The dialog shows the problem.
    }
  }

  return (
    <>
      <PageHeader
        title={`${t('health.stays.title')} · ${formatDateTime(stay.admissionAt)}`}
        description={personName === undefined ? '…' : (personName ?? undefined)}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('health.cases.title'),
                render: (label) => <Link to="/cases">{label}</Link>,
              },
              {
                label: t('health.case.title'),
                render: (label) => (
                  <Link to="/cases/$caseId" params={{ caseId: stay.caseId }}>
                    {label}
                  </Link>
                ),
              },
              { label: t('health.stays.title') },
            ]}
          />
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={stayTone(stay.status)} data-testid="stay-status">
              {t(`health.stays.status.${stay.status}`)}
            </Badge>
            {canExtend ? (
              <Button
                size="sm"
                variant="secondary"
                onClick={() => setDialog('extend')}
                data-testid="extend-button"
              >
                {t('health.stays.extend.button')}
              </Button>
            ) : null}
            {canDischarge ? (
              <Button size="sm" onClick={() => setDialog('discharge')}>
                {t('health.stays.discharge.button')}
              </Button>
            ) : null}
            {canCancel ? (
              <Button size="sm" variant="secondary" onClick={() => setDialog('cancel')}>
                {t('health.stays.cancel.button')}
              </Button>
            ) : null}
          </div>
        }
      />

      <div className="grid gap-4">
        <Card>
          <h2 className="text-base font-semibold">{t('health.stays.figures.title')}</h2>
          <dl
            className="mt-3 grid gap-x-6 gap-y-1 text-sm md:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]"
            data-testid="stay-figures"
          >
            <dt className="text-fg-muted">{t('health.stays.figures.admissionAt')}</dt>
            <dd>{formatDateTime(stay.admissionAt)}</dd>
            <dt className="text-fg-muted">{t('health.stays.figures.expected')}</dt>
            <dd>{formatDateTime(stay.expectedDischargeAt)}</dd>
            {stay.dischargeAt ? (
              <>
                <dt className="text-fg-muted">{t('health.stays.figures.dischargeAt')}</dt>
                <dd>{formatDateTime(stay.dischargeAt)}</dd>
              </>
            ) : null}
            <dt className="text-fg-muted">{t('health.stays.figures.estimated')}</dt>
            <dd className="font-mono">{stay.estimatedDays}</dd>
            <dt className="text-fg-muted inline-flex items-center gap-1.5">
              {t('health.stays.figures.authorized')}
              <HelpHint term="onOnay" />
            </dt>
            <dd className="font-mono">{qty(stay.authorizedDays)}</dd>
            {stay.actualDays ? (
              <>
                <dt className="text-fg-muted">{t('health.stays.figures.actual')}</dt>
                <dd className="font-mono">{qty(stay.actualDays)}</dd>
                <dt className="text-fg-muted">{t('health.stays.figures.released')}</dt>
                <dd className="font-mono">{qty(stay.releasedDays)}</dd>
              </>
            ) : null}
          </dl>
          {stay.overAuthorization ? (
            <p role="status" className="bg-warning-soft text-fg mt-3 rounded-md p-3 text-sm">
              {t('health.stays.overNote')}
            </p>
          ) : null}
        </Card>

        {stay.status === 'DISCHARGED' ? (
          <Card data-testid="reconciliation">
            <h2 className="text-base font-semibold">{t('health.stays.reconciliation.title')}</h2>
            <p className="text-fg-muted mt-1 text-sm">{t('health.stays.reconciliation.intro')}</p>
            {reconciliation.isPending ? (
              <p className="text-fg-muted mt-2 text-sm">{t('common.loading')}</p>
            ) : reconciliation.error ? (
              <div className="mt-2">
                <ProblemAlert problem={problemOf(reconciliation.error)} />
              </div>
            ) : reconciliation.data ? (
              <dl className="mt-3 grid gap-x-6 gap-y-1 text-sm md:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]">
                <dt className="text-fg-muted">{t('health.stays.figures.authorized')}</dt>
                <dd className="font-mono">{qty(reconciliation.data.authorizedDays)}</dd>
                <dt className="text-fg-muted">{t('health.stays.figures.actual')}</dt>
                <dd className="font-mono">{qty(reconciliation.data.actualDays)}</dd>
                <dt className="text-fg-muted">{t('health.stays.figures.released')}</dt>
                <dd className="font-mono">{qty(reconciliation.data.releasedDays)}</dd>
                <dt className="text-fg-muted">{t('health.stays.figures.over')}</dt>
                <dd>{reconciliation.data.overAuthorization ? t('common.yes') : t('common.no')}</dd>
              </dl>
            ) : null}
          </Card>
        ) : null}

        <Card>
          <h2 className="text-base font-semibold">{t('health.stays.segments.title')}</h2>
          <p className="text-fg-muted mb-3 mt-1 text-sm">{t('health.stays.segments.intro')}</p>
          {canManage && active ? (
            <SegmentsEditor key={stay.rowVersion} stay={stay} />
          ) : stay.segments.length === 0 ? (
            <p className="text-fg-muted text-sm">{t('health.stays.segments.empty')}</p>
          ) : (
            <Table data-testid="segments">
              <THead>
                <TR>
                  <TH>{t('health.stays.segments.type')}</TH>
                  <TH>{t('health.stays.segments.startsAt')}</TH>
                  <TH>{t('health.stays.segments.endsAt')}</TH>
                  <TH>{t('health.stays.segments.room')}</TH>
                </TR>
              </THead>
              <TBody>
                {stay.segments.map((s) => (
                  <TR key={s.id}>
                    <TD>{t(`health.stays.segments.types.${s.segmentType}`)}</TD>
                    <TD>{formatDateTime(s.startsAt)}</TD>
                    <TD>{s.endsAt ? formatDateTime(s.endsAt) : '—'}</TD>
                    <TD className="font-mono">
                      {[s.roomCode, s.bedCode].filter(Boolean).join(' / ') || '—'}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </Card>

        <Card>
          <h2 className="text-base font-semibold">{t('health.stays.extensions.title')}</h2>
          {undecided ? (
            <p
              role="status"
              className="bg-info-soft text-fg mt-2 rounded-md p-3 text-sm"
              data-testid="extension-pending"
            >
              {t('health.stays.extensions.pendingNote')}
            </p>
          ) : null}
          {stay.extensions.length === 0 ? (
            <p className="text-fg-muted mt-2 text-sm">{t('health.stays.extensions.empty')}</p>
          ) : (
            <div className="mt-3">
              <Table data-testid="extensions">
                <THead>
                  <TR>
                    <TH>{t('health.stays.extensions.sequence')}</TH>
                    <TH className="text-right">{t('health.stays.extensions.days')}</TH>
                    <TH>{t('health.stays.extensions.reason')}</TH>
                    <TH>{t('health.stays.extensions.status')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {stay.extensions.map((e) => (
                    <TR key={e.id} data-status={e.status}>
                      <TD className="font-mono">{e.sequenceNo}</TD>
                      <TD className="text-right font-mono">{e.additionalDays}</TD>
                      <TD>
                        <span className="font-mono">{e.reasonCode}</span>
                        {clinical && e.reasonText ? (
                          <span className="text-fg-muted"> · {e.reasonText}</span>
                        ) : null}
                      </TD>
                      <TD>{t(`health.stays.extensions.statuses.${e.status}`)}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
        </Card>
      </div>

      <Dialog
        open={dialog === 'extend'}
        onOpenChange={(open) => (open ? setDialog('extend') : close())}
        title={t('health.stays.extend.title')}
        description={t('health.stays.extend.intro')}
        actions={
          <>
            <Button variant="secondary" onClick={close}>
              {t('common.cancel')}
            </Button>
            <Button
              loading={commands.extend.isPending}
              disabled={!extendOk}
              onClick={() => void extend()}
            >
              {t('health.stays.extend.submit')}
            </Button>
          </>
        }
      >
        <div className="grid gap-3">
          <FormField
            label={t('health.stays.extend.days')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              name="additionalDays"
              inputMode="numeric"
              value={days}
              onChange={(e) => setDays(e.target.value)}
              className="w-24 text-right font-mono"
            />
          </FormField>
          <FormField
            label={t('health.stays.extend.reasonCode')}
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
          <FormField label={t('health.stays.extend.reasonText')}>
            <Textarea
              name="reasonText"
              value={reasonText}
              onChange={(e) => setReasonText(e.target.value)}
              rows={2}
            />
          </FormField>
          <ProblemAlert problem={commands.extend.error ? problemOf(commands.extend.error) : null} />
        </div>
      </Dialog>

      <Dialog
        open={dialog === 'discharge'}
        onOpenChange={(open) => (open ? setDialog('discharge') : close())}
        title={t('health.stays.discharge.title')}
        description={t('health.stays.discharge.intro')}
        actions={
          <>
            <Button variant="secondary" onClick={close}>
              {t('common.cancel')}
            </Button>
            <Button
              loading={commands.discharge.isPending}
              disabled={!dischargeAt}
              onClick={() => void discharge()}
            >
              {t('health.stays.discharge.submit')}
            </Button>
          </>
        }
      >
        <div className="grid gap-3">
          <FormField
            label={t('health.stays.discharge.at')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              name="dischargeAt"
              type="datetime-local"
              value={dischargeAt}
              onChange={(e) => setDischargeAt(e.target.value)}
            />
          </FormField>
          <ProblemAlert
            problem={commands.discharge.error ? problemOf(commands.discharge.error) : null}
          />
        </div>
      </Dialog>

      <Dialog
        open={dialog === 'cancel'}
        onOpenChange={(open) => (open ? setDialog('cancel') : close())}
        title={t('health.stays.cancel.title')}
        actions={
          <>
            <Button variant="secondary" onClick={close}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="danger"
              loading={commands.cancel.isPending}
              disabled={!REASON_CODE.test(reasonCode)}
              onClick={() => void cancel()}
            >
              {t('health.stays.cancel.submit')}
            </Button>
          </>
        }
      >
        <div className="grid gap-3">
          <FormField
            label={t('health.stays.cancel.reasonCode')}
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
