import type {
  AvailabilitySearchRequest,
  BookingStatus,
  CreateHoldRequest,
  JoinWaitlistRequest,
} from '@kapsora/api-client';
import { useSession, useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../services';

/**
 * The member's half of the accommodation vertical. The server resolves the person from the
 * account's PERSON scope, so nothing here names one: a member reads their own bookings by
 * asking for "bookings", and a body that named somebody else would be refused.
 */

const POLL_MS = 4_000;

/** The person this account acts for, from the tenant context; null before onboarding. */
export function useMyPersonId(): string | null {
  return useSession((s) => s.activeTenant?.personId ?? null);
}

export function useMyEntitlements(personId: string | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'entitlements', personId],
    queryFn: () => ops.entitlements.listPersonEntitlements(tenantId, personId!),
    enabled: personId !== null,
  });
}

export function useProperties() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'properties'],
    queryFn: () => ops.lodging.listProperties(tenantId, { status: 'ACTIVE', limit: 100 }),
  });
}

export function useRoomTypes(propertyId: string | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'room-types', propertyId],
    queryFn: () => ops.lodging.listRoomTypes(tenantId, propertyId!),
    enabled: propertyId !== null,
  });
}

export function useSearch() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (body: AvailabilitySearchRequest) => ops.lodging.search(tenantId, body),
  });
}

function useInvalidateBookings() {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return (bookingId?: string) =>
    Promise.all([
      client.invalidateQueries({ queryKey: ['member', tenantId, 'bookings'] }),
      client.invalidateQueries({ queryKey: ['member', tenantId, 'entitlements'] }),
      client.invalidateQueries({ queryKey: ['member', tenantId, 'waitlist'] }),
      bookingId
        ? client.invalidateQueries({ queryKey: ['member', tenantId, 'booking', bookingId] })
        : Promise.resolve(),
    ]);
}

export function useHold() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBookings();
  return useMutation({
    mutationFn: (body: CreateHoldRequest) => ops.lodging.hold(tenantId, body),
    onSuccess: () => invalidate(),
  });
}

export function useBookings(status?: BookingStatus) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'bookings', status ?? 'all'],
    queryFn: () =>
      ops.lodging.listBookings(tenantId, status ? { status, limit: 50 } : { limit: 50 }),
  });
}

/**
 * One booking, re-read while a decision is still on its way: a confirmation is decided
 * through the request pipeline, so a HOLD the member just confirmed and a PENDING_APPROVAL
 * both change without anybody clicking.
 */
export function useBooking(bookingId: string, watch = false) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'booking', bookingId],
    queryFn: () => ops.lodging.getBooking(tenantId, bookingId),
    refetchInterval: (query) => {
      const status = query.state.data?.data.status;
      return watch && (status === 'HOLD' || status === 'PENDING_APPROVAL') ? POLL_MS : false;
    },
  });
}

export function useConfirm(bookingId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBookings();
  return useMutation({
    mutationFn: () => ops.lodging.confirm(tenantId, bookingId),
    onSuccess: () => invalidate(bookingId),
  });
}

export function useRelease(bookingId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBookings();
  return useMutation({
    mutationFn: () => ops.lodging.release(tenantId, bookingId),
    onSuccess: () => invalidate(bookingId),
  });
}

/** The token comes back once and lives in the mutation's state until the screen lets go. */
export function useVoucher(bookingId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({ mutationFn: () => ops.lodging.voucher(tenantId, bookingId) });
}

export function useCancellationPreview(bookingId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({ mutationFn: () => ops.lodging.previewCancellation(tenantId, bookingId) });
}

export function useCancel(bookingId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBookings();
  return useMutation({
    mutationFn: () => ops.lodging.cancel(tenantId, bookingId),
    onSuccess: () => invalidate(bookingId),
  });
}

export function useWaitlist() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'waitlist'],
    queryFn: () => ops.lodging.listWaitlist(tenantId),
  });
}

export function useJoinWaitlist() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBookings();
  return useMutation({
    mutationFn: (body: JoinWaitlistRequest) => ops.lodging.joinWaitlist(tenantId, body),
    onSuccess: () => invalidate(),
  });
}

export function useAcceptOffer() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBookings();
  return useMutation({
    mutationFn: (entryId: string) => ops.lodging.acceptWaitlistOffer(tenantId, entryId),
    onSuccess: () => invalidate(),
  });
}

export function useLeaveWaitlist() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBookings();
  return useMutation({
    mutationFn: (entryId: string) => ops.lodging.cancelWaitlistEntry(tenantId, entryId),
    onSuccess: () => invalidate(),
  });
}
