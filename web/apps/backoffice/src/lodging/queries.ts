import type {
  BookingListQuery,
  CreateProperty,
  CreateRoomType,
  PutLodgingTermsRequest,
  ReviewNoShowRequest,
  WaitlistQuery,
} from '@kapsora/api-client';
import { ApiError } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

/**
 * The payer's half of the accommodation vertical: the properties its providers opened,
 * every booking as a sequence, the no-show reports a second person confirms, the waiting
 * list, and the lodging terms on a contract version. Every figure is the server's string.
 */

export function useProperties() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['lodging', tenantId, 'properties'],
    queryFn: () => ops.lodging.listProperties(tenantId, { limit: 100 }),
  });
}

export function useProperty(propertyId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['lodging', tenantId, 'property', propertyId],
    queryFn: () => ops.lodging.getProperty(tenantId, propertyId),
  });
}

export function useRoomTypes(propertyId: string | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['lodging', tenantId, 'room-types', propertyId],
    queryFn: () => ops.lodging.listRoomTypes(tenantId, propertyId!),
    enabled: propertyId !== null,
  });
}

export function useCreateProperty() {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateProperty) => ops.lodging.createProperty(tenantId, body),
    onSuccess: () => client.invalidateQueries({ queryKey: ['lodging', tenantId, 'properties'] }),
  });
}

export function useCreateRoomType(propertyId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateRoomType) => ops.lodging.createRoomType(tenantId, propertyId, body),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: ['lodging', tenantId, 'room-types', propertyId] }),
  });
}

/** Provider organizations, for the property form's one required choice. */
export function useProviderOrganizations() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['lodging', tenantId, 'provider-organizations'],
    queryFn: () => ops.organizations.list(tenantId, { role: 'PROVIDER', limit: 200 }),
  });
}

export function useBookings(query: BookingListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['lodging', tenantId, 'bookings', query],
    queryFn: () => ops.lodging.listBookings(tenantId, { limit: 100, ...query }),
  });
}

export function useBooking(bookingId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['lodging', tenantId, 'booking', bookingId],
    queryFn: () => ops.lodging.getBooking(tenantId, bookingId),
  });
}

/** The no-show report of a booking, or null when nobody reported one (a 404 is an answer). */
export function useNoShow(bookingId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['lodging', tenantId, 'no-show', bookingId],
    queryFn: async () => {
      try {
        return await ops.lodging.getNoShow(tenantId, bookingId);
      } catch (err) {
        if (err instanceof ApiError && err.problem.status === 404) return null;
        throw err;
      }
    },
    retry: false,
  });
}

export function useReviewNoShow(bookingId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: ReviewNoShowRequest) => ops.lodging.reviewNoShow(tenantId, bookingId, body),
    onSuccess: () =>
      Promise.all([
        client.invalidateQueries({ queryKey: ['lodging', tenantId, 'no-show', bookingId] }),
        client.invalidateQueries({ queryKey: ['lodging', tenantId, 'booking', bookingId] }),
        client.invalidateQueries({ queryKey: ['lodging', tenantId, 'bookings'] }),
      ]),
  });
}

export function useWaitlist(query: WaitlistQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['lodging', tenantId, 'waitlist', query],
    queryFn: () => ops.lodging.listWaitlist(tenantId, { limit: 100, ...query }),
  });
}

/** The version's lodging terms, or null while none are written (the server's 404). */
export function useLodgingTerms(contractVersionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['lodging', tenantId, 'terms', contractVersionId],
    queryFn: async () => {
      try {
        return await ops.lodging.lodgingTerms(tenantId, contractVersionId);
      } catch (err) {
        if (err instanceof ApiError && err.problem.status === 404) return null;
        throw err;
      }
    },
    retry: false,
  });
}

export function usePutLodgingTerms(contractVersionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ etag, body }: { etag: string; body: PutLodgingTermsRequest }) =>
      ops.lodging.putLodgingTerms(tenantId, contractVersionId, etag, body),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: ['lodging', tenantId, 'terms', contractVersionId] }),
  });
}
