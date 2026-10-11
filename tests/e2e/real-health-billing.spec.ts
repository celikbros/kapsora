import { registerBillingAcceptance } from './real-billing-acceptance';

registerBillingAcceptance({
  title:
    'accepted inpatient claim reaches a scanned invoice, worker settlement and exact local payment',
  environmentPrefix: 'E2E_HEALTH_BILLING',
  referencePrefix: 'PC05',
  username: 'billing.a',
  domain: 'HEALTH',
  sourceType: 'HEALTH_CASE',
  total: '800',
  dueDays: 30,
  paidText: '800,00',
  net: '666.67',
  tax: '133.33',
  firstPayment: '300',
  finalPayment: '500',
  excessPayment: '501',
  screenshots: '.impeccable/review/health-billing',
});
