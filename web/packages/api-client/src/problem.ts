import type { components } from './generated/kapsora-v1';

/** RFC 9457 problem document as the contract defines it (ADR-015). */
export type Problem = components['schemas']['Problem'];
/** One field error inside a VALIDATION_FAILED problem. */
export type FieldError = NonNullable<Problem['errors']>[number];

/** Error thrown by every operation when the API answers with a problem document. */
export class ApiError extends Error {
  readonly problem: Problem;
  readonly status: number;

  constructor(problem: Problem) {
    super(`${problem.code}: ${problem.title}`);
    this.name = 'ApiError';
    this.problem = problem;
    this.status = problem.status;
  }

  /** Field errors keyed by field path; empty for non-validation problems. */
  fieldErrors(): Map<string, FieldError> {
    const out = new Map<string, FieldError>();
    for (const e of this.problem.errors ?? []) {
      out.set(e.field, e);
    }
    return out;
  }
}

/** Code used when the server could not be reached at all. */
export const NETWORK_ERROR = 'NETWORK_ERROR';

function isProblem(value: unknown): value is Problem {
  if (typeof value !== 'object' || value === null) {
    return false;
  }
  const v = value as Record<string, unknown>;
  return (
    typeof v['code'] === 'string' &&
    typeof v['status'] === 'number' &&
    typeof v['title'] === 'string'
  );
}

/**
 * Normalises whatever openapi-fetch handed back on a non-2xx response into a Problem.
 * A body that is not a problem document (a proxy error page, an empty 502) still yields
 * a Problem so the UI has one rendering path.
 */
export function toProblem(error: unknown, response: Response): Problem {
  if (isProblem(error)) {
    return error;
  }
  return {
    type: 'about:blank',
    title: response.statusText || `HTTP ${response.status}`,
    status: response.status,
    code: `HTTP_${response.status}`,
    traceId: response.headers.get('X-Request-ID') ?? '',
    instance: response.url,
  };
}

/** Problem for a fetch that never produced a response. */
export function networkProblem(cause: unknown): Problem {
  return {
    type: 'about:blank',
    title: cause instanceof Error ? cause.message : 'Network error',
    status: 0,
    code: NETWORK_ERROR,
    traceId: '',
  };
}

/** Result shape of openapi-fetch calls, narrowed to what unwrap needs. */
interface FetchResult<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** Turns an openapi-fetch result into data or throws ApiError. */
export async function unwrap<T>(
  call: Promise<FetchResult<T>>,
): Promise<{ data: T; response: Response }> {
  let result: FetchResult<T>;
  try {
    result = await call;
  } catch (cause) {
    throw new ApiError(networkProblem(cause));
  }
  if (result.error !== undefined || !result.response.ok) {
    throw new ApiError(toProblem(result.error, result.response));
  }
  return { data: result.data as T, response: result.response };
}
