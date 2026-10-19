import { describe, expect, it } from 'vitest'
import { creationDefault, creationFieldAvailable, toggleCreationOption } from './projectCreation'
import type { ProjectCreationField } from '@/domain/harbor'

const features: ProjectCreationField = { key: 'features', label: 'Features', step: 'Stack', kind: 'multi', default: [], options: [
  { value: 'api', label: 'API', description: '' },
  { value: 'accounts', label: 'Accounts', description: '', requires: ['api'] },
  { value: 'sqlite', label: 'SQLite', description: '', exclusive_group: 'db' },
  { value: 'postgres', label: 'Postgres', description: '', exclusive_group: 'db' },
] }

describe('stack-neutral creation fields', () => {
  it('resolves dependencies, removal, exclusive choices, and unavailable field defaults without GoForj keys', () => {
    const dependent: ProjectCreationField = { key: 'ui-template', label: 'UI template', kind: 'select', step: 'Stack', default: 'none', depends_on: 'features', requires: ['api'] }
    const values = { features: 'sqlite', 'ui-template': 'custom' }
    toggleCreationOption(features, 'accounts', true, values, [features, dependent])
    expect(values.features.split(',')).toEqual(['sqlite', 'accounts', 'api'])
    toggleCreationOption(features, 'postgres', true, values, [features, dependent])
    expect(values.features).not.toContain('sqlite')
    toggleCreationOption(features, 'api', false, values, [features, dependent])
    expect(values.features).toBe('postgres')
    expect(values['ui-template']).toBe('none')
    expect(creationFieldAvailable(dependent, values)).toBe(false)
  })

  it('shows conditional options only for the advertised parent value', () => {
    const conditional: ProjectCreationField = { key: 'targets', label: 'Targets', kind: 'multi', step: 'Options', default: ['one', 'two'], when: { integration: 'custom' } }
    expect(creationDefault(conditional)).toBe('one,two')
    expect(creationFieldAvailable(conditional, { integration: 'recommended' })).toBe(false)
    expect(creationFieldAvailable(conditional, { integration: 'custom' })).toBe(true)
    expect(creationDefault({ ...conditional, kind: 'boolean', default: false })).toBe('false')
  })
})
