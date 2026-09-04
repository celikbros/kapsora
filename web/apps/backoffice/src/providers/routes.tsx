import type { ProviderStatus, ProviderType } from '@kapsora/api-client';
import { Link, useNavigate, type LinkProps } from '@tanstack/react-router';
import type { ReactNode } from 'react';

import { PROVIDER_STATUSES, PROVIDER_TYPES } from './schema';

/** What the provider list keeps in the URL. The cursor is the server's opaque token. */
export interface ProviderListSearch {
  q?: string;
  providerType?: ProviderType;
  networkTier?: string;
  status?: ProviderStatus;
  cursor?: string;
}

/** Search validator for the list route; `router.tsx` passes it to `validateSearch`. */
export function providerListSearch(raw: Record<string, unknown>): ProviderListSearch {
  const out: ProviderListSearch = {};
  const providerType = raw['providerType'];
  if (
    typeof providerType === 'string' &&
    (PROVIDER_TYPES as readonly string[]).includes(providerType)
  ) {
    out.providerType = providerType as ProviderType;
  }
  const status = raw['status'];
  if (typeof status === 'string' && (PROVIDER_STATUSES as readonly string[]).includes(status)) {
    out.status = status as ProviderStatus;
  }
  if (typeof raw['q'] === 'string' && raw['q'] !== '') out.q = raw['q'];
  if (typeof raw['networkTier'] === 'string' && raw['networkTier'] !== '') {
    out.networkTier = raw['networkTier'];
  }
  if (typeof raw['cursor'] === 'string' && raw['cursor'] !== '') out.cursor = raw['cursor'];
  return out;
}

/** The three destinations these screens navigate between. */
export type ProviderTarget =
  | { to: '/providers'; search: ProviderListSearch }
  | { to: '/providers/new' }
  | { to: '/providers/$providerId'; params: { providerId: string } };

/**
 * The provider destinations, named once so the screens cannot drift from the routes.
 * These now pass through the router's own types: the paths are registered, so a wrong
 * path or a missing parameter is a compile error again.
 */
function asLinkProps(target: ProviderTarget): LinkProps {
  return target;
}

export function useProviderNavigate(): (target: ProviderTarget) => Promise<void> {
  const navigate = useNavigate();
  return (target) => navigate(target);
}

export interface ProviderLinkProps {
  target: ProviderTarget;
  className?: string | undefined;
  children: ReactNode;
}

/** Router link to one of the provider screens. */
export function ProviderLink({ target, className, children }: ProviderLinkProps) {
  return (
    <Link {...asLinkProps(target)} className={className}>
      {children}
    </Link>
  );
}
