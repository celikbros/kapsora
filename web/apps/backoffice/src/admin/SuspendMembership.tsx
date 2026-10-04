import {
  ApiError,
  randomId,
  type SuspendTenantUserReasonCode,
  type TenantUserDetail,
} from '@kapsora/api-client';
import { useSession, useSessionStore, useStepUp } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Button, Dialog, ProblemAlert, Select, StepUpDialog, useToast } from '@kapsora/ui';
import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { directoryContextKey, useOps } from '../api';
import { problemOf } from '../problems';

interface Command {
  context: string;
  contextRevision: number;
  tenantId: string;
  membershipId: string;
  reasonCode: SuspendTenantUserReasonCode;
  etag: string;
  idempotencyKey: string;
}

const reasons: SuspendTenantUserReasonCode[] = [
  'ACCESS_REVIEW',
  'STAFF_DEPARTURE',
  'SECURITY_CONCERN',
];

/** A tenant membership command. The account and its credentials are never edited here. */
export function SuspendMembership({
  membershipId,
  detail,
  etag,
  onReload,
}: {
  membershipId: string;
  detail: TenantUserDetail;
  etag: string;
  onReload: () => Promise<boolean>;
}) {
  const { t } = useTranslation();
  const ops = useOps();
  const store = useSessionStore();
  const queryClient = useQueryClient();
  const toast = useToast();
  const stepUp = useStepUp();
  const canManage = useSession((s) => s.activeTenant?.canManageTenantUsers === true);
  const tenantName = useSession((s) => s.activeTenant?.tenant.displayName ?? '');
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState<SuspendTenantUserReasonCode | ''>('');
  const [confirmed, setConfirmed] = useState(false);
  const [command, setCommand] = useState<Command | null>(null);
  const [uncertain, setUncertain] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const inFlight = useRef(false);
  const mounted = useRef(true);
  const contextRevision = useRef(0);

  useEffect(() => {
    mounted.current = true;
    let previous = directoryContextKey(store.getState());
    const unsubscribe = store.subscribe((state) => {
      const next = directoryContextKey(state);
      if (next !== previous) {
        contextRevision.current += 1;
        previous = next;
      }
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [store]);

  if (!canManage || detail.membership.membershipStatus !== 'ACTIVE') return null;

  const current = (snapshot: Command) => {
    const state = store.getState();
    return (
      mounted.current &&
      contextRevision.current === snapshot.contextRevision &&
      state.activeTenant?.canManageTenantUsers === true &&
      directoryContextKey(state) === snapshot.context &&
      state.activeTenant.tenant.id === snapshot.tenantId
    );
  };

  async function send(snapshot: Command) {
    if (inFlight.current || !current(snapshot)) return;
    inFlight.current = true;
    setBusy(true);
    setProblem(null);
    try {
      const result = await stepUp.run(() => {
        if (!current(snapshot)) throw new Error('Authorization context changed');
        return ops.admin.suspendUser(
          snapshot.tenantId,
          snapshot.membershipId,
          {
            reasonCode: snapshot.reasonCode,
          },
          snapshot.etag,
          snapshot.idempotencyKey,
        );
      });
      if (!current(snapshot)) return;
      if (!result) return; // Password dialog was cancelled; keep the same command for retry.
      queryClient.setQueryData(
        ['admin-users', snapshot.context, 'detail', snapshot.membershipId],
        result,
      );
      await queryClient.invalidateQueries({ queryKey: ['admin-users', snapshot.context, 'list'] });
      if (!current(snapshot)) return;
      setCommand(null);
      setUncertain(false);
      setOpen(false);
      toast.notify({ tone: 'success', title: t('adminUsers.suspend.done') });
    } catch (error) {
      if (!current(snapshot)) return;
      const status = error instanceof ApiError ? error.status : 0;
      const code = error instanceof ApiError ? error.problem.code : '';
      setProblem(problemOf(error));
      if (
        status === 0 ||
        status === 408 ||
        status === 429 ||
        status >= 500 ||
        code === 'IDEMPOTENCY_IN_PROGRESS'
      ) {
        // The server may have committed. Keep the exact command for an idempotent retry.
        setUncertain(true);
      } else {
        // A definitive refusal needs a fresh read before a different command can be sent.
        setConflict(true);
        setCommand(null);
        setUncertain(false);
      }
    } finally {
      inFlight.current = false;
      if (current(snapshot)) setBusy(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || conflict) return;
    if (command) {
      void send(command);
      return;
    }
    if (!reason || !confirmed || !etag || !/^".+"$/.test(etag)) return;
    const state = store.getState();
    if (state.activeTenant?.canManageTenantUsers !== true) return;
    const snapshot: Command = {
      context: directoryContextKey(state),
      contextRevision: contextRevision.current,
      tenantId: state.activeTenant.tenant.id,
      membershipId,
      reasonCode: reason,
      etag,
      idempotencyKey: randomId(),
    };
    setCommand(snapshot);
    void send(snapshot);
  }

  async function reload() {
    if (busy) return;
    const ok = await onReload();
    if (!mounted.current || !ok) return;
    setConflict(false);
    setProblem(null);
    setReason('');
    setConfirmed(false);
  }

  return (
    <>
      <div className="mt-6">
        <Button variant="danger" onClick={() => setOpen(true)}>
          {t('adminUsers.suspend.action')}
        </Button>
      </div>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (!next && !busy) setOpen(false);
        }}
        title={t('adminUsers.suspend.title')}
        description={t('adminUsers.suspend.impact', {
          name: detail.membership.displayName,
          tenant: tenantName,
        })}
      >
        <form onSubmit={submit} className="grid gap-4">
          <p className="text-sm">
            {t('adminUsers.suspend.target', {
              name: detail.membership.displayName,
              tenant: tenantName,
            })}
          </p>
          <label className="grid gap-1 text-sm">
            {t('adminUsers.suspend.reason')}
            <Select
              value={command?.reasonCode ?? reason}
              onChange={(event) =>
                setReason(event.target.value as SuspendTenantUserReasonCode | '')
              }
              disabled={!!command || conflict || busy}
              required
              placeholder={t('adminUsers.suspend.chooseReason')}
              options={reasons.map((value) => ({
                value,
                label: t(`adminUsers.suspend.reasons.${value}`),
              }))}
            />
          </label>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(event) => setConfirmed(event.target.checked)}
              disabled={!!command || conflict || busy}
            />
            <span>{t('adminUsers.suspend.confirm', { tenant: tenantName })}</span>
          </label>
          <ProblemAlert problem={problem} hideFieldErrors />
          {uncertain && (
            <p role="status" className="text-fg-muted text-sm">
              {t('adminUsers.suspend.uncertain')}
            </p>
          )}
          {conflict && (
            <p role="status" className="text-fg-muted text-sm">
              {t('adminUsers.suspend.conflict')}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setOpen(false)} disabled={busy}>
              {t('common.cancel')}
            </Button>
            {conflict ? (
              <Button type="button" onClick={() => void reload()}>
                {t('adminUsers.suspend.reload')}
              </Button>
            ) : (
              <Button
                type="submit"
                variant="danger"
                loading={busy}
                disabled={!command && (!reason || !confirmed || !etag)}
              >
                {command ? t('adminUsers.suspend.retry') : t('adminUsers.suspend.action')}
              </Button>
            )}
          </div>
        </form>
      </Dialog>
      <StepUpDialog
        open={stepUp.required}
        action={t('adminUsers.suspend.action')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </>
  );
}
