import type { AccessPurpose } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
import { Button, Dialog, FormField, Select, Textarea } from '@kapsora/ui';
import { useState } from 'react';

import { ACCESS_PURPOSES } from './access';

/**
 * The question a sensitive record asks before it opens: why. It is a real dialog with the
 * purpose list and a reason, it says that the look is recorded, and declining is a real
 * button that leads somewhere — to the financial projection, with a sentence saying why
 * the clinical half is not there.
 */
export function PurposeDialog({
  open,
  defaultPurpose,
  onGrant,
  onDecline,
}: {
  open: boolean;
  defaultPurpose: AccessPurpose;
  onGrant: (purpose: AccessPurpose, reason: string) => void;
  onDecline: () => void;
}) {
  const { t } = useTranslation();
  const [purpose, setPurpose] = useState<AccessPurpose>(defaultPurpose);
  const [reason, setReason] = useState('');
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onDecline();
      }}
      title={t('review.purpose.title')}
      description={t('review.purpose.intro')}
      actions={
        <>
          <Button variant="secondary" onClick={onDecline} data-testid="purpose-decline">
            {t('review.purpose.decline')}
          </Button>
          <Button onClick={() => onGrant(purpose, reason)} data-testid="purpose-confirm">
            {t('review.purpose.confirm')}
          </Button>
        </>
      }
    >
      <div className="grid gap-3" data-testid="purpose-dialog">
        <FormField
          label={t('review.purpose.purpose')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          <Select
            name="purpose"
            value={purpose}
            onChange={(e) => setPurpose(e.target.value as AccessPurpose)}
            options={ACCESS_PURPOSES.map((code) => ({
              value: code,
              label: t(`review.purpose.codes.${code}`),
            }))}
          />
        </FormField>
        <FormField label={t('review.purpose.reason')} hint={t('review.purpose.reasonHint')}>
          <Textarea
            name="reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            rows={2}
          />
        </FormField>
        <p className="text-fg-muted text-sm">{t('review.purpose.recorded')}</p>
      </div>
    </Dialog>
  );
}
