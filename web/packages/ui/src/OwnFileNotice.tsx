import { useTranslation } from '@kapsora/i18n';

import { cn } from './cn';

/**
 * Stands where the decision controls would be when the file belongs to the reviewer's own
 * person. It says why there is nothing to press and who should decide instead; the server
 * refuses the decision anyway (OWN_FILE_DECISION), so this is the sentence, not the lock.
 */
export function OwnFileNotice({ className }: { className?: string }) {
  const { t } = useTranslation();
  return (
    <p
      role="note"
      data-testid="own-file-notice"
      className={cn(
        'border-line bg-surface-sunken text-fg-muted max-w-prose rounded-md border px-3 py-2 text-sm',
        className,
      )}
    >
      <span className="text-fg font-medium">{t('ownFile.title')}</span> {t('ownFile.body')}
    </p>
  );
}
