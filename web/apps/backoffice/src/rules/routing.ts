/**
 * The shape of the rule list's search, and the two loose reads of it. Navigation itself
 * goes through the router's own Link and useNavigate, which check the path.
 */
import type { RuleSetPurpose } from '@kapsora/api-client';
import { useParams, useSearch } from '@tanstack/react-router';

/** Search of the rule set list; `cursor` is the server's opaque keyset cursor. */
export interface RuleSetListSearch {
  q?: string;
  purpose?: RuleSetPurpose;
  cursor?: string;
}

/** Path parameters of the current match, whichever rule route it is. */
export function useRuleParams(): Record<string, string | undefined> {
  return useParams({ strict: false }) as unknown as Record<string, string | undefined>;
}

export function useRuleSetListSearch(): RuleSetListSearch {
  return useSearch({ strict: false }) as unknown as RuleSetListSearch;
}
