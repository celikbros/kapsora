import { formatDateTime, useTranslation } from '@kapsora/i18n';
import { useSession, useSessionStore } from '@kapsora/auth';
import {
  Badge,
  Button,
  Dialog,
  EmptyState,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  StepUpDialog,
  statusTone,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useEffect, useRef, useState } from 'react';
import { directoryContextKey } from '../api';
import { problemOf } from '../problems';
import { PermissionEvidence, roleDuty, roleTitle } from './RoleChangeCopy';
import { RoleChangePerson } from './RoleChangePerson';
import { useRoleChange } from './queries';
import { useRoleChangeCommand } from './useRoleChangeCommand';

type Decision = 'approve' | 'reject' | 'cancel';

export function RoleChangeDetailPage() {
  const { t } = useTranslation();
  const { requestId } = useParams({ from: '/app/admin/role-change-requests/$requestId' });
  const query = useRoleChange(requestId);
  const command = useRoleChangeCommand();
  const store = useSessionStore();
  const context = useSession(directoryContextKey);
  const tenantName = useSession((s) => s.activeTenant?.tenant.displayName ?? '');
  const [open, setOpen] = useState<Decision | null>(null);
  const [reason, setReason] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const previousContext = useRef(context);
  useEffect(() => {
    if (previousContext.current === context) return;
    previousContext.current = context;
    setOpen(null);
    setReason('');
    setConfirmed(false);
  }, [context]);
  const detail = query.data?.data;
  const request = command.success?.data.request ?? detail?.request;

  async function reload() {
    const before = directoryContextKey(store.getState());
    const result = await query.refetch();
    if (before !== directoryContextKey(store.getState()) || !result.isSuccess) return;
    command.afterReload();
    setReason('');
    setConfirmed(false);
  }
  function decide() {
    if (command.command) {
      command.retry();
      return;
    }
    if (!open || !confirmed || !detail || !query.data?.etag || request?.status !== 'PENDING')
      return;
    if (open === 'approve' && !detail.canApprove) return;
    if (open === 'reject' && (!detail.canReject || !reason)) return;
    if (open === 'cancel' && !detail.canCancel) return;
    const body =
      open === 'approve'
        ? {}
        : {
            reasonCode:
              open === 'cancel'
                ? ('WITHDRAWN' as const)
                : (reason as 'NOT_JUSTIFIED' | 'INCORRECT_ACCESS' | 'STALE_REQUEST'),
          };
    command.begin(requestId, open, body, query.data.etag);
  }
  const pending = request?.status === 'PENDING';
  return (
    <section data-testid="role-change-detail-page" className="min-w-0">
      <Link
        to="/admin/role-change-requests"
        activeOptions={{ exact: true }}
        className="text-primary mb-4 inline-block text-sm hover:underline"
      >
        ← {t('roleChanges.backToQueue')}
      </Link>
      {command.success && (
        <div role="status" className="border-line bg-surface mb-4 rounded-md border p-3 text-sm">
          {t(`roleChanges.done.${command.success.data.request.status}`)}
        </div>
      )}
      {command.refreshFailed && (
        <p role="alert" className="text-danger mb-4 text-sm">
          {t('roleChanges.refreshFailed')}
        </p>
      )}
      {command.command && (command.uncertain || command.cancelled) && !open && (
        <Button
          onClick={() =>
            setOpen(command.command?.action === 'create' ? null : (command.command?.action ?? null))
          }
        >
          {t('roleChanges.retry')}
        </Button>
      )}
      {query.isPending && !request ? (
        <p aria-busy="true">
          <Spinner /> {t('common.loading')}
        </p>
      ) : query.isError && !request ? (
        <ProblemAlert
          page
          problem={problemOf(query.error)}
          actions={<Button onClick={() => void query.refetch()}>{t('common.retry')}</Button>}
        />
      ) : !request ? (
        <EmptyState title={t('roleChanges.notFound')} />
      ) : (
        <>
          <PageHeader
            title={`${t(`roleChanges.operation.${request.operation}`)} · ${roleTitle(request.roleCode, t)}`}
            description={t('roleChanges.detailIntro')}
          />
          <div className="border-line bg-surface grid min-w-0 gap-4 rounded-lg border p-4 text-sm sm:grid-cols-2">
            <div>
              <span className="text-fg-muted">{t('roleChanges.state')}</span>
              <p>
                <Badge tone={statusTone(request.status)}>
                  {t(`roleChanges.status.${request.status}`)}
                </Badge>
              </p>
            </div>
            <div>
              <span className="text-fg-muted">{t('roleChanges.institution')}</span>
              <p>{tenantName}</p>
            </div>
            <div>
              <span className="text-fg-muted">{t('roleChanges.target')}</span>
              <p>
                <RoleChangePerson membershipId={request.targetMembershipId} />
              </p>
            </div>
            <div>
              <span className="text-fg-muted">{t('roleChanges.maker')}</span>
              <p>
                <RoleChangePerson membershipId={request.makerMembershipId} />
              </p>
            </div>
            <div>
              <span className="text-fg-muted">{t('roleChanges.createdAt')}</span>
              <p>{formatDateTime(request.createdAt)}</p>
            </div>
            <div>
              <span className="text-fg-muted">{t('roleChanges.reason')}</span>
              <p>{t(`roleChanges.reasons.${request.reasonCode}`)}</p>
            </div>
            {request.decidedAt && (
              <>
                <div>
                  <span className="text-fg-muted">{t('roleChanges.decidedAt')}</span>
                  <p>{formatDateTime(request.decidedAt)}</p>
                </div>
                <div>
                  <span className="text-fg-muted">{t('roleChanges.checker')}</span>
                  <p>
                    {request.decidedByMembershipId && (
                      <RoleChangePerson membershipId={request.decidedByMembershipId} />
                    )}
                  </p>
                </div>
              </>
            )}
            {request.decisionReasonCode && (
              <div>
                <span className="text-fg-muted">{t('roleChanges.decisionReason')}</span>
                <p>{t(`roleChanges.reasons.${request.decisionReasonCode}`)}</p>
              </div>
            )}
          </div>
          <div className="border-line bg-surface mt-4 rounded-lg border p-4 text-sm">
            <h2 className="font-semibold">{t('roleChanges.proposedAccess')}</h2>
            <p className="mt-2">{roleDuty(request.roleCode, t)}</p>
            <p>
              {t('roleChanges.appAndScope', { app: t('auth.apps.backoffice'), tenant: tenantName })}
            </p>
            <p>
              {request.status === 'PENDING'
                ? request.operation === 'ASSIGN'
                  ? t('roleChanges.pendingAssignEffect')
                  : t('roleChanges.pendingRevokeEffect')
                : request.status === 'APPROVED'
                  ? request.operation === 'ASSIGN'
                    ? t('roleChanges.assignApplied')
                    : t('roleChanges.revokeApplied')
                  : t('roleChanges.closedWithoutChange')}
            </p>
            {request.appliedValidity?.from && (
              <p>
                {t('roleChanges.appliedAt')}:{' '}
                {formatDateTime(request.decidedAt ?? request.appliedValidity.from)}
              </p>
            )}
            {request.revokeValidity && (
              <p>
                {t('roleChanges.originalValidity')}:{' '}
                {request.revokeValidity.from
                  ? formatDateTime(request.revokeValidity.from)
                  : t('adminUsers.noStart')}{' '}
                —{' '}
                {request.revokeValidity.to
                  ? formatDateTime(request.revokeValidity.to)
                  : t('adminUsers.noEnd')}
              </p>
            )}
            <PermissionEvidence request={request} />
          </div>
          {pending && !command.success && (
            <div className="mt-4 grid gap-3">
              {detail?.checkerAvailability === 'NO_ELIGIBLE_CHECKER' && (
                <p role="status" className="border-line bg-surface rounded-md border p-3 text-sm">
                  {t('roleChanges.noChecker')}
                </p>
              )}
              <div className="flex flex-wrap gap-2">
                {detail?.canApprove && (
                  <Button
                    onClick={() => {
                      setReason('');
                      setConfirmed(false);
                      setOpen('approve');
                    }}
                  >
                    {t('roleChanges.approve')}
                  </Button>
                )}
                {detail?.canReject && (
                  <Button
                    variant="secondary"
                    onClick={() => {
                      setReason('');
                      setConfirmed(false);
                      setOpen('reject');
                    }}
                  >
                    {t('roleChanges.reject')}
                  </Button>
                )}
                {detail?.canCancel && (
                  <Button
                    variant="secondary"
                    onClick={() => {
                      setReason('');
                      setConfirmed(false);
                      setOpen('cancel');
                    }}
                  >
                    {t('roleChanges.cancel')}
                  </Button>
                )}
              </div>
              {!detail?.canApprove && !detail?.canReject && !detail?.canCancel && (
                <p className="text-fg-muted text-sm">
                  {t('roleChanges.noDecision')}{' '}
                  {detail?.approvalRefusalCode &&
                    t(`problems.${detail.approvalRefusalCode}`, { defaultValue: '' })}
                </p>
              )}
              {(detail?.approvalRefusalCode === 'ROLE_CHANGE_TARGET_CHANGED' ||
                detail?.approvalRefusalCode === 'ROLE_CHANGE_CONFIGURATION_CHANGED') && (
                <p className="text-fg-muted text-sm">{t('roleChanges.staleProposal')}</p>
              )}
            </div>
          )}
        </>
      )}
      <Dialog
        open={open !== null && !command.success}
        onOpenChange={(next) => {
          if (!next && !command.busy) setOpen(null);
        }}
        title={t(`roleChanges.${open ?? 'approve'}`)}
        description={t('roleChanges.decisionIntro', {
          name: request ? roleTitle(request.roleCode, t) : '',
          tenant: tenantName,
        })}
      >
        <div className="grid gap-4 text-sm">
          {request && (
            <p className="font-medium">
              {t(`roleChanges.operation.${request.operation}`)} · {roleTitle(request.roleCode, t)}
            </p>
          )}
          {request && <p>{roleDuty(request.roleCode, t)}</p>}
          <p>
            {open === 'approve'
              ? request?.operation === 'REVOKE'
                ? t('roleChanges.approveRevokeEffect')
                : t('roleChanges.approveAssignEffect')
              : open === 'reject'
                ? t('roleChanges.rejectEffect')
                : t('roleChanges.cancelEffect')}
          </p>
          {request && (
            <p>
              {t('roleChanges.target')}:{' '}
              <RoleChangePerson membershipId={request.targetMembershipId} /> · {tenantName}
            </p>
          )}
          {open === 'reject' && (
            <label className="grid gap-1">
              {t('roleChanges.reason')}
              <Select
                value={
                  command.command && 'reasonCode' in command.command.body
                    ? String(command.command.body.reasonCode)
                    : reason
                }
                onChange={(event) => setReason(event.target.value)}
                disabled={!!command.command || command.busy || command.conflict}
                required
                placeholder={t('roleChanges.chooseReason')}
                options={['NOT_JUSTIFIED', 'INCORRECT_ACCESS', 'STALE_REQUEST'].map((value) => ({
                  value,
                  label: t(`roleChanges.reasons.${value}`),
                }))}
              />
            </label>
          )}
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(event) => setConfirmed(event.target.checked)}
              disabled={!!command.command || command.busy || command.conflict}
            />
            <span>{t('roleChanges.confirmDecision')}</span>
          </label>
          <ProblemAlert problem={command.problem} hideFieldErrors />
          {command.uncertain && <p role="status">{t('roleChanges.uncertain')}</p>}
          {command.cancelled && <p role="status">{t('roleChanges.stepUpCancelled')}</p>}
          {command.conflict && <p role="status">{t('roleChanges.conflict')}</p>}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setOpen(null)} disabled={command.busy}>
              {t('common.cancel')}
            </Button>
            {command.conflict ? (
              <Button onClick={() => void reload()}>{t('roleChanges.reload')}</Button>
            ) : (
              <Button
                variant={open === 'approve' ? 'primary' : 'secondary'}
                onClick={decide}
                loading={command.busy}
                disabled={!command.command && (!confirmed || (open === 'reject' && !reason))}
              >
                {command.command ? t('roleChanges.retry') : t(`roleChanges.${open ?? 'approve'}`)}
              </Button>
            )}
          </div>
        </div>
      </Dialog>
      <StepUpDialog
        open={command.stepUp.required}
        action={t(`roleChanges.${open ?? 'approve'}`)}
        busy={command.stepUp.busy}
        problem={command.stepUp.error}
        onConfirm={(password) => void command.stepUp.confirm(password)}
        onCancel={command.stepUp.cancel}
      />
    </section>
  );
}
