import type { EligibilityCheckRequest, ServiceUnitType } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
import { Button, FormField, Input, PageHeader, ProblemAlert, Select, useToast } from '@kapsora/ui';
import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { EligibilityPane } from './EligibilityPane';
import { MemberPicker, type PickedMember } from './MemberPicker';
import { problemOf } from './problems';
import {
  useCreateAndSubmit,
  useLiveEligibility,
  useProviderOrganizationId,
  useServiceDefinitions,
} from './queries';

/**
 * The home page is the form, and the answer arrives while the question is being typed:
 * the right column asks the server as soon as a member, a service and a date are named,
 * and asks again whenever one of them changes.
 *
 * The answer is also what makes the request possible. A provider may read no programs
 * and no enrollments, so the enrollment a request draws on comes from the check's own
 * result, and Gönder exists only once the check has found it. Gönder creates the draft
 * and submits it in one act, because a desk between patients has no use for a draft.
 */
export function NewRequestPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const providerOrganizationId = useProviderOrganizationId();
  const definitions = useServiceDefinitions();
  const submitAll = useCreateAndSubmit();
  const [member, setMember] = useState<PickedMember | null>(null);
  const [definitionId, setDefinitionId] = useState('');
  const [unitType, setUnitType] = useState<ServiceUnitType>('SESSION');
  const [serviceDate, setServiceDate] = useState(new Date().toISOString().slice(0, 10));
  const [quantity, setQuantity] = useState('1');
  const [amount, setAmount] = useState('');

  // The question the right column asks, or null while it cannot be asked yet. The query
  // key is hashed structurally, so building it every render costs nothing.
  const checkBody: EligibilityCheckRequest | null =
    !member || !definitionId || !serviceDate || !quantity.trim()
      ? null
      : {
          personId: member.id,
          serviceDate,
          ...(providerOrganizationId ? { providerOrganizationId } : {}),
          serviceItems: [
            {
              serviceDefinitionId: definitionId,
              quantity: quantity.trim(),
              ...(amount.trim() ? { requestedAmount: amount.trim(), currencyCode: 'TRY' } : {}),
            },
          ],
        };
  const check = useLiveEligibility(checkBody);
  // The check names the enrollment when it found exactly one. That, not a verdict, is
  // what a request needs: a service the catalog has not mapped to an entitlement yet is
  // "review required", which is a request that goes to review — not one that cannot exist.
  const enrollmentId = check.data?.enrollmentId ?? null;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!member || !enrollmentId) return;
    try {
      const result = await submitAll.mutateAsync({
        personId: member.id,
        enrollmentId,
        requestType: 'DIRECT_SERVICE',
        channel: 'PROVIDER_PORTAL',
        serviceDate,
        ...(providerOrganizationId ? { providerOrganizationId } : {}),
        items: [
          {
            serviceDefinitionId: definitionId,
            unitType,
            requestedQuantity: quantity.trim(),
            ...(amount.trim() ? { requestedAmount: amount.trim(), currencyCode: 'TRY' } : {}),
          },
        ],
      });
      toast.notify({
        tone: 'success',
        title: t('provider.newRequest.created', { reference: result.data.reference }),
      });
      await navigate({ to: '/requests/$requestId', params: { requestId: result.data.id } });
    } catch {
      // The alert in the form carries the problem.
    }
  }

  return (
    <>
      <PageHeader
        title={t('provider.newRequest.title')}
        description={t('provider.newRequest.intro')}
      />
      <div className="grid gap-6 lg:grid-cols-[3fr_2fr]">
        <form onSubmit={submit} className="grid gap-4" noValidate data-testid="new-request-form">
          <ProblemAlert problem={submitAll.error ? problemOf(submitAll.error) : null} />
          <MemberPicker member={member} onPick={setMember} />
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('provider.newRequest.service')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Select
                name="serviceDefinitionId"
                value={definitionId}
                onChange={(e) => {
                  setDefinitionId(e.target.value);
                  const picked = definitions.data?.find((d) => d.value === e.target.value);
                  if (picked) setUnitType(picked.unitType);
                }}
                placeholder={t('common.none')}
                options={(definitions.data ?? []).map((d) => ({ value: d.value, label: d.label }))}
              />
            </FormField>
            <FormField
              label={t('provider.newRequest.serviceDate')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="serviceDate"
                type="date"
                value={serviceDate}
                onChange={(e) => setServiceDate(e.target.value)}
              />
            </FormField>
            <FormField
              label={t('provider.newRequest.quantity')}
              required
              requiredLabel={t('common.requiredMark')}
              hint={t(`units.${unitType}`)}
            >
              <Input
                name="requestedQuantity"
                value={quantity}
                onChange={(e) => setQuantity(e.target.value)}
                inputMode="decimal"
                className="text-right font-mono"
              />
            </FormField>
            <FormField label={t('provider.newRequest.amount')}>
              <Input
                name="requestedAmount"
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
                inputMode="decimal"
                className="text-right font-mono"
              />
            </FormField>
          </div>
          {/* Absent, not disabled, until the answer on the right has found the enrollment. */}
          {enrollmentId ? (
            <div className="flex justify-end">
              <Button type="submit" loading={submitAll.isPending}>
                {t('provider.newRequest.submit')}
              </Button>
            </div>
          ) : null}
        </form>
        {/* Narrow screens read top to bottom: the answer comes before the act. */}
        <div className="order-first lg:order-none">
          <EligibilityPane
            check={{
              asked: checkBody !== null,
              isPending: check.isPending,
              error: check.error,
              data: check.data,
            }}
          />
        </div>
      </div>
    </>
  );
}
