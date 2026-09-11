import { describe, expect, it } from 'vitest';
import { appsFor, fitsApp } from './apps';

const staff = { permissions: ['claim.read'], personId: null, scopes: [] };
const clerk = {
  permissions: ['claim.read'],
  personId: null,
  scopes: [{ type: 'ORGANIZATION', id: '0190a1b2-0000-7000-8000-000000000001' }],
};
const member = {
  permissions: ['booking.read'],
  personId: '0190a1b2-0000-7000-8000-000000000002',
  scopes: [{ type: 'PERSON', id: '0190a1b2-0000-7000-8000-000000000002' }],
};

describe('which app an account has work in', () => {
  it('places each kind of account in exactly one app per tenant', () => {
    expect(appsFor([staff])).toEqual(['backoffice']);
    expect(appsFor([clerk])).toEqual(['provider']);
    expect(appsFor([member])).toEqual(['member']);
  });

  it('lets an account work in several apps across its tenants', () => {
    expect(appsFor([member, staff])).toEqual(['backoffice', 'member']);
    expect(appsFor([clerk, staff, member])).toEqual(['backoffice', 'provider', 'member']);
  });

  it('follows the server: a person binding wins over an organization grant', () => {
    const both = {
      ...clerk,
      personId: member.personId,
      scopes: [...clerk.scopes, ...member.scopes],
    };
    expect(fitsApp(both, 'member')).toBe(true);
    expect(fitsApp(both, 'provider')).toBe(false);
    expect(fitsApp(both, 'backoffice')).toBe(false);
  });

  it('gives an account with no permissions and no binding no app at all', () => {
    expect(appsFor([{ permissions: [], personId: null, scopes: [] }])).toEqual([]);
  });

  it('does not count an organization grant that names no organization', () => {
    const blank = { ...staff, scopes: [{ type: 'ORGANIZATION', id: null }] };
    expect(fitsApp(blank, 'provider')).toBe(false);
    expect(fitsApp(blank, 'backoffice')).toBe(true);
  });
});
