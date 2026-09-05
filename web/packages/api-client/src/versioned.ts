/** A resource together with the ETag the server returned for it. */
export interface Versioned<T> {
  data: T;
  etag: string;
}

/** Pairs a resource with its ETag. */
export function versioned<T>(data: T, response: Response): Versioned<T> {
  return { data, etag: response.headers.get('ETag') ?? '' };
}

/**
 * The ETag the server would have answered for a row at this version. List endpoints carry
 * no per-row ETag, so a screen acting on a row it read from a list builds the header
 * from `rowVersion` — in the server's own quoted form, never as a bare number, which the
 * If-Match parser refuses.
 */
export function etagOf(rowVersion: number): string {
  return `"${rowVersion}"`;
}
