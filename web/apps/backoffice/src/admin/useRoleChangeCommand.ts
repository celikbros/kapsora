import {
  ApiError,
  randomId,
  type CreateRoleChangeRequest,
  type RoleChangeCommandResult,
  type Versioned,
} from '@kapsora/api-client';
import { useSessionStore, useStepUp } from '@kapsora/auth';
import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { directoryContextKey, useOps } from '../api';
import { problemOf } from '../problems';

type Decision = 'approve' | 'reject' | 'cancel';
export type RoleChangeCommand = {
  context: string;
  csrfToken: string | null;
  revision: number;
  tenantId: string;
  selector: string;
  action: 'create' | Decision;
  body:
    | CreateRoleChangeRequest
    | Record<string, never>
    | { reasonCode: 'NOT_JUSTIFIED' | 'INCORRECT_ACCESS' | 'STALE_REQUEST' | 'WITHDRAWN' };
  etag: string;
  key: string;
};

/** One in-memory command snapshot survives uncertain replies and a closed confirmation dialog. */
export function useRoleChangeCommand() {
  const ops = useOps();
  const store = useSessionStore();
  const queryClient = useQueryClient();
  const stepUp = useStepUp();
  const stepUpRef = useRef(stepUp);
  useEffect(() => {
    stepUpRef.current = stepUp;
  }, [stepUp]);
  const [command, setCommand] = useState<RoleChangeCommand | null>(null);
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [cancelled, setCancelled] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [success, setSuccess] = useState<Versioned<RoleChangeCommandResult> | null>(null);
  const [refreshFailed, setRefreshFailed] = useState(false);
  const mounted = useRef(true);
  const revision = useRef(0);
  const inFlight = useRef<RoleChangeCommand | null>(null);

  useEffect(() => {
    mounted.current = true;
    let previous = directoryContextKey(store.getState());
    const unsubscribe = store.subscribe((state) => {
      const next = directoryContextKey(state);
      if (next === previous) return;
      previous = next;
      revision.current += 1;
      stepUpRef.current.cancel();
      inFlight.current = null;
      setCommand(null);
      setSuccess(null);
      setBusy(false);
      setUncertain(false);
      setCancelled(false);
      setConflict(false);
      setProblem(null);
      setRefreshFailed(false);
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [store]);

  const current = (snapshot: RoleChangeCommand) => {
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
  };

  async function send(snapshot: RoleChangeCommand) {
    if (inFlight.current || !current(snapshot)) return;
    inFlight.current = snapshot;
    setBusy(true);
    setProblem(null);
    setCancelled(false);
    try {
      const result = await stepUp.run(() => {
        if (!current(snapshot)) throw new Error('Authorization context changed');
        return snapshot.action === 'create'
          ? ops.admin.createRoleChange(
              snapshot.tenantId,
              snapshot.selector,
              snapshot.body as CreateRoleChangeRequest,
              snapshot.etag,
              snapshot.key,
            )
          : ops.admin.decideRoleChange(
              snapshot.tenantId,
              snapshot.selector,
              snapshot.action,
              snapshot.body as
                | Record<string, never>
                | {
                    reasonCode:
                      'NOT_JUSTIFIED' | 'INCORRECT_ACCESS' | 'STALE_REQUEST' | 'WITHDRAWN';
                  },
              snapshot.etag,
              snapshot.key,
            );
      });
      if (!current(snapshot)) return;
      if (!result) {
        // Password confirmation was cancelled before dispatch. Keep the frozen command
        // available for an explicit retry even if its confirmation dialog is closed.
        setCancelled(true);
        return;
      }
      setSuccess(result);
      setCommand(null);
      setUncertain(false);
      setCancelled(false);
      setConflict(false);
      setRefreshFailed(false);
      const refreshed = await Promise.allSettled([
        queryClient.invalidateQueries(
          { queryKey: ['admin-role-changes', snapshot.context] },
          { throwOnError: true },
        ),
        queryClient.invalidateQueries(
          { queryKey: ['admin-role-change-eligibility', snapshot.context] },
          { throwOnError: true },
        ),
        queryClient.invalidateQueries(
          { queryKey: ['admin-role-grants', snapshot.context] },
          { throwOnError: true },
        ),
        queryClient.invalidateQueries(
          { queryKey: ['admin-users', snapshot.context] },
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
        setCancelled(false);
      } else {
        setConflict(true);
        setCommand(null);
        setUncertain(false);
        setCancelled(false);
      }
    } finally {
      if (inFlight.current === snapshot) {
        inFlight.current = null;
        if (current(snapshot)) setBusy(false);
      }
    }
  }

  function begin(
    selector: string,
    action: RoleChangeCommand['action'],
    body: RoleChangeCommand['body'],
    etag: string,
  ) {
    if (inFlight.current || command || conflict || !/^".+"$/.test(etag)) return;
    const state = store.getState();
    if (state.status !== 'authenticated' || state.activeTenant?.canManageTenantRoles !== true)
      return;
    const snapshot: RoleChangeCommand = {
      context: directoryContextKey(state),
      csrfToken: state.csrfToken,
      revision: revision.current,
      tenantId: state.activeTenant.tenant.id,
      selector,
      action,
      body: Object.freeze(body),
      etag,
      key: randomId(),
    };
    setSuccess(null);
    setCommand(snapshot);
    void send(snapshot);
  }

  function afterReload() {
    if (busy) return;
    setConflict(false);
    setProblem(null);
  }

  return {
    command,
    busy,
    uncertain,
    cancelled,
    conflict,
    problem,
    success,
    refreshFailed,
    stepUp,
    begin,
    retry: () => {
      if (command) void send(command);
    },
    afterReload,
    dismissSuccess: () => {
      setSuccess(null);
      setRefreshFailed(false);
    },
  };
}
