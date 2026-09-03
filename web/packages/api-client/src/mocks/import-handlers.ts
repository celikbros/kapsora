/**
 * MSW handlers for the member import pipeline: upload a sponsor file, watch it validate,
 * decide the rows a human has to resolve, then apply. The mock parses the uploaded CSV
 * the same way the Go parser does for the columns the screens show, and it keeps
 * identifiers masked in every response.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  toImportBatch,
  toImportRow,
  type MockWorld,
  type StoredImportBatch,
  type StoredImportRow,
} from './data';
import type { MockApi } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  hasStepUp,
  parseIfMatch,
  parseLimit,
  pathParam,
  problem,
  readJson,
  sha256Hex,
  stepUpRequired,
  wait,
  type Schemas,
} from './handlers';

/** The columns the mock reads; the real parser accepts the same set. */
const COLUMNS = [
  'source_record_id',
  'first_name',
  'middle_name',
  'last_name',
  'birth_date',
  'sex_at_birth',
  'tckn',
  'member_no',
  'employee_no',
  'membership_type',
  'principal_member_no',
  'relationship',
  'valid_from',
  'valid_to',
  'plan_code',
] as const;

interface ParsedRow {
  values: Record<string, string>;
  rowNo: number;
}

/** Splits a CSV_V1 body into rows; the delimiter comes from the header. */
function parseCsv(text: string): { rows: ParsedRow[]; error?: string } {
  const body = text.replace(/^\uFEFF/, '').trim();
  if (body === '') return { rows: [], error: 'EMPTY_FILE' };
  const lines = body.split(/\r?\n/);
  const headerLine = lines[0] ?? '';
  const delimiter = headerLine.includes(';') ? ';' : ',';
  const header = headerLine.split(delimiter).map((h) => h.trim());
  if (header.length !== COLUMNS.length || COLUMNS.some((c) => !header.includes(c))) {
    return { rows: [], error: 'HEADER_INVALID' };
  }
  const rows: ParsedRow[] = [];
  for (let i = 1; i < lines.length; i++) {
    const line = lines[i] ?? '';
    if (line.trim() === '') continue;
    const fields = line.split(delimiter);
    const values: Record<string, string> = {};
    header.forEach((name, index) => {
      values[name] = (fields[index] ?? '').trim();
    });
    rows.push({ values, rowNo: rows.length + 1 });
  }
  return { rows };
}

/** Masks an identifier the way the API does, so the mock never echoes a raw number. */
function mask(type: string, value: string): string {
  const digits = value.replace(/[\s-]/g, '');
  if (type === 'TCKN' && digits.length === 11) {
    return `${digits.slice(0, 3)}******${digits.slice(9)}`;
  }
  if (digits.length > 3) return `${'*'.repeat(digits.length - 3)}${digits.slice(-3)}`;
  return '*'.repeat(digits.length);
}

/** The TCKN checksum, so an invalid number becomes an INVALID row like on the server. */
function validTckn(value: string): boolean {
  const v = value.replace(/[\s-]/g, '');
  if (!/^[1-9]\d{10}$/.test(v)) return false;
  const d = [...v].map(Number);
  const odd = (d[0] ?? 0) + (d[2] ?? 0) + (d[4] ?? 0) + (d[6] ?? 0) + (d[8] ?? 0);
  const even = (d[1] ?? 0) + (d[3] ?? 0) + (d[5] ?? 0) + (d[7] ?? 0);
  if ((((odd * 7 - even) % 10) + 10) % 10 !== d[9]) return false;
  return d.slice(0, 10).reduce((a, b) => a + b, 0) % 10 === d[10];
}

