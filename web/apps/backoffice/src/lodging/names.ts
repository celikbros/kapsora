import { useMemo } from 'react';

import { useProperties, useRoomTypes } from './queries';

/** Bookings and entries carry ids; the screens show names. Properties are one short list. */
export function usePropertyNames(): Map<string, string> {
  const properties = useProperties();
  return useMemo(
    () => new Map((properties.data?.items ?? []).map((p) => [p.id, p.name] as const)),
    [properties.data],
  );
}

export function useRoomTypeName(propertyId: string | null, roomTypeId: string | null): string {
  const rooms = useRoomTypes(propertyId);
  if (!rooms.data) return '…';
  return rooms.data.find((r) => r.id === roomTypeId)?.name ?? '—';
}
