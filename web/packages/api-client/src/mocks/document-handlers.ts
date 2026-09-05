/**
 * MSW handlers for the M4 document pipeline: reserving an upload, completing it, reading
 * a document, minting a download URL, linking a document to a record, and legal holds.
 *
 * The rule the whole module exists to enforce is that a document is downloadable only
 * once its scan came back CLEAN. PENDING, SCANNING and FAILED are deliberately
 * indistinguishable in the answer — telling them apart would only say how far along an
 * attack got — and an infected file has its own refusal because its bytes are already
 * gone. `downloadable` on the resource is the same question answered in one place, so a
 * screen never offers a button the server refuses.
 *
 * No endpoint here writes a scan verdict. Only the worker does, which in the mock is
 * `world.advanceScan`, so a fixture can move a document from "taranıyor" to a verdict
 * without any request appearing to have caused it.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  documentDownloadable,
  toDocument,
  toDocumentLink,
  toLegalHold,
  type MockWorld,
  type StoredDocument,
  type StoredLegalHold,
} from './data';
import type { MockApi, MockSession } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  hasPermission,
  organizationScope,
  parseLimit,
  pathParam,
  problem,
  readJson,
  requireIdempotencyKey,
  requireIfMatch,
  validationFailed,
  wait,
  withinScope,
  type FieldError,
  type Schemas,
} from './handlers';

const AGGREGATE_TYPE = /^[A-Z][A-Z0-9_]{1,63}$/;
const DOCUMENT_TYPE_CODE = /^[A-Z][A-Z0-9_]{1,63}$/;
const SHA256 = /^[0-9a-f]{64}$/;
const MIN_BYTE_SIZE = 1;
const MAX_BYTE_SIZE = 100 << 20;
const MAX_FILENAME = 255;
const MAX_CONTENT_TYPE = 255;
const MAX_PURPOSE = 200;
const MAX_REASON = 1000;
const UPLOAD_TTL_MINUTES = 15;
const DOWNLOAD_TTL_MINUTES = 5;

const SCAN_STATUSES = new Set<string>(['PENDING', 'SCANNING', 'CLEAN', 'INFECTED', 'FAILED']);
const CLASSIFICATIONS = new Set<string>(['INTERNAL', 'CONFIDENTIAL', 'PERSONAL', 'HEALTH']);

/** Every response of this module carries it: a body here may quote a bearer URL. */
const NO_STORE = { 'Cache-Control': 'no-store' } as const;

function documentNotFound(api: MockApi): Response {
  return problem(api, 404, 'DOCUMENT_NOT_FOUND', 'Belge bulunamadı');
}

function expiresIn(minutes: number): string {
  return new Date(Date.now() + minutes * 60_000).toISOString();
}

/**
 * The one place the refusal is decided. INFECTED wins over purged, and everything that is
 * simply not clean yet — reserved, scanning, or a scan that reached no verdict — is one
 * answer, because "how far along is it" is not something a refusal should tell anybody.
 */
function downloadRefusal(api: MockApi, doc: StoredDocument): Response | null {
  if (documentDownloadable(doc)) return null;
  if (doc.scanStatus === 'INFECTED') {
    return problem(api, 409, 'DOCUMENT_INFECTED', 'Belgede zararlı yazılım bulundu', {
      detail: 'Dosya silindi; yeniden yüklemeniz gerekir.',
    });
  }
  if (doc.purgedAt) {
    return problem(api, 409, 'DOCUMENT_PURGED', 'Belge saklama süresi dolduğu için silindi');
  }
  return problem(api, 409, 'DOCUMENT_NOT_SCANNED', 'Belge henüz taranmadı', {
    detail: 'Tarama tamamlanana kadar belge indirilemez.',
  });
}

/** Any directory part a browser sent is stripped, and it is never used to build a key. */
function normalizeFilename(name: string): string {
  const parts = name.split(/[\\/]/);
  return (parts[parts.length - 1] ?? '').trim();
}

