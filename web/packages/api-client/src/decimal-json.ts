/**
 * Preserve decimal number tokens from JSON before JSON.parse can round them.
 * The API's numeric fields are JSON numbers even though the UI works with exact text.
 * Only named quantity properties are quoted; ids, counts and versions remain numbers.
 */
export function parseDecimalJson<T>(raw: string, fields: ReadonlySet<string>): T {
  const token = /("(?:\\.|[^"\\])*")|(-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/g;
  let key = '';
  let keyEnd = -1;
  const rewritten = raw.replace(
    token,
    (match, quoted: string | undefined, number: string | undefined, offset: number) => {
      if (quoted !== undefined) {
        if (/^\s*:/.test(raw.slice(offset + match.length))) {
          key = JSON.parse(quoted) as string;
          keyEnd = offset + match.length;
        }
        return match;
      }
      if (number !== undefined && fields.has(key) && /^\s*:\s*$/.test(raw.slice(keyEnd, offset))) {
        return JSON.stringify(number);
      }
      return match;
    },
  );
  return JSON.parse(rewritten) as T;
}
