import { useTranslation } from '@kapsora/i18n';
import { FormField, Input, Select } from '@kapsora/ui';

import { usePersonList } from './queries';

/**
 * People are chosen by name, never by identifier (DESIGN.md). Two characters of a name
 * narrow the list; the pick is a select so the chosen person is always one of the
 * server's answers rather than a typed guess. The identifier search, which needs a
 * step-up, is a separate screen on purpose.
 */
export function PersonPicker({
  query,
  onQueryChange,
  personId,
  onPick,
  label,
}: {
  query: string;
  onQueryChange: (value: string) => void;
  personId: string;
  onPick: (id: string) => void;
  label: string;
}) {
  const { t } = useTranslation();
  const trimmed = query.trim();
  const people = usePersonList(
    trimmed.length >= 2 ? { q: trimmed, status: 'ACTIVE', limit: 20 } : { limit: 20 },
  );
  const options = (people.data?.items ?? []).map((person) => ({
    value: person.id,
    label: person.displayName,
  }));

  return (
    <>
      <FormField label={t('people.search')} hint={t('people.searchHint')}>
        <Input
          name="personSearch"
          value={query}
          onChange={(e) => onQueryChange(e.target.value)}
          autoComplete="off"
        />
      </FormField>
      <FormField label={label} required requiredLabel={t('common.requiredMark')}>
        <Select
          name="personId"
          value={personId}
          onChange={(e) => onPick(e.target.value)}
          placeholder={t('common.none')}
          options={options}
        />
      </FormField>
    </>
  );
}
