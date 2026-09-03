/**
 * Synthetic Turkish data for the mock API. Deterministic (seeded) so screenshots,
 * Playwright runs and reviews see the same rows. No real names, tax numbers are random
 * with a valid checksum, nothing here refers to a real person or company.
 */
import type { components } from '../generated/kapsora-v1';
import { maskIdentifier, randomTCKN, randomVKN } from '../identifiers';

type Schemas = components['schemas'];
export type MockTenant = Schemas['TenantSummary'];

/** Linear congruential generator: small, deterministic, good enough for fixtures. */
export function seededRandom(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 2 ** 32;
  };
}

/** UUIDv7-like ids: time-ordered prefix, random tail, from the seeded generator. */
export function makeIdFactory(
  random: () => number,
  startMillis: number,
): (offsetMs?: number) => string {
  let counter = 0;
  return (offsetMs = 0) => {
    counter += 1;
    const ms = startMillis + offsetMs + counter;
    const hex = ms.toString(16).padStart(12, '0');
    const tail = Array.from({ length: 18 }, () => Math.floor(random() * 16).toString(16)).join('');
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-7${tail.slice(0, 3)}-${(8 + Math.floor(random() * 4)).toString(16)}${tail.slice(3, 6)}-${tail.slice(6, 18)}`;
  };
}

export interface MockAccount {
  actorId: string;
  username: string;
  displayName: string;
  email: string;
  /** Tenant codes the account is a member of, with permissions per tenant. */
  memberships: { tenantCode: string; permissions: string[] }[];
}

export interface StoredOrganization {
  /** Global legal entity id (directory.organization). */
  organizationId: string;
  legalName: string;
  displayName: string;
  organizationKind: Schemas['Organization']['organizationKind'];
  countryCode: string;
  organizationStatus: Schemas['Organization']['organizationStatus'];
  /** Plain tax number, kept only inside the mock to emulate dedup; never returned. */
  taxNumber?: { type: 'VKN' | 'TCKN'; value: string };
  otherIdentifiers: {
    type: 'MERSIS' | 'PROVIDER_REGISTRY' | 'OTHER';
    value: string;
    primary: boolean;
  }[];
}

export interface StoredRelationship {
  id: string;
  tenantId: string;
  organizationId: string;
  relationshipRole: Schemas['Organization']['relationshipRole'];
  relationshipStatus: Schemas['Organization']['relationshipStatus'];
  tenantCode: string | null;
  validFrom: string;
  validTo: string | null;
  createdAt: string;
  rowVersion: number;
}

export interface StoredPerson {
  id: string;
  tenantId: string;
  firstName: string;
  middleName: string | null;
  lastName: string;
  birthDate: string | null;
  sexAtBirth: 'FEMALE' | 'MALE' | 'INTERSEX' | 'UNKNOWN' | null;
  status: Schemas['PersonSummary']['status'];
  identifiers: { type: string; value: string; primary: boolean }[];
  rowVersion: number;
  createdAt: string;
}

export type StoredServiceRequest = Schemas['ServiceRequest'] & { tenantId: string };

const ADMIN_PERMISSIONS = [
  'organization.read',
  'organization.manage',
  'person.read',
  'person.manage',
  'eligibility.check',
  'service_request.read',
  'service_request.manage',
  'user.read',
  'user.manage',
  'role.manage',
  'audit.read',
  'report.read',
  'integration.read',
];
const REVIEWER_PERMISSIONS = [
  'organization.read',
  'person.read',
  'service_request.read',
  'service_request.review',
];

const ORG_PREFIXES = [
  'Anadolu',
  'Marmara',
  'Ege',
  'Karadeniz',
  'Akdeniz',
  'Boğaziçi',
  'Toros',
  'Kapadokya',
  'Truva',
  'Efes',
  'Palandöken',
  'Uludağ',
  'Erciyes',
  'Sakarya',
  'Meriç',
  'Kızılırmak',
  'Fırat',
  'Dicle',
  'Göksu',
  'Çoruh',
];
const ORG_SUFFIXES: Record<Schemas['Organization']['organizationKind'], string[]> = {
  BANK: ['Bankası A.Ş.', 'Katılım Bankası A.Ş.'],
  INSURER: ['Sigorta A.Ş.', 'Hayat ve Emeklilik A.Ş.'],
  SPONSOR: ['Holding A.Ş.', 'Vakfı'],
  PROVIDER: [
    'Hastanesi A.Ş.',
    'Tıp Merkezi Ltd. Şti.',
    'Termal Otel A.Ş.',
    'Diş Kliniği Ltd. Şti.',
    'Fizik Tedavi Merkezi A.Ş.',
  ],
  VENDOR: ['Bilişim A.Ş.', 'Lojistik Ltd. Şti.'],
  PUBLIC_BODY: ['Belediyesi', 'İl Sağlık Müdürlüğü'],
  OTHER: ['Derneği', 'Kooperatifi'],
};
const KIND_TO_ROLE: Record<
  Schemas['Organization']['organizationKind'],
  Schemas['Organization']['relationshipRole']
> = {
  BANK: 'PAYER',
  INSURER: 'PAYER',
  SPONSOR: 'SPONSOR',
  PROVIDER: 'PROVIDER',
  VENDOR: 'VENDOR',
  PUBLIC_BODY: 'PARTNER',
  OTHER: 'PARTNER',
};
const KIND_WEIGHTS: Schemas['Organization']['organizationKind'][] = [
  'PROVIDER',
  'PROVIDER',
  'PROVIDER',
  'PROVIDER',
  'PROVIDER',
  'PROVIDER',
  'VENDOR',
  'INSURER',
  'BANK',
  'SPONSOR',
  'PUBLIC_BODY',
  'OTHER',
];

const FIRST_NAMES = [
  'Ayşe',
  'Mehmet',
  'Elif',
  'Can',
  'Zeynep',
  'Emre',
  'Defne',
  'Kerem',
  'Nehir',
  'Arda',
  'Ada',
  'Yusuf',
];
const LAST_NAMES = [
  'Yılmaz',
  'Demir',
  'Şahin',
  'Çelik',
  'Kaya',
  'Aydın',
  'Öztürk',
  'Arslan',
  'Doğan',
  'Kılıç',
  'Aslan',
  'Çetin',
];

function pick<T>(random: () => number, list: readonly T[]): T {
  return list[Math.floor(random() * list.length)]!;
}

function isoDaysAgo(base: number, days: number): string {
  return new Date(base - days * 86_400_000).toISOString();
}

/** The whole in-memory world of the mock API. */
export interface MockWorld {
  tenants: MockTenant[];
  accounts: MockAccount[];
  organizations: Map<string, StoredOrganization>;
  relationships: StoredRelationship[];
  people: StoredPerson[];
  serviceRequests: StoredServiceRequest[];
  nextId: (offsetMs?: number) => string;
  random: () => number;
}

/** Builds the seeded world; `organizationsPerTenant` defaults to 120 to exercise paging. */
export function buildWorld(
  options: { seed?: number; organizationsPerTenant?: number } = {},
): MockWorld {
  const random = seededRandom(options.seed ?? 20260903);
  const base = Date.UTC(2026, 8, 3, 9, 0, 0);
  const nextId = makeIdFactory(random, base - 400 * 86_400_000);
  const perTenant = options.organizationsPerTenant ?? 120;

  const tenants: MockTenant[] = [
    {
      id: nextId(),
      code: 'DEMO_A',
      displayName: 'Demo Banka A.Ş.',
      status: 'ACTIVE',
      defaultLocale: 'tr-TR',
      defaultTimeZone: 'Europe/Istanbul',
    },
    {
      id: nextId(),
      code: 'DEMO_B',
      displayName: 'Demo Sigorta A.Ş.',
      status: 'ACTIVE',
      defaultLocale: 'tr-TR',
      defaultTimeZone: 'Europe/Istanbul',
    },
  ];
  const accounts: MockAccount[] = [
    {
      actorId: nextId(),
      username: 'admin.a',
      displayName: 'Ayşe Yönetici',
      email: 'admin.a@example.invalid',
      memberships: [{ tenantCode: 'DEMO_A', permissions: ADMIN_PERMISSIONS }],
    },
    {
      actorId: nextId(),
      username: 'reviewer.a',
      displayName: 'Refik İnceleyici',
      email: 'reviewer.a@example.invalid',
      memberships: [{ tenantCode: 'DEMO_A', permissions: REVIEWER_PERMISSIONS }],
    },
    {
      actorId: nextId(),
      username: 'admin.b',
      displayName: 'Bora Yönetici',
      email: 'admin.b@example.invalid',
      memberships: [{ tenantCode: 'DEMO_B', permissions: ADMIN_PERMISSIONS }],
    },
    {
      actorId: nextId(),
      username: 'both.ab',
      displayName: 'Belgin Çift',
      email: 'both.ab@example.invalid',
      memberships: [
        { tenantCode: 'DEMO_A', permissions: ADMIN_PERMISSIONS },
        { tenantCode: 'DEMO_B', permissions: REVIEWER_PERMISSIONS },
      ],
    },
  ];

  const organizations = new Map<string, StoredOrganization>();
  const relationships: StoredRelationship[] = [];
  const usedNames = new Set<string>();

  for (const tenant of tenants) {
    for (let i = 0; i < perTenant; i++) {
      const kind = pick(random, KIND_WEIGHTS);
      let name = `${pick(random, ORG_PREFIXES)} ${pick(random, ORG_SUFFIXES[kind])}`;
      let attempt = 0;
      while (usedNames.has(name)) {
        attempt += 1;
        name = `${pick(random, ORG_PREFIXES)} ${attempt + 1}. ${pick(random, ORG_SUFFIXES[kind])}`;
      }
      usedNames.add(name);
      const soleTrader = kind === 'PROVIDER' && random() < 0.1;
      const organizationId = nextId();
      const org: StoredOrganization = {
        organizationId,
        legalName: name,
        displayName: name.replace(/ (A\.Ş\.|Ltd\. Şti\.)$/, ''),
        organizationKind: kind,
        countryCode: 'TR',
        organizationStatus: 'ACTIVE',
        taxNumber: soleTrader
          ? { type: 'TCKN', value: randomTCKN(random) }
          : { type: 'VKN', value: randomVKN(random) },
        otherIdentifiers:
          random() < 0.3
            ? [
                {
                  type: 'MERSIS',
                  value: Array.from({ length: 16 }, () => Math.floor(random() * 10)).join(''),
                  primary: false,
                },
              ]
            : [],
      };
      organizations.set(organizationId, org);
      const daysAgo = perTenant - i + Math.floor(random() * 3);
      const statusRoll = random();
      relationships.push({
        id: nextId(daysAgo * -86_400_000 + i),
        tenantId: tenant.id,
        organizationId,
        relationshipRole: KIND_TO_ROLE[kind],
        relationshipStatus:
          statusRoll < 0.85 ? 'ACTIVE' : statusRoll < 0.95 ? 'SUSPENDED' : 'PENDING',
        tenantCode:
          random() < 0.5 ? `${tenant.code.slice(-1)}-${String(i + 1).padStart(4, '0')}` : null,
        validFrom: isoDaysAgo(base, daysAgo).slice(0, 10),
        validTo: null,
        createdAt: isoDaysAgo(base, daysAgo),
        rowVersion: 1 + Math.floor(random() * 3),
      });
    }
  }
  // One organization shared by both tenants: exercises the shared-name protection.
  const shared = relationships.find(
    (r) => r.tenantId === tenants[0]!.id && r.relationshipRole === 'PROVIDER',
  );
  if (shared) {
    relationships.push({
      ...shared,
      id: nextId(),
      tenantId: tenants[1]!.id,
      tenantCode: null,
      rowVersion: 1,
    });
  }

  const people: StoredPerson[] = [];
  for (const tenant of tenants) {
    for (let i = 0; i < 40; i++) {
      const daysAgo = 200 - i * 3;
      people.push({
        id: nextId(daysAgo * -86_400_000),
        tenantId: tenant.id,
        firstName: pick(random, FIRST_NAMES),
        middleName: random() < 0.2 ? pick(random, FIRST_NAMES) : null,
        lastName: pick(random, LAST_NAMES),
        birthDate: `${1950 + Math.floor(random() * 55)}-${String(1 + Math.floor(random() * 12)).padStart(2, '0')}-${String(1 + Math.floor(random() * 28)).padStart(2, '0')}`,
        sexAtBirth: random() < 0.5 ? 'FEMALE' : 'MALE',
        status: random() < 0.95 ? 'ACTIVE' : 'INACTIVE',
        identifiers: [{ type: 'TCKN', value: randomTCKN(random), primary: true }],
        rowVersion: 1,
        createdAt: isoDaysAgo(base, daysAgo),
      });
    }
  }

  const serviceRequests: StoredServiceRequest[] = [];
  const statuses: Schemas['ServiceRequest']['status'][] = [
    'DRAFT',
    'SUBMITTED',
    'PENDING_REVIEW',
    'APPROVED',
    'REJECTED',
    'CLOSED',
  ];
  for (const tenant of tenants) {
    const tenantPeople = people.filter((p) => p.tenantId === tenant.id);
    const providers = relationships.filter(
      (r) => r.tenantId === tenant.id && r.relationshipRole === 'PROVIDER',
    );
    for (let i = 0; i < 25; i++) {
      const daysAgo = 60 - i * 2;
      const status = pick(random, statuses);
      const id = nextId(daysAgo * -86_400_000);
      serviceRequests.push({
        tenantId: tenant.id,
        id,
        reference: `SR-2026-${String(1000 + i)}`,
        personId: pick(random, tenantPeople).id,
        programId: nextId(),
        enrollmentId: nextId(),
        providerOrganizationId: pick(random, providers)?.id ?? null,
        requestType: pick(random, [
          'DIRECT_SERVICE',
          'PREAUTHORIZATION',
          'RESERVATION',
          'REIMBURSEMENT',
        ] as const),
        channel: pick(random, ['BACKOFFICE', 'PROVIDER_PORTAL', 'MEMBER_PORTAL'] as const),
        status,
        serviceDate: isoDaysAgo(base, daysAgo - 5).slice(0, 10),
        requestedStartAt: null,
        requestedEndAt: null,
        submittedAt: status === 'DRAFT' ? null : isoDaysAgo(base, daysAgo),
        createdAt: isoDaysAgo(base, daysAgo),
        rowVersion: 1,
        items: [
          {
            id: nextId(),
            lineNo: 1,
            serviceDefinitionId: nextId(),
            unitType: 'SESSION',
            requestedQuantity: 1 + Math.floor(random() * 5),
            requestedAmount: Math.round(random() * 5000) / 1,
            currencyCode: 'TRY',
            status: 'REQUESTED',
            approvedQuantity: null,
            approvedAmount: null,
            decisionReasonCode: null,
          },
        ],
      });
    }
  }

  return {
    tenants,
    accounts,
    organizations,
    relationships,
    people,
    serviceRequests,
    nextId,
    random,
  };
}

/** Projects a relationship + organization into the contract's Organization shape. */
export function toOrganization(
  rel: StoredRelationship,
  org: StoredOrganization,
): Schemas['Organization'] {
  const identifiers: Schemas['OrganizationIdentifier'][] = [];
  if (org.taxNumber) {
    identifiers.push({
      type: org.taxNumber.type,
      maskedValue: maskIdentifier(org.taxNumber.type, org.taxNumber.value),
      primary: true,
    });
  }
  for (const other of org.otherIdentifiers) {
    identifiers.push({
      type: other.type,
      maskedValue: maskIdentifier(other.type, other.value),
      primary: other.primary,
    });
  }
  const out: Schemas['Organization'] = {
    id: rel.id,
    organizationId: org.organizationId,
    legalName: org.legalName,
    displayName: org.displayName,
    organizationKind: org.organizationKind,
    countryCode: org.countryCode,
    organizationStatus: org.organizationStatus,
    relationshipRole: rel.relationshipRole,
    relationshipStatus: rel.relationshipStatus,
    identifiers,
    validFrom: rel.validFrom,
    rowVersion: rel.rowVersion,
  };
  if (rel.tenantCode !== null) out.tenantCode = rel.tenantCode;
  if (rel.validTo !== null) out.validTo = rel.validTo;
  return out;
}

/** Projects to the list row shape. */
export function toOrganizationSummary(
  rel: StoredRelationship,
  org: StoredOrganization,
): Schemas['OrganizationSummary'] {
  return {
    id: rel.id,
    organizationId: org.organizationId,
    displayName: org.displayName,
    organizationKind: org.organizationKind,
    relationshipRole: rel.relationshipRole,
    relationshipStatus: rel.relationshipStatus,
  };
}

export function toPersonSummary(p: StoredPerson): Schemas['PersonSummary'] {
  const primary = p.identifiers.find((i) => i.primary);
  return {
    id: p.id,
    displayName: [p.firstName, p.middleName, p.lastName].filter(Boolean).join(' '),
    status: p.status,
    maskedPrimaryIdentifier: primary ? maskIdentifier(primary.type as 'TCKN', primary.value) : null,
  };
}

export function toPerson(p: StoredPerson): Schemas['Person'] {
  return {
    ...toPersonSummary(p),
    firstName: p.firstName,
    middleName: p.middleName,
    lastName: p.lastName,
    birthDate: p.birthDate,
    sexAtBirth: p.sexAtBirth,
    identifiers: p.identifiers.map((i) => ({
      type: i.type,
      maskedValue: maskIdentifier(i.type as 'TCKN', i.value),
      primary: i.primary,
    })),
    rowVersion: p.rowVersion,
  };
}
