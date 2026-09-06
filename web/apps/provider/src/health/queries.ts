import type {
  CodeValue,
  CreateEncounter,
  CreateHealthCase,
  CreateInpatientStay,
  CreateMedicalReport,
  DischargeInpatientStay,
  ExtendInpatientStay,
  PatchMedicalReportDraft,
  PutEncounterDiagnoses,
  PutMedicalReportServices,
  PutStaySegments,
} from '@kapsora/api-client';
import { etagOf } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../services';

/**
 * The provider's half of the health vertical. Every read here is scoped by the server to
 * the actor's own organization; nothing is filtered on the client. A clinical field — a
 * diagnosis, a branch, a note, a summary — arrives or it does not, and the screens render
 * what arrived: the `projection` on each record says which half came.
 */

const ICD10 = 'ICD10';

export function useCases(status: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'cases', status],
    queryFn: () =>
      ops.health.listCases(tenantId, status ? { status: status as 'OPEN' | 'CLOSED' } : {}),
  });
}

export function useCase(caseId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'case', caseId],
    queryFn: () => ops.health.getCase(tenantId, caseId),
  });
}

function useInvalidateCase() {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return (caseId?: string) =>
    Promise.all([
      client.invalidateQueries({ queryKey: ['provider', tenantId, 'cases'] }),
      caseId
        ? client.invalidateQueries({ queryKey: ['provider', tenantId, 'case', caseId] })
        : Promise.resolve(),
    ]);
}

export function useOpenCase() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateCase();
  return useMutation({
    mutationFn: (body: CreateHealthCase) => ops.health.createCase(tenantId, body),
    onSuccess: () => invalidate(),
  });
}

export function useCloseCase(caseId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateCase();
  return useMutation({
    mutationFn: (input: { rowVersion: number; reasonText?: string }) =>
      ops.health.closeCase(tenantId, caseId, etagOf(input.rowVersion), {
        ...(input.reasonText ? { reasonText: input.reasonText } : {}),
      }),
    onSuccess: () => invalidate(caseId),
  });
}

export function useCreateEncounter(caseId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateCase();
  return useMutation({
    mutationFn: (body: CreateEncounter) => ops.health.createEncounter(tenantId, caseId, body),
    onSuccess: () => invalidate(caseId),
  });
}

/** The diagnoses of one encounter. Not asked for at all on the financial projection: the answer would be 403. */
export function useDiagnoses(encounterId: string, clinical: boolean) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'diagnoses', encounterId],
    queryFn: () => ops.health.listDiagnoses(tenantId, encounterId),
    enabled: clinical && encounterId !== '',
    retry: false,
  });
}

export function usePutDiagnoses(caseId: string, encounterId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  const invalidate = useInvalidateCase();
  return useMutation({
    mutationFn: (body: PutEncounterDiagnoses) =>
      ops.health.putDiagnoses(tenantId, encounterId, body),
    onSuccess: async () => {
      await client.invalidateQueries({
        queryKey: ['provider', tenantId, 'diagnoses', encounterId],
      });
      await invalidate(caseId);
    },
  });
}

/** ICD-10 by name or code, against the tenant's copy of the code system. */
export function useIcd10Search(q: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const trimmed = q.trim();
  const system = useQuery({
    queryKey: ['provider', tenantId, 'code-system', ICD10],
    queryFn: async () => {
      const page = await ops.catalog.listCodeSystems(tenantId, { limit: 100 });
      return page.items.find((s) => s.code === ICD10) ?? null;
    },
    staleTime: 10 * 60_000,
  });
  const systemId = system.data?.id ?? '';
  const values = useQuery({
    queryKey: ['provider', tenantId, 'icd10', systemId, trimmed],
    queryFn: () => ops.catalog.listCodeValues(tenantId, systemId, { q: trimmed, limit: 20 }),
    enabled: systemId !== '' && trimmed.length >= 2,
  });
  return { systemMissing: system.isSuccess && system.data === null, values };
}

/** Whether a code value belongs to a category WP-I5-01 treats as sensitive. */
export function isSensitiveCode(value: CodeValue): boolean {
  return value.attributes['sensitive'] === true;
}

// --- treatment reports ------------------------------------------------------------------

export function useReportsOfCase(caseId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'reports', 'case', caseId],
    queryFn: () => ops.reports.list(tenantId, { caseId, limit: 50 }),
    enabled: caseId !== '',
  });
}

export function useReport(reportId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'report', reportId],
    queryFn: () => ops.reports.get(tenantId, reportId),
  });
}

