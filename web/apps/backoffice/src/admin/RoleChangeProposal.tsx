import type {
  CreateRoleChangeRequest,
  TenantRoleGrant,
  TenantUserDetail,
} from '@kapsora/api-client';
import { useSession, useSessionStore, useTenantId } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Button, Dialog, ProblemAlert, Select, Spinner, StepUpDialog } from '@kapsora/ui';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { directoryContextKey, useOps } from '../api';
import { problemOf } from '../problems';
import { roleDuty, roleTitle } from './RoleChangeCopy';
import { usePrivilegedRoleOptions, useRoleChangeEligibility } from './queries';
import { useRoleChangeCommand } from './useRoleChangeCommand';

export function RoleChangeProposal({
  membershipId,
  detail,
}: {
  membershipId: string;
  detail: TenantUserDetail;
}) {
  const { t } = useTranslation();
  const ops = useOps();
  const store = useSessionStore();
  const tenantId = useTenantId();
  const context = useSession(directoryContextKey);
  const allowed = useSession((s) => s.activeTenant?.canManageTenantRoles === true);
  const tenantName = useSession((s) => s.activeTenant?.tenant.displayName ?? '');
  const eligibility = useRoleChangeEligibility(membershipId);
  const options = usePrivilegedRoleOptions();
  const [grantCursors, setGrantCursors] = useState<string[]>(['']);
  const [grantIndex, setGrantIndex] = useState(0);
  const grantCursor = grantCursors[grantIndex] ?? '';
  const grants = useQuery({
    queryKey: ['admin-role-grants', context, 'b-proposal', membershipId, grantCursor],
    queryFn: () =>
      ops.admin.roleGrants(tenantId, membershipId, {
        limit: 100,
        ...(grantCursor ? { cursor: grantCursor } : {}),
      }),
    enabled: allowed,
    retry: false,
  });
  const command = useRoleChangeCommand();
  const [open, setOpen] = useState<'ASSIGN' | 'REVOKE' | null>(null);
  const [roleCode, setRoleCode] = useState('');
  const [reason, setReason] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const [revokeGrant, setRevokeGrant] = useState<TenantRoleGrant | null>(null);
  const previousContext = useRef(context);
  useEffect(() => {
    if (previousContext.current === context) return;
    previousContext.current = context;
    setOpen(null);
    setRoleCode('');
    setRevokeGrant(null);
    setReason('');
    setConfirmed(false);
    setGrantCursors(['']);
    setGrantIndex(0);
  }, [context]);
  if (!allowed) return null;
  const current = eligibility.data?.data;
  const selected = options.data?.find((item) => item.code === roleCode);
  const revokeOption = options.data?.find((item) => item.code === revokeGrant?.roleCode);
  const eligibleGrant =
    grants.data?.data.items.find((item) => current?.revokeGrantIds.includes(item.id)) ?? null;
  const nextGrantCursor = grants.data?.data.nextCursor;
  const canNextGrant =
    !!nextGrantCursor && !grantCursors.slice(0, grantIndex + 1).includes(nextGrantCursor);
  const etagMatches = !!eligibility.data?.etag && eligibility.data.etag === grants.data?.etag;
  const canAssign = !!current?.canRequestAssignment && !!selected;
  const noChecker = current?.checkerAvailability === 'NO_ELIGIBLE_CHECKER';

  function clearSelection() {
    setRoleCode('');
    setRevokeGrant(null);
    setReason('');
    setConfirmed(false);
  }
  async function reload() {
    const before = directoryContextKey(store.getState());
    const results = await Promise.all([eligibility.refetch(), options.refetch(), grants.refetch()]);
    if (before !== directoryContextKey(store.getState()) || results.some((item) => !item.isSuccess))
      return;
    command.afterReload();
    clearSelection();
    setGrantCursors(['']);
    setGrantIndex(0);
  }
  function submit(event: FormEvent) {
    event.preventDefault();
    if (command.command) {
      command.retry();
      return;
    }
    if (!confirmed || !reason || !etagMatches || !eligibility.data?.etag) return;
    let body: CreateRoleChangeRequest;
    if (open === 'ASSIGN' && canAssign) {
      body = {
        operation: 'ASSIGN',
        roleCode: selected!.code,
        configurationHash: selected!.configurationHash,
        reasonCode: reason as 'ONBOARDING' | 'DUTY_ASSIGNMENT',
      };
    } else if (
      open === 'REVOKE' &&
      revokeGrant &&
      revokeOption &&
      current?.revokeGrantIds.includes(revokeGrant.id)
    ) {
      body = {
        operation: 'REVOKE',
        grantId: revokeGrant.id,
        configurationHash: revokeOption.configurationHash,
        reasonCode: reason as 'ACCESS_REVIEW' | 'DUTY_ENDED' | 'SECURITY_CONCERN',
      };
    } else return;
    command.begin(membershipId, 'create', body, eligibility.data.etag);
  }

  return (
    <section className="mt-6" data-testid="role-change-proposal">
      <h2 className="text-lg font-semibold">{t('roleChanges.proposalTitle')}</h2>
      <p className="text-fg-muted mb-3 text-sm">{t('roleChanges.proposalIntro')}</p>
      {command.success && (
        <div role="status" className="border-line bg-surface mb-3 rounded-md border p-3 text-sm">
          <p>{t('roleChanges.created')}</p>
          <Link
            className="text-primary font-medium hover:underline"
            to="/admin/role-change-requests/$requestId"
            params={{ requestId: command.success.data.request.id }}
          >
            {t('roleChanges.openRequest')} →
          </Link>
        </div>
      )}
      {command.refreshFailed && (
        <p role="alert" className="text-danger mb-3 text-sm">
          {t('roleChanges.refreshFailed')}
        </p>
      )}
      {command.command && (command.uncertain || command.cancelled) && !open && (
        <Button
          onClick={() =>
            setOpen(
              command.command?.body && 'operation' in command.command.body
                ? command.command.body.operation
                : 'ASSIGN',
            )
          }
        >
          {t('roleChanges.retry')}
        </Button>
      )}
      {eligibility.isPending || grants.isPending || options.isPending ? (
        <p aria-busy="true">
          <Spinner /> {t('common.loading')}
        </p>
      ) : eligibility.isError || grants.isError || options.isError ? (
        <ProblemAlert
          page
          problem={problemOf(eligibility.error ?? grants.error ?? options.error)}
          actions={<Button onClick={() => void reload()}>{t('common.retry')}</Button>}
        />
      ) : !etagMatches ? (
        <div role="alert">
          <p>{t('roleChanges.versionChanged')}</p>
          <Button onClick={() => void reload()}>{t('roleChanges.reload')}</Button>
        </div>
      ) : (
        <>
          {noChecker && (
            <p role="status" className="border-line bg-surface mb-3 rounded-md border p-3 text-sm">
              {t('roleChanges.noChecker')}
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            {current?.canRequestAssignment && !command.command && (
              <Button
                onClick={() => {
                  command.dismissSuccess();
                  clearSelection();
                  setOpen('ASSIGN');
                }}
              >
                {t('roleChanges.proposeAssign')}
              </Button>
            )}
            {eligibleGrant && !command.command && (
              <Button
                variant="secondary"
                onClick={() => {
                  command.dismissSuccess();
                  clearSelection();
                  setRevokeGrant(eligibleGrant);
                  setOpen('REVOKE');
                }}
              >
                {t('roleChanges.proposeRevoke')}
              </Button>
            )}
          </div>
          {!current?.canRequestAssignment && !eligibleGrant && !current?.revokeGrantIds.length && (
            <p className="text-fg-muted text-sm">
              {current?.assignmentRefusalCode
                ? t(`problems.${current.assignmentRefusalCode}`, {
                    defaultValue: t('roleChanges.noAction'),
                  })
                : t('roleChanges.noAction')}
            </p>
          )}
          {current?.revokeGrantIds.length && !eligibleGrant ? (
            <div className="mt-3 grid gap-2 text-sm">
              <p className="text-fg-muted">
                {canNextGrant ? t('roleChanges.searchGrantPages') : t('roleChanges.reloadGrant')}
              </p>
              <nav className="flex gap-2" aria-label={t('roleChanges.grantPages')}>
                <Button
                  variant="secondary"
                  disabled={grantIndex === 0}
                  onClick={() => setGrantIndex(grantIndex - 1)}
                >
                  {t('adminUsers.previous')}
                </Button>
                <Button
                  variant="secondary"
                  disabled={!canNextGrant}
                  onClick={() => {
                    if (nextGrantCursor) {
                      setGrantCursors((old) => [...old.slice(0, grantIndex + 1), nextGrantCursor]);
                      setGrantIndex(grantIndex + 1);
                    }
                  }}
                >
                  {t('adminUsers.next')}
                </Button>
              </nav>
            </div>
          ) : null}
        </>
      )}
      <Dialog
        open={open !== null && !command.success}
        onOpenChange={(next) => {
          if (!next && !command.busy) setOpen(null);
        }}
        title={open === 'REVOKE' ? t('roleChanges.proposeRevoke') : t('roleChanges.proposeAssign')}
        description={t('roleChanges.confirmIntro', {
          name: detail.membership.displayName,
          tenant: tenantName,
        })}
      >
        <form onSubmit={submit} className="grid gap-4 text-sm">
          <p>
            {t('roleChanges.personAndTenant', {
              name: detail.membership.displayName,
              tenant: tenantName,
            })}
          </p>
          {open === 'ASSIGN' ? (
            <label className="grid gap-1">
              {t('roleChanges.role')}
              <Select
                value={roleCode}
                onChange={(event) => setRoleCode(event.target.value)}
                disabled={!!command.command || command.busy || command.conflict}
                required
                placeholder={t('roleChanges.chooseRole')}
                options={(options.data ?? []).map((item) => ({
                  value: item.code,
                  label: roleTitle(item.code, t),
                }))}
              />
            </label>
          ) : (
            <strong>{revokeGrant ? roleTitle(revokeGrant.roleCode, t) : ''}</strong>
          )}
          {(selected || revokeGrant) && (
            <div className="border-line bg-surface-sunken rounded-md border p-3">
              <p>
                {t('roleChanges.appAndScope', {
                  app: t('auth.apps.backoffice'),
                  tenant: tenantName,
                })}
              </p>
              <p>{roleDuty(selected?.code ?? revokeGrant!.roleCode, t)}</p>
              <p>
                {open === 'ASSIGN' ? t('roleChanges.assignEffect') : t('roleChanges.revokeEffect')}
              </p>
              <p>{t('roleChanges.secondPerson')}</p>
            </div>
          )}
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
              options={(open === 'ASSIGN'
                ? ['ONBOARDING', 'DUTY_ASSIGNMENT']
                : ['ACCESS_REVIEW', 'DUTY_ENDED', 'SECURITY_CONCERN']
              ).map((value) => ({ value, label: t(`roleChanges.reasons.${value}`) }))}
            />
          </label>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(event) => setConfirmed(event.target.checked)}
              disabled={!!command.command || command.busy || command.conflict}
            />
            <span>{t('roleChanges.confirmProposal')}</span>
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
                type="submit"
                loading={command.busy}
                disabled={
                  !command.command &&
                  (!confirmed || !reason || (open === 'ASSIGN' ? !selected : !revokeOption))
                }
              >
                {command.command ? t('roleChanges.retry') : t('roleChanges.submit')}
              </Button>
            )}
          </div>
        </form>
      </Dialog>
      <StepUpDialog
        open={command.stepUp.required}
        action={t('roleChanges.submit')}
        busy={command.stepUp.busy}
        problem={command.stepUp.error}
        onConfirm={(password) => void command.stepUp.confirm(password)}
        onCancel={command.stepUp.cancel}
      />
    </section>
  );
}
