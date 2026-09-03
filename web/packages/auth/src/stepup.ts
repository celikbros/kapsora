import { ApiError } from '@kapsora/api-client';
import { useCallback, useState } from 'react';

import { useSession, useSessionStore } from './hooks';

/**
 * Returns a predicate that says whether the step-up window is still open. It reads the
 * clock when called, not while rendering, so a component never renders from a value that
 * silently expires between paints.
 */
export function useStepUpValid(): () => boolean {
  const expiresAt = useSession((s) => s.session?.stepUpExpiresAt ?? null);
  return useCallback(() => expiresAt !== null && Date.parse(expiresAt) > Date.now(), [expiresAt]);
}

/** What `useStepUp` hands the screen. */
export interface StepUp {
  /** True while the dialog should be open. */
  required: boolean;
  /**
   * Runs the guarded action. When the server asks for a password re-entry the returned
   * promise stays pending until the retry finishes, so the caller keeps its result.
   * Resolves undefined only if the operator cancels.
   */
  run: <T>(action: () => Promise<T>) => Promise<T | undefined>;
  /** Confirms with the password, then retries the pending action once. */
  confirm: (password: string) => Promise<void>;
  /** Closes the dialog and abandons the pending action. */
  cancel: () => void;
  /** Set while the confirmation is in flight. */
  busy: boolean;
  /** The problem of a failed confirmation, for the dialog to render. */
  error: ApiError['problem'] | null;
}

/** The action waiting behind the dialog, with the promise `run` handed its caller. */
interface Pending {
  action: () => Promise<unknown>;
  resolve: (value: unknown) => void;
  reject: (error: unknown) => void;
}

function toProblem(error: unknown): ApiError['problem'] {
  if (error instanceof ApiError) return error.problem;
  return { type: 'about:blank', code: 'UNKNOWN', title: '', status: 0, traceId: '' };
}

function isStepUpRefusal(error: unknown): boolean {
  return error instanceof ApiError && error.problem.code === 'STEP_UP_REQUIRED';
}

/**
 * Wraps an action the server may refuse with 403 STEP_UP_REQUIRED: the screen calls `run`
 * and awaits it, the dialog opens, takes the password and retries the same action once.
 * The awaited value is the retry's own result, so the screen can act on what came back.
 * The password never leaves the dialog's state.
 */
export function useStepUp(): StepUp {
  const store = useSessionStore();
  const [pending, setPending] = useState<Pending | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError['problem'] | null>(null);

  const run = useCallback(async <T>(action: () => Promise<T>): Promise<T | undefined> => {
    try {
      return await action();
    } catch (err) {
      if (!isStepUpRefusal(err)) throw err;
      setError(null);
      return await new Promise<T | undefined>((resolve, reject) => {
        setPending({ action, resolve: resolve as (value: unknown) => void, reject });
      });
    }
  }, []);

  const confirm = useCallback(
    async (password: string) => {
      const current = pending;
      setBusy(true);
      setError(null);
      try {
        await store.stepUp(password);
      } catch (err) {
        // A wrong password leaves the dialog open with the reason on it.
        setError(toProblem(err));
        setBusy(false);
        return;
      }
      setBusy(false);
      setPending(null);
      if (!current) return;
      try {
        current.resolve(await current.action());
      } catch (err) {
        current.reject(err);
      }
    },
    [pending, store],
  );

  const cancel = useCallback(() => {
    // Cancelling resolves undefined rather than throwing: the caller treats it as "no result".
    pending?.resolve(undefined);
    setPending(null);
    setError(null);
  }, [pending]);

  return { required: pending !== null, run, confirm, cancel, busy, error };
}
