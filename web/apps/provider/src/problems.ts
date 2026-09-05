import { ApiError, type Problem } from '@kapsora/api-client';

/** Problem document for any thrown value; non-API errors become the generic UNKNOWN. */
export function problemOf(err: unknown): Problem {
  if (err instanceof ApiError) return err.problem;
  return {
    type: 'about:blank',
    code: 'UNKNOWN',
    title: err instanceof Error ? err.message : '',
    status: 0,
    traceId: '',
  };
}
