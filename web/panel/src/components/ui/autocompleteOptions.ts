export interface AutocompleteOption {
  value: string;
  label: string;
}

export const normalizeAutocompleteOptions = (
  options: string[] | { value: string; label?: string }[]
): AutocompleteOption[] =>
  options.map((opt) =>
    typeof opt === 'string'
      ? { value: opt, label: opt }
      : { value: opt.value, label: opt.label || opt.value }
  );

/**
 * The current value only narrows the list while the user is typing. When the dropdown is
 * merely opened with a value already chosen, every option stays reachable so the user can
 * switch without clearing the input first.
 */
export const filterAutocompleteOptions = (
  options: AutocompleteOption[],
  value: string,
  isTyping: boolean
): AutocompleteOption[] => {
  const query = value.trim().toLowerCase();
  if (!isTyping || !query) return options;
  return options.filter(
    (opt) => opt.value.toLowerCase().includes(query) || opt.label.toLowerCase().includes(query)
  );
};
