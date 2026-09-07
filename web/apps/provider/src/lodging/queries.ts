import type {
  BookingStatus,
  CheckInBookingRequest,
  PutRoomTypeInventory,
  ReportNoShowRequest,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../services';

/**
 * The property desk's half of the accommodation vertical. Every read is scoped by the
 * server to the desk's own organization — its properties, their room types, the bookings at
 * them — and nothing is filtered on the client. Money and counts are shown as the server
 * sent them.
 */

export function useProperties() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'lodging', 'properties'],
    queryFn: () => ops.lodging.listProperties(tenantId, { limit: 100 }),
  });
}

export function useRoomTypes(propertyId: string | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'lodging', 'room-types', propertyId],
    queryFn: () => ops.lodging.listRoomTypes(tenantId, propertyId!),
    enabled: propertyId !== null,
  });
}

export function useInventory(roomTypeId: string | null, from: string, to: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'lodging', 'inventory', roomTypeId, from, to],
    queryFn: () => ops.lodging.getInventory(tenantId, roomTypeId!, from, to),
    enabled: roomTypeId !== null && from !== '' && to !== '',
  });
}

export function usePutInventory(roomTypeId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: PutRoomTypeInventory) =>
      ops.lodging.putInventory(tenantId, roomTypeId, body),
    onSuccess: () =>
      client.invalidateQueries({
        queryKey: ['provider', tenantId, 'lodging', 'inventory', roomTypeId],
      }),
  });
}

export function useBookings(status?: BookingStatus, propertyId?: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'lodging', 'bookings', status ?? 'all', propertyId ?? 'all'],
    queryFn: () =>
      ops.lodging.listBookings(tenantId, {
        limit: 100,
        ...(status ? { status } : {}),
        ...(propertyId ? { propertyId } : {}),
      }),
  });
}

function useInvalidateBookings() {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return () =>
    client.invalidateQueries({ queryKey: ['provider', tenantId, 'lodging', 'bookings'] });
}

export function useCheckIn() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBookings();
  return useMutation({
    mutationFn: ({ bookingId, body }: { bookingId: string; body: CheckInBookingRequest }) =>
      ops.lodging.checkIn(tenantId, bookingId, body),
    onSuccess: () => invalidate(),
  });
}

export function useCheckOut() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBookings();
  return useMutation({
    mutationFn: (bookingId: string) => ops.lodging.checkOut(tenantId, bookingId),
    onSuccess: () => invalidate(),
  });
}

export function useReportNoShow() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBookings();
  return useMutation({
    mutationFn: ({ bookingId, body }: { bookingId: string; body: ReportNoShowRequest }) =>
      ops.lodging.reportNoShow(tenantId, bookingId, body),
    onSuccess: () => invalidate(),
  });
}
