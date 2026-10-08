import { ApiError, randomId } from '@kapsora/api-client';
import { useSession, useSessionStore, useStepUp } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Dialog,
  EmptyState,
  PageHeader,
  ProblemAlert,
  Spinner,
  StepUpDialog,
  statusTone,
} from '@kapsora/ui';
import { useQueryClient } from '@tanstack/react-query';
import { Link, useParams } from '@tanstack/react-router';
import { useEffect, useRef, useState } from 'react';
import { directoryContextKey, useOps } from '../api';
import { problemOf } from '../problems';
import { useTenantInvitation } from './queries';

interface CancelCommand {
  context: string;
  revision: number;
  tenantId: string;
  invitationId: string;
  etag: string;
  key: string;
}

export function AdminInvitationDetailPage() {
  const { t } = useTranslation();
  const { invitationId } = useParams({ from: '/app/admin/invitations/$invitationId' });
  const query = useTenantInvitation(invitationId);
  const ops = useOps();
  const store = useSessionStore();
  const stepUp = useStepUp();
  const queryClient = useQueryClient();
  const canManage = useSession((s) => s.activeTenant?.canManageTenantUsers === true);
  const tenantName = useSession((s) => s.activeTenant?.tenant.displayName ?? '');
  const [open, setOpen] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [command, setCommand] = useState<CancelCommand | null>(null);
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const mounted = useRef(true);
  const inFlight = useRef(false);
  const revision = useRef(0);

  useEffect(() => {
    mounted.current = true;
    let previous = directoryContextKey(store.getState());
    const unsubscribe = store.subscribe((state) => {
      const next = directoryContextKey(state);
      if (next !== previous) {
        revision.current += 1;
        previous = next;
      }
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [store]);

  const current = (snapshot: CancelCommand) => {
    const state = store.getState();
    return (
      mounted.current &&
      revision.current === snapshot.revision &&
      state.activeTenant?.canManageTenantUsers === true &&
      state.activeTenant.tenant.id === snapshot.tenantId &&
      directoryContextKey(state) === snapshot.context
    );
  };

  async function send(snapshot: CancelCommand) {
    if (inFlight.current || !current(snapshot)) return;
    inFlight.current = true;
    setBusy(true);
    setProblem(null);
    try {
      const result = await stepUp.run(() => {
        if (!current(snapshot)) throw new Error('Authorization context changed');
        return ops.admin.cancelInvitation(
          snapshot.tenantId,
          snapshot.invitationId,
          snapshot.etag,
          snapshot.key,
        );
      });
      if (!current(snapshot) || !result) return;
      queryClient.setQueryData(
        ['admin-invitations', snapshot.context, 'detail', snapshot.invitationId],
        result,
      );
      void queryClient.invalidateQueries({
        queryKey: ['admin-invitations', snapshot.context, 'list'],
      });
      setOpen(false);
      setCommand(null);
      setUncertain(false);
    } catch (error) {
      if (!current(snapshot)) return;
      setProblem(problemOf(error));
      const status = error instanceof ApiError ? error.status : 0;
      const code = error instanceof ApiError ? error.problem.code : '';
      if (
        status === 0 ||
        status === 408 ||
        status === 429 ||
        status >= 500 ||
        code === 'IDEMPOTENCY_IN_PROGRESS'
      ) {
        setUncertain(true);
      } else {
        setConflict(true);
        setCommand(null);
        setUncertain(false);
      }
    } finally {
      inFlight.current = false;
      if (current(snapshot)) setBusy(false);
    }
  }

  function submit() {
    if (inFlight.current || conflict) return;
    if (command) {
      void send(command);
      return;
    }
    const etag = query.data?.etag ?? '';
    if (!confirmed || !/^".+"$/.test(etag)) return;
    const state = store.getState();
    if (state.activeTenant?.canManageTenantUsers !== true) return;
    const snapshot: CancelCommand = {
      context: directoryContextKey(state),
      revision: revision.current,
      tenantId: state.activeTenant.tenant.id,
      invitationId,
      etag,
      key: randomId(),
    };
    setCommand(snapshot);
    void send(snapshot);
  }

  async function reload() {
    if (busy) return;
    const snapshot = directoryContextKey(store.getState());
    const result = await query.refetch();
    if (!mounted.current || directoryContextKey(store.getState()) !== snapshot || !result.isSuccess)
      return;
    setConflict(false);
    setProblem(null);
    setConfirmed(false);
  }

  const invitation = query.data?.data;
  return (
    <section data-testid="admin-invitation-detail-page">
      <Link
        to="/admin/invitations"
        className="text-primary mb-4 inline-block text-sm hover:underline"
      >
        ← {t('adminInvitations.backToList')}
      </Link>
      {query.isPending ? (
        <p className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </p>
      ) : query.isError ? (
        <ProblemAlert
          page
          problem={problemOf(query.error)}
          actions={
            <Button variant="secondary" onClick={() => void query.refetch()}>
              {t('common.retry')}
            </Button>
          }
        />
      ) : !invitation ? (
        <EmptyState title={t('adminInvitations.notFound')} />
      ) : (
        <>
          <PageHeader
            title={invitation.maskedRecipient || t('adminInvitations.recipientPurged')}
            description={t('adminInvitations.detailIntro')}
          />
          <dl className="border-line bg-surface grid gap-4 rounded-lg border p-4 text-sm sm:grid-cols-2">
            <div>
              <dt className="text-fg-muted">{t('adminInvitations.state')}</dt>
              <dd>
                <Badge tone={statusTone(invitation.status)}>
                  {t(`adminInvitations.status.${invitation.status}`)}
                </Badge>
              </dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('adminInvitations.delivery')}</dt>
              <dd>{t(`adminInvitations.deliveryStatus.${invitation.deliveryStatus}`)}</dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('adminInvitations.createdAt')}</dt>
              <dd>{formatDateTime(invitation.createdAt)}</dd>
            </div>
            <div>
              <dt className="text-fg-muted">{t('adminInvitations.expiresAt')}</dt>
              <dd>{formatDateTime(invitation.expiresAt)}</dd>
            </div>
          </dl>
          {canManage && invitation.status === 'PENDING' && (
            <div className="mt-6">
              <Button variant="danger" onClick={() => setOpen(true)}>
                {t('adminInvitations.cancel')}
              </Button>
            </div>
          )}
          <Dialog
            open={open}
            onOpenChange={(next) => {
              if (!next && !busy) setOpen(false);
            }}
            title={t('adminInvitations.cancelTitle')}
            description={t('adminInvitations.cancelImpact', { tenant: tenantName })}
          >
            <div className="grid gap-4">
              <p className="break-all text-sm">
                {invitation.maskedRecipient || t('adminInvitations.recipientPurged')} · {tenantName}
              </p>
              <label className="flex items-start gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={confirmed}
                  onChange={(event) => setConfirmed(event.target.checked)}
                  disabled={!!command || conflict || busy}
                />
                <span>{t('adminInvitations.cancelConfirm')}</span>
              </label>
              <ProblemAlert problem={problem} hideFieldErrors />
              {uncertain && (
                <p role="status" className="text-fg-muted text-sm">
                  {t('adminInvitations.uncertain')}
                </p>
              )}
              {conflict && (
                <p role="status" className="text-fg-muted text-sm">
                  {t('adminInvitations.conflict')}
                </p>
              )}
              <div className="flex justify-end gap-2">
                <Button variant="secondary" onClick={() => setOpen(false)} disabled={busy}>
                  {t('common.cancel')}
                </Button>
                {conflict ? (
                  <Button onClick={() => void reload()}>{t('adminInvitations.reload')}</Button>
                ) : (
                  <Button
                    variant="danger"
                    onClick={submit}
                    loading={busy}
                    disabled={!command && (!confirmed || !query.data?.etag)}
                  >
                    {command ? t('adminInvitations.retry') : t('adminInvitations.cancel')}
                  </Button>
                )}
              </div>
            </div>
          </Dialog>
          <StepUpDialog
            open={stepUp.required}
            action={t('adminInvitations.cancel')}
            busy={stepUp.busy}
            problem={stepUp.error}
            onConfirm={(password) => void stepUp.confirm(password)}
            onCancel={stepUp.cancel}
          />
        </>
      )}
    </section>
  );
}
