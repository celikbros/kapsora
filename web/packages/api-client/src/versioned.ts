/** A resource together with the ETag the server returned for it. */
export interface Versioned<T> {
  data: T;
  etag: string;
}

/** Pairs a resource with its ETag. */
export function versioned<T>(data: T, response: Response): Versioned<T> {
  return { data, etag: response.headers.get('ETag') ?? '' };
}