/** Every version of the chain a report belongs to, oldest first. */
export function useReportChain(rootReportId: string | undefined) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'reports', 'chain', rootReportId ?? ''],
    queryFn: () => ops.reports.list(tenantId, { rootReportId: rootReportId!, limit: 50 }),
    enabled: Boolean(rootReportId),
    select: (page) => [...page.items].sort((a, b) => a.versionNo - b.versionNo),
  });
}

function useInvalidateReports() {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return (reportId?: string) =>
    Promise.all([
      client.invalidateQueries({ queryKey: ['provider', tenantId, 'reports'] }),
      reportId
        ? client.invalidateQueries({ queryKey: ['provider', tenantId, 'report', reportId] })
        : Promise.resolve(),
    ]);
}

export function useCreateReport() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateReports();
  return useMutation({
    mutationFn: (body: CreateMedicalReport) => ops.reports.create(tenantId, body),
    onSuccess: () => invalidate(),
  });
}

export function useReportCommands(reportId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateReports();
  const done = () => invalidate(reportId);
  return {
    patch: useMutation({
      mutationFn: (input: { rowVersion: number; body: PatchMedicalReportDraft }) =>
        ops.reports.patchDraft(tenantId, reportId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
    putServices: useMutation({
      mutationFn: (input: { rowVersion: number; body: PutMedicalReportServices }) =>
        ops.reports.putServices(tenantId, reportId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
    submit: useMutation({
      mutationFn: (input: { rowVersion: number }) =>
        ops.reports.submit(tenantId, reportId, etagOf(input.rowVersion)),
      onSuccess: done,
    }),
    cancel: useMutation({
      mutationFn: (input: { rowVersion: number }) =>
        ops.reports.cancel(tenantId, reportId, etagOf(input.rowVersion)),
      onSuccess: done,
    }),
  };
}

// --- inpatient stays --------------------------------------------------------------------

export function useStaysOfCase(caseId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'stays', 'case', caseId],
    queryFn: () => ops.stays.list(tenantId, { caseId, limit: 50 }),
    enabled: caseId !== '',
  });
}

export function useStay(stayId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'stay', stayId],
    queryFn: () => ops.stays.get(tenantId, stayId),
  });
}

export function useReconciliation(stayId: string, discharged: boolean) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'stay', stayId, 'reconciliation'],
    queryFn: () => ops.stays.reconciliation(tenantId, stayId),
    enabled: discharged,
  });
}

function useInvalidateStays() {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return (stayId?: string) =>
    Promise.all([
      client.invalidateQueries({ queryKey: ['provider', tenantId, 'stays'] }),
      stayId
        ? client.invalidateQueries({ queryKey: ['provider', tenantId, 'stay', stayId] })
        : Promise.resolve(),
    ]);
}

export function useAdmit() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateStays();
  return useMutation({
    mutationFn: (body: CreateInpatientStay) => ops.stays.create(tenantId, body),
    onSuccess: () => invalidate(),
  });
}

export function useStayCommands(stayId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateStays();
  const done = () => invalidate(stayId);
  return {
    extend: useMutation({
      mutationFn: (input: { rowVersion: number; body: ExtendInpatientStay }) =>
        ops.stays.extend(tenantId, stayId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
    putSegments: useMutation({
      mutationFn: (input: { rowVersion: number; body: PutStaySegments }) =>
        ops.stays.putSegments(tenantId, stayId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
    discharge: useMutation({
      mutationFn: (input: { rowVersion: number; body: DischargeInpatientStay }) =>
        ops.stays.discharge(tenantId, stayId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
    cancel: useMutation({
      mutationFn: (input: { rowVersion: number; reasonCode: string }) =>
        ops.stays.cancel(tenantId, stayId, etagOf(input.rowVersion), {
          reasonCode: input.reasonCode,
        }),
      onSuccess: done,
    }),
  };
}

/** A person's name for a row that carries only the id; "…" while it loads, null when unknown. */
export function usePersonName(personId: string | null | undefined): string | null | undefined {
  const ops = useOps();
  const tenantId = useTenantId();
  const q = useQuery({
    queryKey: ['provider', tenantId, 'person-name', personId ?? ''],
    queryFn: () => ops.people.get(tenantId, personId!),
    enabled: Boolean(personId),
    staleTime: 5 * 60_000,
    retry: false,
  });
  if (q.isPending && q.fetchStatus !== 'idle') return undefined;
  return q.data?.data.displayName ?? null;
}
