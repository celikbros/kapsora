import {
  randomId,
  type CreateServiceRequest,
  type Operations,
  type ServiceRequest,
  type Versioned,
} from '@kapsora/api-client';

type Requests = Pick<Operations['requests'], 'create' | 'submit'>;
type Result = Versioned<ServiceRequest>;
interface Attempt {
  body: CreateServiceRequest;
  createKey: string;
  submitKey: string;
  draft?: Result;
  result?: Result;
  inFlight?: Promise<Result>;
}

/** One form session. An uncertain response is retried with the same command, not a new draft. */
export function createRequestSubmission(requests: Requests, actorId = '') {
  const attempts = new Map<string, Attempt>();
  return (tenantId: string, body: CreateServiceRequest): Promise<Result> => {
    const signature = JSON.stringify([actorId, tenantId, body]);
    let attempt = attempts.get(signature);
    if (!attempt) {
      attempt = { body: structuredClone(body), createKey: randomId(), submitKey: randomId() };
      attempts.set(signature, attempt);
    }
    if (attempt.result) return Promise.resolve(attempt.result);
    if (attempt.inFlight) return attempt.inFlight;
    const current = attempt;
    const run = async () => {
      current.draft ??= await requests.create(tenantId, current.body, current.createKey);
      current.result = await requests.submit(
        tenantId,
        current.draft.data.id,
        current.draft.etag,
        current.submitKey,
      );
      return current.result;
    };
    current.inFlight = run().finally(() => {
      delete current.inFlight;
    });
    return current.inFlight;
  };
}
