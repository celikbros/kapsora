import type { EligibilityCheckRequest } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
import { FormField, Input, PageHeader, Select } from '@kapsora/ui';
import { useState } from 'react';

import { EligibilityPane } from './EligibilityPane';
import { MemberPicker, type PickedMember } from './MemberPicker';
import { useLiveEligibility, useProviderOrganizationId, useServiceDefinitions } from './queries';

/**
 * The check on its own, without a request: the same pane the form shows, asked from a
 * form that stops at the question. It is the desk's first move when somebody only wants
 * to know, and it records an evaluation like any other check.
 */
export function EligibilityPage() {
  const { t } = useTranslation();
  const providerOrganizationId = useProviderOrganizationId();
  const definitions = useServiceDefinitions();
  const [member, setMember] = useState<PickedMember | null>(null);
  const [definitionId, setDefinitionId] = useState('');
  const [serviceDate, setServiceDate] = useState(new Date().toISOString().slice(0, 10));
  const [quantity, setQuantity] = useState('1');

  const body: EligibilityCheckRequest | null =
    !member || !definitionId || !serviceDate || !quantity.trim()
      ? null
      : {
          personId: member.id,
          serviceDate,
          ...(providerOrganizationId ? { providerOrganizationId } : {}),
          serviceItems: [{ serviceDefinitionId: definitionId, quantity: quantity.trim() }],
        };
  const check = useLiveEligibility(body);

  return (
    <>
      <PageHeader
        title={t('provider.eligibility.title')}
        description={t('provider.eligibility.intro')}
      />
      <div className="grid gap-6 lg:grid-cols-[3fr_2fr]">
        <form
          className="grid gap-4"
          noValidate
          onSubmit={(e) => e.preventDefault()}
          data-testid="eligibility-form"
        >
          <MemberPicker member={member} onPick={setMember} />
          <div className="grid gap-4 md:grid-cols-3">
            <FormField
              label={t('provider.newRequest.service')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Select
                name="serviceDefinitionId"
                value={definitionId}
                onChange={(e) => setDefinitionId(e.target.value)}
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
            >
              <Input
                name="quantity"
                value={quantity}
                onChange={(e) => setQuantity(e.target.value)}
                inputMode="decimal"
                className="text-right font-mono"
              />
            </FormField>
          </div>
        </form>
        <EligibilityPane
          check={{
            asked: body !== null,
            isPending: check.isPending,
            error: check.error,
            data: check.data,
          }}
        />
      </div>
    </>
  );
}
