import { registerBillingAcceptance } from './real-billing-acceptance';

// Explicit claim and booking IDs bind this to an already accepted PC06 source.
// Local payment records exercise arithmetic; no bank transfer or fiscal document is sent.
registerBillingAcceptance({
  title:
    'accepted lodging claim reaches its hotel invoice, worker settlement and exact local payment',
  environmentPrefix: 'E2E_LODGING_BILLING',
  referencePrefix: 'PC06',
  username: 'hotel.billing.a',
  domain: 'ACCOMMODATION',
  sourceType: 'BOOKING',
  total: '1800',
  paidText: '1.800,00',
  net: '1500',
  tax: '300',
  firstPayment: '300',
  finalPayment: '1500',
  excessPayment: '1501',
  screenshots: '.impeccable/review/lodging-billing',
});