export function documentHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  /**
   * The row, or nothing. A provider-scoped actor sees its own organizations' documents
   * and the tenant's own; one belonging to another provider is genuinely absent, which is
   * why the answer is 404 rather than 403.
   */
  const find = (session: MockSession, tenantId: string, id: string): StoredDocument | undefined => {
    const scope = organizationScope(api, session, tenantId);
    const row = world().documents.find((d) => d.id === id && d.tenantId === tenantId);
    if (!row) return undefined;
    if (scope === null) return row;
    // The tenant's own documents stay visible to a provider; another provider's do not.
    if (row.ownerOrganizationId === null) return row;
    return withinScope(scope, row.ownerOrganizationId ?? null) ? row : undefined;
  };

  const visible = (session: MockSession, tenantId: string, doc: StoredDocument): boolean => {
    const scope = organizationScope(api, session, tenantId);
    return scope === null || doc.ownerOrganizationId === null
      ? true
      : withinScope(scope, doc.ownerOrganizationId ?? null);
  };

  return [
    // The presigned PUT. In production this is MinIO, not the API: the browser sends
    // the bytes straight to the quarantine bucket and the API never sees them. The
    // mock accepts anything the signed URL names so a screen can upload end to end;
    // the scan that follows is driven by `world.advanceScan`, never by this route.
    http.put('https://quarantine.kapsora.local/*', async () => {
      await wait(api);
      return new HttpResponse(null, { status: 200, headers: { ETag: '"mock-upload"' } });
    }),
    http.get(`${ANY}/api/v1/documents`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'document.read', false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') return validationFailed(api, [{ field: 'limit', code: 'FORMAT' }]);
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const scanStatus = url.searchParams.get('scanStatus');
      const classification = url.searchParams.get('classification');
      const errors: FieldError[] = [];
      if (scanStatus && !SCAN_STATUSES.has(scanStatus)) {
        errors.push({ field: 'scanStatus', code: 'ENUM' });
      }
      if (classification && !CLASSIFICATIONS.has(classification)) {
        errors.push({ field: 'classification', code: 'ENUM' });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      const aggregateType = url.searchParams.get('aggregateType');
      const aggregateId = url.searchParams.get('aggregateId');
      const rows = world()
        .documents.filter((d) => {
          if (d.tenantId !== g.tenantId || !visible(g.session, g.tenantId, d)) return false;
          if (scanStatus && d.scanStatus !== scanStatus) return false;
          if (classification && d.classification !== classification) return false;
          if (aggregateType || aggregateId) {
            const links = world().documentLinks.filter((l) => l.documentId === d.id);
            const hit = links.some(
              (l) =>
                (!aggregateType || l.aggregateType === aggregateType) &&
                (!aggregateId || l.aggregateId === aggregateId),
            );
            if (!hit) return false;
          }
          return true;
        })
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page: Schemas['DocumentPage'] = {
        // The page carries the scan status, so a client can show "taranıyor" rather than
        // a broken download.
        items: rows.slice(offset, offset + limit).map((d) => toDocument(world(), d)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(page, { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/documents`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'document.upload', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const body = await readJson<Schemas['CreateUpload']>(request);
      const errors: FieldError[] = [];
      const filename = normalizeFilename(body?.originalFilename ?? '');
      if (filename.length < 1 || filename.length > MAX_FILENAME) {
        errors.push({ field: 'originalFilename', code: 'LENGTH' });
      }
      if (
        typeof body?.contentType !== 'string' ||
        body.contentType.length < 3 ||
        body.contentType.length > MAX_CONTENT_TYPE
      ) {
        errors.push({ field: 'contentType', code: 'LENGTH' });
      }
      // The declared size is what gets signed into the URL, so it is the actual limit
      // rather than a hint: the store refuses a body of any other size.
      if (
        !Number.isInteger(body?.byteSize) ||
        body!.byteSize < MIN_BYTE_SIZE ||
        body!.byteSize > MAX_BYTE_SIZE
      ) {
        errors.push({ field: 'byteSize', code: 'RANGE' });
      }
      if (body?.classification !== undefined && !CLASSIFICATIONS.has(body.classification)) {
        errors.push({ field: 'classification', code: 'ENUM' });
      }
      if (body?.sha256 != null && !SHA256.test(body.sha256)) {
        errors.push({ field: 'sha256', code: 'FORMAT' });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      // Ownership. An unrestricted caller may name any organization or none; a restricted
      // one holding exactly one does not have to say which, and one holding several has
      // to, because guessing on its behalf is how a document ends up in the wrong place.
      const scope = organizationScope(api, g.session, g.tenantId);
      let ownerOrganizationId = body!.ownerOrganizationId ?? null;
      if (scope !== null) {
        if (ownerOrganizationId === null) {
          if (scope.length === 1) {
            ownerOrganizationId = scope[0]!;
          } else {
            return validationFailed(api, [{ field: 'ownerOrganizationId', code: 'REQUIRED' }]);
          }
        } else if (!scope.includes(ownerOrganizationId)) {
          return validationFailed(api, [{ field: 'ownerOrganizationId', code: 'SCOPE' }]);
        }
      }

      // When those exact bytes are already stored clean and visible to the caller, no
      // upload happens at all: that is what stops a member re-uploading the same document
      // being counted as a new one.
      if (body!.sha256) {
        const existing = world().documents.find(
          (d) =>
            d.tenantId === g.tenantId &&
            d.sha256 === body!.sha256 &&
            d.scanStatus === 'CLEAN' &&
            d.duplicateOfDocumentId === null &&
            visible(g.session, g.tenantId, d),
        );
        if (existing) {
          const answer: Schemas['DocumentUpload'] = {
            document: toDocument(world(), existing),
            upload: null,
          };
          return HttpResponse.json(answer, { status: 201, headers: NO_STORE });
        }
      }

      const now = new Date();
      const id = world().nextId();
      const doc: StoredDocument = {
        tenantId: g.tenantId,
        id,
        // Reserved bytes always land in quarantine; nothing reaches the secure bucket
        // without a clean verdict.
        bucket: 'quarantine',
        classification: body!.classification ?? 'INTERNAL',
        originalFilename: filename,
        contentType: body!.contentType,
        // Null until the upload is completed: what the client claimed is not a fact yet.
        byteSize: null,
        sha256: null,
        scanStatus: 'PENDING',
        ownerOrganizationId,
        duplicateOfDocumentId: null,
        uploadedBy: g.session.account.actorId,
        uploadedAt: now.toISOString(),
        purgedAt: null,
        // Nothing client-supplied is in the key, so a filename can never steer storage.
        objectKey: `${g.tenantId}/${now.getUTCFullYear()}/${String(now.getUTCMonth() + 1).padStart(2, '0')}/${id}`,
        createdAt: now.toISOString(),
        rowVersion: 1,
      };
      world().documents.push(doc);
      const answer: Schemas['DocumentUpload'] = {
        document: toDocument(world(), doc),
        upload: {
          url: `https://quarantine.kapsora.local/${doc.objectKey}?signature=mock`,
          method: 'PUT',
          expiresAt: expiresIn(UPLOAD_TTL_MINUTES),
          headers: {
            'Content-Type': doc.contentType,
            'Content-Length': String(body!.byteSize),
          },
        },
      };
      return HttpResponse.json(answer, { status: 201, headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/documents/:documentId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'document.read', false);
      if ('error' in g) return g.error;
      const doc = find(g.session, g.tenantId, pathParam(params, 'documentId'));
      if (!doc) return documentNotFound(api);
      return HttpResponse.json(toDocument(world(), doc), { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/documents/:documentId/complete`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'document.upload', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const body = await readJson<Schemas['CompleteUpload']>(request);
      const errors: FieldError[] = [];
      if (typeof body?.sha256 !== 'string' || !SHA256.test(body.sha256)) {
        errors.push({ field: 'sha256', code: 'FORMAT' });
      }
      if (
        !Number.isInteger(body?.byteSize) ||
        body!.byteSize < MIN_BYTE_SIZE ||
        body!.byteSize > MAX_BYTE_SIZE
      ) {
        errors.push({ field: 'byteSize', code: 'RANGE' });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      const doc = find(g.session, g.tenantId, pathParam(params, 'documentId'));
      if (!doc) return documentNotFound(api);
      if (doc.scanStatus !== 'PENDING') {
        return problem(api, 409, 'DOCUMENT_ALREADY_COMPLETED', 'Bu yükleme zaten tamamlandı');
      }
      // Neither the size nor the digest is trusted: they are recorded because a claim that
      // later disagrees with the file is itself worth knowing about, and the worker's own
      // count replaces them when it has read every byte.
      doc.byteSize = body!.byteSize;
      doc.sha256 = body!.sha256;
      doc.scanStatus = 'SCANNING';
      doc.rowVersion += 1;
      return HttpResponse.json(toDocument(world(), doc), { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/documents/:documentId/download`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'document.read', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['DownloadDocument']>(request);
      if (body?.reasonText !== undefined && [...body.reasonText].length > MAX_REASON) {
        return validationFailed(api, [{ field: 'reasonText', code: 'LENGTH' }]);
      }
      const doc = find(g.session, g.tenantId, pathParam(params, 'documentId'));
      if (!doc) return documentNotFound(api);
      // Every link on the document is checked, not one of them: a clinical attachment
      // stays clinical wherever it is reached from. This is asked before the scan gate,
      // so a caller who may not see the file at all is not told about its scan.
      const links = world().documentLinks.filter((l) => l.documentId === doc.id);
      const missing = links.find(
        (l) =>
          l.requiredPermission != null &&
          !hasPermission(api, g.session, g.tenantId, l.requiredPermission),
      );
      if (missing) {
        return problem(
          api,
          403,
          'DOCUMENT_LINK_PERMISSION_DENIED',
          'Bu belgeyi indirmek için ek yetki gerekiyor',
          { detail: missing.requiredPermission ?? '' },
        );
      }
      const refusal = downloadRefusal(api, doc);
      if (refusal) return refusal;
      // No Idempotency-Key: every call mints a new short-lived URL and records a new
      // access, which is exactly what a retry should do. Replaying the first response
      // would hand the caller a URL that has since expired.
      const answer: Schemas['DocumentDownload'] = {
        url: `https://secure.kapsora.local/${doc.objectKey}?signature=mock`,
        method: 'GET',
        expiresAt: expiresIn(DOWNLOAD_TTL_MINUTES),
        classification: doc.classification,
      };
      return HttpResponse.json(answer, { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/documents/:documentId/links`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'document.link', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const body = await readJson<Schemas['CreateDocumentLink']>(request);
      const errors: FieldError[] = [];
      if (typeof body?.aggregateType !== 'string' || !AGGREGATE_TYPE.test(body.aggregateType)) {
        errors.push({ field: 'aggregateType', code: 'FORMAT' });
      }
      if (!body?.aggregateId) errors.push({ field: 'aggregateId', code: 'REQUIRED' });
      if (
        typeof body?.documentTypeCode !== 'string' ||
        !DOCUMENT_TYPE_CODE.test(body.documentTypeCode)
      ) {
        errors.push({ field: 'documentTypeCode', code: 'FORMAT' });
      }
      if (body?.purpose !== undefined && [...body.purpose].length > MAX_PURPOSE) {
        errors.push({ field: 'purpose', code: 'LENGTH' });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      const doc = find(g.session, g.tenantId, pathParam(params, 'documentId'));
      if (!doc) return documentNotFound(api);
      if (
        world().documentLinks.some(
          (l) =>
            l.documentId === doc.id &&
            l.aggregateType === body!.aggregateType &&
            l.aggregateId === body!.aggregateId &&
            l.documentTypeCode === body!.documentTypeCode,
        )
      ) {
        return problem(api, 409, 'DOCUMENT_LINK_EXISTS', 'Belge bu kayda zaten bağlı');
      }
      const link = {
        tenantId: g.tenantId,
        id: world().nextId(),
        documentId: doc.id,
        aggregateType: body!.aggregateType,
        aggregateId: body!.aggregateId,
        documentTypeCode: body!.documentTypeCode,
        purpose: body!.purpose ?? null,
        // Narrows who may download through this link, on top of document.read.
        requiredPermission: body!.requiredPermission ?? null,
        createdBy: g.session.account.actorId,
        createdAt: new Date().toISOString(),
      };
      world().documentLinks.push(link);
      return HttpResponse.json(toDocumentLink(link), { status: 201, headers: NO_STORE });
    }),

    http.delete(
      `${ANY}/api/v1/documents/:documentId/links/:linkId`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'document.link', true);
        if ('error' in g) return g.error;
        const doc = find(g.session, g.tenantId, pathParam(params, 'documentId'));
        if (!doc) return documentNotFound(api);
        const linkId = pathParam(params, 'linkId');
        const index = world().documentLinks.findIndex(
          (l) => l.id === linkId && l.documentId === doc.id,
        );
        if (index < 0) {
          return problem(api, 404, 'DOCUMENT_LINK_NOT_FOUND', 'Belge bağlantısı bulunamadı');
        }
        // The document itself is untouched: a file that is no longer this request's
        // invoice is still a file somebody uploaded, and other records may still need it.
        world().documentLinks.splice(index, 1);
        return new HttpResponse(null, { status: 204, headers: NO_STORE });
      },
    ),

    http.post(`${ANY}/api/v1/legal-holds`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'document.legal_hold.manage', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const body = await readJson<Schemas['CreateLegalHold']>(request);
      const errors: FieldError[] = [];
      const reason = body?.reason?.trim() ?? '';
      if (reason.length < 1 || reason.length > MAX_REASON) {
        errors.push({ field: 'reason', code: 'LENGTH' });
      }
      const hasAggregate = Boolean(body?.aggregateType && body?.aggregateId);
      if (!body?.documentId && !body?.personId && !hasAggregate) {
        // A hold that named nothing would look like protection and protect nothing.
        errors.push({
          field: 'documentId',
          code: 'REQUIRED',
          message: 'belge, kişi veya kayıt hedeflerinden biri zorunlu',
        });
      }
      if (body?.aggregateType != null && !AGGREGATE_TYPE.test(body.aggregateType)) {
        errors.push({ field: 'aggregateType', code: 'FORMAT' });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      if (body!.documentId) {
        // Placing a hold on a document outside the caller's scope answers 404, so a hold
        // cannot be used to probe whether somebody else's document exists.
        const doc = find(g.session, g.tenantId, body!.documentId);
        if (!doc) return documentNotFound(api);
      }
      const clash = world().legalHolds.find(
        (h) =>
          h.tenantId === g.tenantId &&
          h.releasedAt === null &&
          ((body!.documentId != null && h.documentId === body!.documentId) ||
            (body!.personId != null && h.personId === body!.personId) ||
            (hasAggregate &&
              h.aggregateType === body!.aggregateType &&
              h.aggregateId === body!.aggregateId)),
      );
      if (clash) {
        return problem(
          api,
          409,
          'LEGAL_HOLD_EXISTS',
          'Bu hedef için etkin bir hukuki saklama zaten var',
        );
      }
      const hold: StoredLegalHold = {
        tenantId: g.tenantId,
        id: world().nextId(),
        documentId: body!.documentId ?? null,
        personId: body!.personId ?? null,
        aggregateType: body!.aggregateType ?? null,
        aggregateId: body!.aggregateId ?? null,
        reason,
        placedBy: g.session.account.actorId,
        placedAt: new Date().toISOString(),
        releasedAt: null,
        releasedBy: null,
        rowVersion: 1,
      };
      world().legalHolds.push(hold);
      return HttpResponse.json(toLegalHold(hold), {
        status: 201,
        headers: { ...NO_STORE, ETag: etagOf(hold.rowVersion) },
      });
    }),

    http.post(`${ANY}/api/v1/legal-holds/:legalHoldId/release`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'document.legal_hold.manage', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const expected = requireIfMatch(api, request);
      if (typeof expected !== 'number') return expected;
      const hold = world().legalHolds.find(
        (h) => h.id === pathParam(params, 'legalHoldId') && h.tenantId === g.tenantId,
      );
      if (!hold) {
        return problem(api, 404, 'LEGAL_HOLD_NOT_FOUND', 'Hukuki saklama kaydı bulunamadı');
      }
      if (hold.releasedAt !== null) {
        return problem(api, 409, 'LEGAL_HOLD_ALREADY_RELEASED', 'Hukuki saklama zaten kaldırılmış');
      }
      if (hold.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      // Who lifted it and when are both recorded: a hold that could be released
      // anonymously would be no protection at all.
      hold.releasedAt = new Date().toISOString();
      hold.releasedBy = g.session.account.actorId;
      hold.rowVersion += 1;
      return HttpResponse.json(toLegalHold(hold), {
        headers: { ...NO_STORE, ETag: etagOf(hold.rowVersion) },
      });
    }),
  ];
}
