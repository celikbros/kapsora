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

/**
 * Zod issues carry an i18n code, sometimes with an argument: "MAX_LENGTH:100". This splits
 * one into the key and the interpolation parameters `fieldErrorMessage` expects.
 */
export function parseIssueMessage(message: string): {
  code: string;
  params: Record<string, unknown>;
} {
  const [code, argument] = message.split(':');
  if (!code) return { code: 'FORMAT', params: {} };
  if (code === 'MIN_LENGTH') return { code, params: { min: Number(argument) } };
  if (code === 'MAX_LENGTH') return { code, params: { max: Number(argument) } };
  return { code, params: {} };
}
