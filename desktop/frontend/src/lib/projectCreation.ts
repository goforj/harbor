import type { ProjectCreationField } from '@/domain/harbor'

export function creationFieldAvailable(field: ProjectCreationField, values: Record<string, string>): boolean {
  if (Object.entries(field.when ?? {}).some(([key, value]) => values[key] !== value)) return false
  const selected = (values[field.depends_on ?? ''] ?? '').split(',')
  return !field.requires?.some(key => !selected.includes(key))
}

export function creationDefault(field: ProjectCreationField): string {
  return Array.isArray(field.default) ? field.default.join(',') : String(field.default)
}

export function toggleCreationOption(field: ProjectCreationField, value: string, selected: boolean, values: Record<string, string>, fields: ProjectCreationField[]): void {
  const options = field.options ?? []
  const choices = new Set((values[field.key] ?? '').split(',').filter(Boolean))
  if (selected) {
    const group = options.find(option => option.value === value)?.exclusive_group
    if (group) for (const option of options) if (option.exclusive_group === group) choices.delete(option.value)
    choices.add(value)
    for (let changed = true; changed;) {
      changed = false
      for (const option of options) if (choices.has(option.value)) for (const required of option.requires ?? []) {
        if (!choices.has(required)) { choices.add(required); changed = true }
      }
    }
  } else {
    choices.delete(value)
    for (let changed = true; changed;) {
      changed = false
      for (const option of options) if (choices.has(option.value) && option.requires?.some(key => !choices.has(key))) {
        choices.delete(option.value); changed = true
      }
    }
  }
  values[field.key] = [...choices].join(',')
  for (const dependent of fields) {
    if (dependent.depends_on === field.key && !creationFieldAvailable(dependent, values)) values[dependent.key] = creationDefault(dependent)
  }
}
