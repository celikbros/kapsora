import {
  ApiError,
  randomId,
  type AssignTenantRoleGrantRequest,
  type TenantRoleGrant,
  type TenantRoleGrantResult,
  type TenantUserDetail,
  type Versioned,
} from '@kapsora/api-client';
import { useSession, useSessionStore, useStepUp, useTenantId } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Button,
  Dialog,
  EmptyState,
  ProblemAlert,
  Select,
  Spinner,
  StepUpDialog,
} from '@kapsora/ui';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { directoryContextKey, useOps } from '../api';
import { problemOf } from '../problems';

type AssignReason = AssignTenantRoleGrantRequest['reasonCode'];
type RevokeReason = 'ACCESS_REVIEW' | 'DUTY_ENDED' | 'SECURITY_CONCERN';
type Command = {
  context: string;
  csrfToken: string | null;
  revision: number;
  tenantId: string;
  membershipId: string;
  etag: string;
  key: string;
  operation: 'assign' | 'revoke';
  body: AssignTenantRoleGrantRequest | { reasonCode: RevokeReason };
  grantId?: string;
};

/** Grant IDs and mutation eligibility live only in this more strongly authorized read. */
export function RoleGrants({
  membershipId,
  detail,
  onReloadDetail,
}: {
  membershipId: string;
  detail: TenantUserDetail;
  onReloadDetail: () => Promise<boolean>;
}) {
  const { t } = useTranslation();
  const ops = useOps();
  const store = useSessionStore();
  const queryClient = useQueryClient();
  const stepUp = useStepUp();
  const stepUpRef = useRef(stepUp);
  useEffect(() => {
    stepUpRef.current = stepUp;
  }, [stepUp]);
  const tenantId = useTenantId();
  const context = useSession(directoryContextKey);
  const allowed = useSession((s) => s.activeTenant?.canManageTenantRoles === true);
  const tenantName = useSession((s) => s.activeTenant?.tenant.displayName ?? '');
  const [cursorTrail, setCursorTrail] = useState<string[]>(['']);
  const [contextEpoch, setContextEpoch] = useState(0);
  const [orgTrail, setOrgTrail] = useState<string[]>(['']);
  const [expectedListEtag, setExpectedListEtag] = useState<string | null>(null);
  const [open, setOpen] = useState<'assign' | 'revoke' | null>(null);
  const [selectedGrant, setSelectedGrant] = useState<TenantRoleGrant | null>(null);
  const [roleCode, setRoleCode] = useState('');
  const [organizationId, setOrganizationId] = useState('');
  const [reason, setReason] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const [command, setCommand] = useState<Command | null>(null);
  const [uncertain, setUncertain] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [success, setSuccess] = useState<Versioned<TenantRoleGrantResult> | null>(null);
  const [refreshFailed, setRefreshFailed] = useState(false);
  const [asOf] = useState(() => Date.now());
  const mounted = useRef(true);
  const inFlight = useRef<Command | null>(null);
  const revision = useRef(0);

  const listCursor = cursorTrail[cursorTrail.length - 1] ?? '';
  const orgCursor = orgTrail[orgTrail.length - 1] ?? '';
  const grants = useQuery({
    queryKey: ['admin-role-grants', context, contextEpoch, membershipId, listCursor],
    queryFn: () =>
      ops.admin.roleGrants(tenantId, membershipId, listCursor ? { cursor: listCursor } : {}),
    enabled: allowed,
    retry: false,
  });
  const options = useQuery({
    queryKey: ['admin-role-options', context, contextEpoch],
    queryFn: () => ops.admin.roleAssignmentOptions(tenantId),
    enabled: allowed,
    retry: false,
  });
  const selectedRole = options.data?.find((item) => item.code === roleCode);
  const organizations = useQuery({
    queryKey: ['admin-role-organizations', context, contextEpoch, orgCursor],
    queryFn: () =>
      ops.admin.roleAssignmentOrganizations(tenantId, orgCursor ? { cursor: orgCursor } : {}),
    enabled: allowed && open === 'assign' && selectedRole?.scopeType === 'ORGANIZATION',
    retry: false,
  });
  const selectedOrganization = organizations.data?.items.find((item) => item.id === organizationId);
  const pageChanged = !!expectedListEtag && !!grants.data && grants.data.etag !== expectedListEtag;

  useEffect(() => {
    mounted.current = true;
    let previous = directoryContextKey(store.getState());
    const unsubscribe = store.subscribe((state) => {
      const next = directoryContextKey(state);
      if (next === previous) return;
      previous = next;
      revision.current += 1;
      stepUpRef.current.cancel();
      setContextEpoch(revision.current);
      setOpen(null);
      setCommand(null);
      setSuccess(null);
      setProblem(null);
      setBusy(false);
      setUncertain(false);
      setConflict(false);
      setRefreshFailed(false);
      inFlight.current = null;
      setCursorTrail(['']);
      setOrgTrail(['']);
      setExpectedListEtag(null);
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [store]);

  function current(snapshot: Command) {
    const state = store.getState();
    return (
      mounted.current &&
      revision.current === snapshot.revision &&
      state.status === 'authenticated' &&
      state.csrfToken === snapshot.csrfToken &&
      state.activeTenant?.canManageTenantRoles === true &&
      state.activeTenant.tenant.id === snapshot.tenantId &&
      directoryContextKey(state) === snapshot.context
    );
  }

  function clearForm() {
    setRoleCode('');
    setOrganizationId('');
    setReason('');
    setConfirmed(false);
    setProblem(null);
    setConflict(false);
    setUncertain(false);
    setCommand(null);
    setOrgTrail(['']);
  }

  async function reload() {
    if (busy) return;
    const beforeContext = directoryContextKey(store.getState());
    const beforeRevision = revision.current;
    const beforeMembership = membershipId;
    const [list, user] = await Promise.all([grants.refetch(), onReloadDetail()]);
    if (
      !mounted.current ||
      beforeRevision !== revision.current ||
      beforeContext !== directoryContextKey(store.getState()) ||
      beforeMembership !== membershipId ||
      !user ||
      !list.isSuccess
    )
      return;
    setCursorTrail(['']);
    setExpectedListEtag(null);
    clearForm();
  }

  async function send(snapshot: Command) {
    if (inFlight.current || !current(snapshot)) return;
    inFlight.current = snapshot;
    setBusy(true);
    setProblem(null);
    try {
      const result = await stepUp.run(() => {
        if (!current(snapshot)) throw new Error('Authorization context changed');
        return snapshot.operation === 'assign'
          ? ops.admin.assignRoleGrant(
              snapshot.tenantId,
              snapshot.membershipId,
              snapshot.body as AssignTenantRoleGrantRequest,
              snapshot.etag,
              snapshot.key,
            )
          : ops.admin.revokeRoleGrant(
              snapshot.tenantId,
              snapshot.membershipId,
              snapshot.grantId!,
              snapshot.body as { reasonCode: RevokeReason },
              snapshot.etag,
              snapshot.key,
            );
      });
      if (!current(snapshot) || !result) return;
      // The returned command result is authoritative even if subsequent reads fail.
      setSuccess(result);
      setRefreshFailed(false);
      setCommand(null);
      setUncertain(false);
      setOpen(null);
      setCursorTrail(['']);
      setExpectedListEtag(null);
      const refreshed = await Promise.allSettled([
        queryClient.invalidateQueries(
          { queryKey: ['admin-role-grants', snapshot.context] },
          { throwOnError: true },
        ),
        queryClient.invalidateQueries(
          {
            queryKey: ['admin-users', snapshot.context, 'detail', snapshot.membershipId],
          },
          { throwOnError: true },
        ),
        queryClient.invalidateQueries(
          { queryKey: ['admin-users', snapshot.context, 'list'] },
          { throwOnError: true },
        ),
      ]);
      if (current(snapshot)) setRefreshFailed(refreshed.some((item) => item.status === 'rejected'));
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
        setUncertain(true);
      } else {
        setConflict(true);
        setCommand(null);
        setUncertain(false);
      }
    } finally {
      if (inFlight.current === snapshot) {
        inFlight.current = null;
        if (current(snapshot)) setBusy(false);
      }
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || conflict) return;
    if (command) {
      void send(command);
      return;
    }
    if (pageChanged) return;
    if (!confirmed || !reason || !grants.data?.etag || !/^".+"$/.test(grants.data.etag)) return;
    const state = store.getState();
    if (state.activeTenant?.canManageTenantRoles !== true) return;
    let body: Command['body'];
    let grantId: string | undefined;
    if (open === 'assign' && grants.data.data.canAssign && selectedRole) {
      if (selectedRole.scopeType === 'ORGANIZATION' && !selectedOrganization) return;
      body = {
        roleCode: selectedRole.code,
        scopeType: selectedRole.scopeType,
        ...(selectedRole.scopeType === 'ORGANIZATION'
          ? { organizationRelationshipId: selectedOrganization!.id }
          : {}),
        reasonCode: reason as AssignReason,
      };
    } else if (open === 'revoke' && selectedGrant?.canRevoke) {
      grantId = selectedGrant.id;
      body = { reasonCode: reason as RevokeReason };
    } else return;
    const snapshot: Command = {
      context: directoryContextKey(state),
      csrfToken: state.csrfToken,
      revision: revision.current,
      tenantId: state.activeTenant.tenant.id,
      membershipId,
      etag: grants.data.etag,
      key: randomId(),
      operation: open,
      body: Object.freeze(body),
      ...(grantId ? { grantId } : {}),
    };
    setCommand(snapshot);
    void send(snapshot);
  }

  if (!allowed) return null;
  const list = grants.data?.data;
  const eligible = !!list?.canAssign && !pageChanged;
  const selectedDescription = selectedRole?.description ?? '';
  const displayGrant = (grant: TenantRoleGrant) => {
    if (grant.validityEmpty) return t('adminUsers.emptyValidity');
    if (grant.validTo && Date.parse(grant.validTo) <= asOf) return t('adminUsers.roleGrants.ended');
    if (grant.validFrom && Date.parse(grant.validFrom) > asOf)
      return t('adminUsers.roleGrants.future');
    return t('adminUsers.roleGrants.active');
  };
  return (
    <section className="mt-6" data-testid="role-grants">
      <h2 className="text-lg font-semibold">{t('adminUsers.roleGrants.title')}</h2>
      <p className="text-fg-muted mb-3 text-sm">{t('adminUsers.roleGrants.intro')}</p>
      {success && (
        <p role="status" className="border-line bg-surface mb-3 rounded-md border p-3 text-sm">
          {t(
            success.data.grant.validTo
              ? 'adminUsers.roleGrants.revokeDone'
              : 'adminUsers.roleGrants.assignDone',
          )}
        </p>
      )}
      {refreshFailed && (
        <p role="alert" className="text-danger mb-3 text-sm">
          {t('adminUsers.roleGrants.refreshFailed')}
        </p>
      )}
      {command && uncertain && !open && (
        <Button onClick={() => setOpen(command.operation)}>
          {t('adminUsers.roleGrants.retry')}
        </Button>
      )}
      {grants.isPending ? (
        <p aria-busy="true">
          <Spinner /> {t('common.loading')}
        </p>
      ) : grants.isError ? (
        <ProblemAlert
          page
          problem={problemOf(grants.error)}
          actions={
            <Button variant="secondary" onClick={() => void grants.refetch()}>
              {t('common.retry')}
            </Button>
          }
        />
      ) : pageChanged ? (
        <div role="alert">
          <p>{t('adminUsers.roleGrants.versionChanged')}</p>
          <Button onClick={() => void reload()}>{t('adminUsers.roleGrants.reload')}</Button>
        </div>
      ) : (
        <>
          {detail.membership.membershipStatus === 'ACTIVE' && eligible && !command && (
            <Button
              onClick={() => {
                clearForm();
                setOpen('assign');
              }}
            >
              {t('adminUsers.roleGrants.assign')}
            </Button>
          )}
          {!eligible && (
            <p className="text-fg-muted text-sm">
              {list?.assignmentRefusalCode
                ? t(`problems.${list.assignmentRefusalCode}`, {
                    defaultValue: t('adminUsers.roleGrants.canAssignNo'),
                  })
                : t('adminUsers.roleGrants.canAssignNo')}
            </p>
          )}
          {list?.items.length ? (
            <ul className="mt-3 grid gap-2">
              {list.items.map((grant) => (
                <li
                  key={grant.id}
                  className="border-line bg-surface grid min-w-0 gap-2 rounded-lg border p-4 text-sm sm:grid-cols-[1fr_auto]"
                >
                  <div className="min-w-0 break-words">
                    <strong>{grant.roleName}</strong>
                    <p className="text-fg-muted">
                      {t(`adminUsers.scopes.${grant.scopeType}`, { defaultValue: grant.scopeType })}
                      {grant.organizationDisplayName ? ` · ${grant.organizationDisplayName}` : ''}
                    </p>
                    <p>
                      {displayGrant(grant)} ·{' '}
                      {grant.validFrom ? formatDateTime(grant.validFrom) : t('adminUsers.noStart')}{' '}
                      — {grant.validTo ? formatDateTime(grant.validTo) : t('adminUsers.noEnd')}
                    </p>
                  </div>
                  {grant.canRevoke && !command ? (
                    <Button
                      variant="danger"
                      onClick={() => {
                        clearForm();
                        setSelectedGrant(grant);
                        setOpen('revoke');
                      }}
                    >
                      {t('adminUsers.roleGrants.remove')}
                    </Button>
                  ) : (
                    <span className="text-fg-muted">{t('adminUsers.roleGrants.unsupported')}</span>
                  )}
                </li>
              ))}
            </ul>
          ) : (
            <EmptyState title={t('adminUsers.roleGrants.noGrants')} />
          )}
          <div className="mt-3 flex gap-2">
            <Button
              variant="secondary"
              disabled={cursorTrail.length === 1}
              onClick={() => setCursorTrail((trail) => trail.slice(0, -1))}
            >
              {t('adminUsers.previous')}
            </Button>
            <Button
              variant="secondary"
              disabled={!list?.nextCursor}
              onClick={() => {
                setExpectedListEtag(grants.data!.etag);
                setCursorTrail((trail) => [...trail, list!.nextCursor!]);
              }}
            >
              {t('adminUsers.next')}
            </Button>
          </div>
        </>
      )}
      <Dialog
        open={open !== null}
        onOpenChange={(next) => {
          if (!next && !busy) setOpen(null);
        }}
        title={t(
          open === 'revoke' ? 'adminUsers.roleGrants.remove' : 'adminUsers.roleGrants.assign',
        )}
        description={t(
          open === 'revoke'
            ? 'adminUsers.roleGrants.revokeImpact'
            : 'adminUsers.roleGrants.assignImpact',
          { name: detail.membership.displayName, tenant: tenantName },
        )}
      >
        <form className="grid gap-4" onSubmit={submit}>
          {open === 'assign' ? (
            <>
              <div className="grid gap-1 text-sm">
                <label htmlFor="role-grant-role">{t('adminUsers.roleGrants.role')}</label>
                <Select
                  id="role-grant-role"
                  value={roleCode}
                  disabled={!!command || conflict || busy}
                  placeholder={t('adminUsers.roleGrants.chooseRole')}
                  options={(options.data ?? []).map((item) => ({
                    value: item.code,
                    label: item.name,
                  }))}
                  onChange={(event) => {
                    setRoleCode(event.target.value);
                    setOrganizationId('');
                    setOrgTrail(['']);
                  }}
                  required
                />
              </div>
              {options.isError && <ProblemAlert problem={problemOf(options.error)} />}
              {options.data?.length === 0 && <p>{t('adminUsers.roleGrants.noOptions')}</p>}
              {selectedRole && (
                <div className="border-line bg-surface-sunken rounded-md border p-3 text-sm">
                  <p>{t('adminUsers.roleGrants.tenant', { name: tenantName })}</p>
                  <p>
                    {t('adminUsers.roleGrants.app', {
                      name: t(
                        selectedRole.scopeType === 'TENANT'
                          ? 'auth.apps.backoffice'
                          : 'auth.apps.provider',
                      ),
                    })}
                  </p>
                  <p>{t('adminUsers.roleGrants.access', { description: selectedDescription })}</p>
                  {selectedRole.hasSensitivePermissions && (
                    <p>{t('adminUsers.roleGrants.sensitive')}</p>
                  )}
                  <p>{t('adminUsers.roleGrants.duration')}</p>
                </div>
              )}
              {selectedRole?.scopeType === 'ORGANIZATION' && (
                <div className="grid gap-2">
                  <div className="grid gap-1 text-sm">
                    <label htmlFor="role-grant-organization">
                      {t('adminUsers.roleGrants.organization')}
                    </label>
                    <Select
                      id="role-grant-organization"
                      value={organizationId}
                      disabled={!!command || conflict || busy}
                      placeholder={t('adminUsers.roleGrants.chooseOrganization')}
                      options={(organizations.data?.items ?? []).map((item) => ({
                        value: item.id,
                        label: `${item.displayName}${item.tenantCode ? ` (${item.tenantCode})` : ''}`,
                      }))}
                      onChange={(event) => setOrganizationId(event.target.value)}
                      required
                    />
                  </div>
                  {organizations.isError && (
                    <ProblemAlert problem={problemOf(organizations.error)} />
                  )}
                  {organizations.data?.items.length === 0 && (
                    <p>{t('adminUsers.roleGrants.noOrganizations')}</p>
                  )}
                  <div className="flex gap-2">
                    <Button
                      variant="secondary"
                      disabled={orgTrail.length === 1 || !!command}
                      onClick={() => {
                        setOrganizationId('');
                        setOrgTrail((trail) => trail.slice(0, -1));
                      }}
                    >
                      {t('adminUsers.previous')}
                    </Button>
                    <Button
                      variant="secondary"
                      disabled={!organizations.data?.nextCursor || !!command}
                      onClick={() => {
                        setOrganizationId('');
                        setOrgTrail((trail) => [...trail, organizations.data!.nextCursor!]);
                      }}
                    >
                      {t('adminUsers.next')}
                    </Button>
                  </div>
                  {selectedOrganization && (
                    <p>
                      {t('adminUsers.roleGrants.organization')}: {selectedOrganization.displayName}
                    </p>
                  )}
                </div>
              )}
            </>
          ) : (
            selectedGrant && (
              <div className="border-line bg-surface-sunken rounded-md border p-3 text-sm">
                <strong>{selectedGrant.roleName}</strong>
                <p>{t('adminUsers.roleGrants.tenant', { name: tenantName })}</p>
                {selectedGrant.organizationDisplayName && (
                  <p>
                    {t('adminUsers.roleGrants.organization')}:{' '}
                    {selectedGrant.organizationDisplayName}
                  </p>
                )}
              </div>
            )
          )}
          <div className="grid gap-1 text-sm">
            <label htmlFor="role-grant-reason">{t('adminUsers.roleGrants.reason')}</label>
            <Select
              id="role-grant-reason"
              value={command?.body.reasonCode ?? reason}
              disabled={!!command || conflict || busy}
              placeholder={t('adminUsers.roleGrants.chooseReason')}
              options={(open === 'assign'
                ? ['ONBOARDING', 'DUTY_ASSIGNMENT']
                : ['ACCESS_REVIEW', 'DUTY_ENDED', 'SECURITY_CONCERN']
              ).map((value) => ({
                value,
                label: t(
                  `adminUsers.roleGrants.${open === 'assign' ? 'assignReasons' : 'revokeReasons'}.${value}`,
                ),
              }))}
              onChange={(event) => setReason(event.target.value)}
              required
            />
          </div>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={confirmed}
              disabled={!!command || conflict || busy}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            <span>{t('adminUsers.roleGrants.confirm')}</span>
          </label>
          <ProblemAlert problem={problem} hideFieldErrors />
          {uncertain && (
            <p role="status" className="text-fg-muted text-sm">
              {t('adminUsers.roleGrants.uncertain')}
            </p>
          )}
          {conflict && (
            <p role="status" className="text-fg-muted text-sm">
              {t('adminUsers.roleGrants.conflict')}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setOpen(null)} disabled={busy}>
              {t('common.cancel')}
            </Button>
            {conflict ? (
              <Button onClick={() => void reload()}>{t('adminUsers.roleGrants.reload')}</Button>
            ) : (
              <Button
                type="submit"
                variant={open === 'revoke' ? 'danger' : 'primary'}
                loading={busy}
                disabled={
                  !command &&
                  (!confirmed ||
                    !reason ||
                    (open === 'assign' &&
                      (!selectedRole ||
                        (selectedRole.scopeType === 'ORGANIZATION' && !selectedOrganization))))
                }
              >
                {t(
                  command
                    ? 'adminUsers.roleGrants.retry'
                    : open === 'revoke'
                      ? 'adminUsers.roleGrants.remove'
                      : 'adminUsers.roleGrants.assign',
                )}
              </Button>
            )}
          </div>
        </form>
      </Dialog>
      <StepUpDialog
        open={stepUp.required}
        action={t(
          open === 'revoke' ? 'adminUsers.roleGrants.remove' : 'adminUsers.roleGrants.assign',
        )}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </section>
  );
}