export function importHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  const findBatch = (tenantId: string, id: string): StoredImportBatch | undefined =>
    world().importBatches.find((b) => b.id === id && b.tenantId === tenantId);

  /** Recomputes the counters and the status a batch reports after a change. */
  const refresh = (batch: StoredImportBatch): void => {
    const rows = world().importRows.filter((r) => r.importId === batch.id);
    const decided = (r: StoredImportRow) => r.decision !== null;
    batch.counters = {
      valid: rows.filter((r) => r.status === 'VALID').length,
      invalid: rows.filter((r) => r.status === 'INVALID' && !decided(r)).length,
      matched: rows.filter((r) => r.status === 'MATCHED').length,
      conflict: rows.filter((r) => r.status === 'CONFLICT' && !decided(r)).length,
      created: rows.filter((r) => r.status === 'APPLIED' && r.decision === 'CREATE').length,
      updated: rows.filter((r) => r.status === 'APPLIED' && r.decision === 'UPDATE').length,
      skipped: rows.filter((r) => r.status === 'SKIPPED').length,
    };
    if (batch.status === 'APPLIED' || batch.status === 'CANCELLED') return;
    batch.status = batch.counters.invalid + batch.counters.conflict > 0 ? 'REVIEW' : 'READY';
  };

  return [
    http.post(`${ANY}/api/v1/imports/members`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'import.execute', true);
      if ('error' in g) return g.error;
      if (!hasStepUp(g.session)) return stepUpRequired(api);
      const form = await request.formData();
      const file = form.get('file');
      const sponsorOrganizationId = String(form.get('sponsorOrganizationId') ?? '');
      const sourceSystem = String(form.get('sourceSystem') ?? '');
      const sourceVersion = String(form.get('sourceVersion') ?? '');
      const planId = form.get('planId') ? String(form.get('planId')) : null;
      if (!(file instanceof Blob) || !sponsorOrganizationId || !sourceSystem || !sourceVersion) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'file', code: 'REQUIRED' }],
        });
      }
      const sponsor = world().relationships.find(
        (r) => r.id === sponsorOrganizationId && r.tenantId === g.tenantId,
      );
      if (!sponsor) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'sponsorOrganizationId', code: 'SPONSOR_ORGANIZATION_INVALID' }],
        });
      }
      const fileSha256 = await sha256Hex(file);
      if (
        world().importBatches.some(
          (b) =>
            b.tenantId === g.tenantId &&
            b.sourceSystem === sourceSystem &&
            b.sourceVersion === sourceVersion &&
            b.fileSha256 === fileSha256,
        )
      ) {
        return problem(api, 409, 'IMPORT_DUPLICATE', 'Bu dosya bu kaynak sürümüyle zaten yüklendi');
      }
      const text = await file.text();
      const parsed = parseCsv(text);
      if (parsed.error) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Dosya okunamadı', {
          errors: [{ field: 'file', code: parsed.error }],
        });
      }

      const fileName = file instanceof File ? file.name : 'members.csv';
      const batch: StoredImportBatch = {
        id: world().nextId(),
        tenantId: g.tenantId,
        sponsorOrganizationId,
        sourceSystem,
        sourceVersion,
        fileName,
        fileSha256,
        format: 'CSV_V1',
        planId,
        status: 'VALIDATING',
        rowCount: parsed.rows.length,
        counters: {
          conflict: 0,
          created: 0,
          invalid: 0,
          matched: 0,
          skipped: 0,
          updated: 0,
          valid: 0,
        },
        errorSummary: null,
        createdAt: new Date().toISOString(),
        appliedAt: null,
        rowVersion: 1,
      };
      world().importBatches.push(batch);

      for (const row of parsed.rows) {
        const values = row.values;
        const tckn = values['tckn'] ?? '';
        const memberNo = values['member_no'] ?? '';
        const identifiers = [
          ...(tckn ? [{ type: 'TCKN', value: tckn, primary: true }] : []),
          ...(memberNo ? [{ type: 'MEMBER_NO', value: memberNo, primary: false }] : []),
        ];
        const errors: { code: string; field: string; message?: string }[] = [];
        if (!values['last_name']) errors.push({ field: 'last_name', code: 'REQUIRED' });
        if (tckn && !validTckn(tckn)) {
          errors.push({ field: 'tckn', code: 'IDENTIFIER_INVALID' });
        }
        // A member number already used by two people in the mock world is a conflict.
        const candidates = world()
          .people.filter((p) => p.tenantId === g.tenantId)
          .filter((p) => p.identifiers.some((i) => i.type === 'TCKN' && i.value === tckn))
          .map((p) => p.id);
        let status: StoredImportRow['status'] = 'VALID';
        let decision: StoredImportRow['decision'] = 'CREATE';
        let matchedPersonId: string | null = null;
        if (errors.length > 0) {
          status = 'INVALID';
          decision = null;
        } else if (candidates.length === 1) {
          status = 'MATCHED';
          decision = 'UPDATE';
          matchedPersonId = candidates[0] ?? null;
        } else if (candidates.length > 1) {
          status = 'CONFLICT';
          decision = null;
        }
        world().importRows.push({
          id: world().nextId(),
          tenantId: g.tenantId,
          importId: batch.id,
          rowNo: row.rowNo,
          sourceRecordId: values['source_record_id'] ?? String(row.rowNo),
          displayName: [values['first_name'], values['middle_name'], values['last_name']]
            .filter((part) => part)
            .join(' '),
          birthDate: values['birth_date'] || null,
          identifiers: identifiers.map((i) => ({ ...i, value: mask(i.type, i.value) })),
          membershipType: values['membership_type'] || null,
          planCode: values['plan_code'] || null,
          principalSourceRecordId: values['principal_member_no'] || null,
          status,
          errors,
          candidatePersonIds: status === 'CONFLICT' ? candidates : [],
          matchedPersonId,
          decision,
          appliedPersonId: null,
          rowVersion: 1,
        });
      }
      refresh(batch);
      batch.rowVersion += 1;
      return HttpResponse.json(toImportBatch(batch), {
        status: 201,
        headers: { ETag: etagOf(batch.rowVersion) },
      });
    }),

    http.get(`${ANY}/api/v1/imports/members`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'import.execute', false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'limit', code: 'FORMAT' }],
        });
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const status = url.searchParams.get('status');
      const rows = world()
        .importBatches.filter((b) => b.tenantId === g.tenantId)
        .filter((b) => !status || b.status === status)
        .sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1));
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map(toImportBatch),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      });
    }),

    http.get(`${ANY}/api/v1/imports/members/:importId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'import.execute', false);
      if ('error' in g) return g.error;
      const batch = findBatch(g.tenantId, pathParam(params, 'importId'));
      if (!batch) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(toImportBatch(batch), {
        headers: { ETag: etagOf(batch.rowVersion) },
      });
    }),

    http.get(`${ANY}/api/v1/imports/members/:importId/rows`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'import.execute', false);
      if ('error' in g) return g.error;
      const batch = findBatch(g.tenantId, pathParam(params, 'importId'));
      if (!batch) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'limit', code: 'FORMAT' }],
        });
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const status = url.searchParams.get('status');
      const rows = world()
        .importRows.filter((r) => r.tenantId === g.tenantId && r.importId === batch.id)
        .filter((r) => !status || r.status === status)
        .sort((a, b) => a.rowNo - b.rowNo);
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map(toImportRow),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      });
    }),

    http.post(
      `${ANY}/api/v1/imports/members/:importId/rows/:rowId/review`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'import.execute', true);
        if ('error' in g) return g.error;
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null)
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        const batch = findBatch(g.tenantId, pathParam(params, 'importId'));
        if (!batch) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        const row = world().importRows.find(
          (r) => r.id === pathParam(params, 'rowId') && r.importId === batch.id,
        );
        if (!row) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        if (row.rowVersion !== expected)
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        if (row.status === 'APPLIED' || row.status === 'SKIPPED') {
          return problem(api, 409, 'IMPORT_ROW_NOT_REVIEWABLE', 'Bu satır bu kararı alamaz');
        }
        const body = await readJson<{ decision?: string; matchedPersonId?: string }>(request);
        const decision = body?.decision;
        if (decision !== 'CREATE' && decision !== 'UPDATE' && decision !== 'SKIP') {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'decision', code: 'ENUM' }],
          });
        }
        if (row.status === 'INVALID' && decision !== 'SKIP') {
          return problem(api, 409, 'IMPORT_ROW_NOT_REVIEWABLE', 'Hatalı satır yalnız atlanabilir');
        }
        if (decision === 'UPDATE') {
          const target = body?.matchedPersonId ?? row.matchedPersonId;
          if (
            !target ||
            !row.candidatePersonIds.concat(row.matchedPersonId ?? []).includes(target)
          ) {
            return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
              errors: [{ field: 'matchedPersonId', code: 'REQUIRED' }],
            });
          }
          row.matchedPersonId = target;
        }
        row.decision = decision;
        if (decision === 'SKIP') row.status = 'SKIPPED';
        else if (decision === 'UPDATE') row.status = 'MATCHED';
        else row.status = 'VALID';
        row.rowVersion += 1;
        refresh(batch);
        batch.rowVersion += 1;
        return HttpResponse.json(toImportRow(row), {
          headers: { ETag: etagOf(row.rowVersion) },
        });
      },
    ),

    http.post(`${ANY}/api/v1/imports/members/:importId/apply`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'import.execute', true);
      if ('error' in g) return g.error;
      if (!hasStepUp(g.session)) return stepUpRequired(api);
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const batch = findBatch(g.tenantId, pathParam(params, 'importId'));
      if (!batch) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (batch.status === 'APPLIED') {
        // Re-applying is a no-op, exactly like the worker job.
        return HttpResponse.json(toImportBatch(batch), {
          status: 202,
          headers: { ETag: etagOf(batch.rowVersion) },
        });
      }
      if (batch.status !== 'READY' && batch.status !== 'REVIEW') {
        return problem(api, 409, 'IMPORT_STATE_INVALID', 'Parti bu işleme uygun durumda değil');
      }
      if (batch.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');

      for (const row of world().importRows.filter((r) => r.importId === batch.id)) {
        if (row.status === 'INVALID' || row.decision === null) {
          row.status = 'SKIPPED';
          row.rowVersion += 1;
          continue;
        }
        if (row.status === 'SKIPPED') continue;
        row.appliedPersonId = row.matchedPersonId ?? world().nextId();
        row.status = 'APPLIED';
        row.rowVersion += 1;
      }
      batch.status = 'APPLIED';
      batch.appliedAt = new Date().toISOString();
      refresh(batch);
      batch.rowVersion += 1;
      return HttpResponse.json(toImportBatch(batch), {
        status: 202,
        headers: { ETag: etagOf(batch.rowVersion) },
      });
    }),

    http.post(`${ANY}/api/v1/imports/members/:importId/cancel`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'import.execute', true);
      if ('error' in g) return g.error;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const batch = findBatch(g.tenantId, pathParam(params, 'importId'));
      if (!batch) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (batch.status === 'APPLIED' || batch.status === 'APPLYING') {
        return problem(api, 409, 'IMPORT_STATE_INVALID', 'Uygulanan parti iptal edilemez');
      }
      if (batch.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      const body = await readJson<Schemas['ReasonCommand']>(request);
      if (!body?.reasonCode) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'reasonCode', code: 'REQUIRED' }],
        });
      }
      batch.status = 'CANCELLED';
      batch.errorSummary = body.reasonText ?? body.reasonCode;
      batch.rowVersion += 1;
      return HttpResponse.json(toImportBatch(batch), {
        headers: { ETag: etagOf(batch.rowVersion) },
      });
    }),
  ];
}
