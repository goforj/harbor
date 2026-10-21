import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import { harborWireFixture } from '@/bridge/harbor.fixture'
import PrerequisitePanel from './PrerequisitePanel.vue'

describe('PrerequisitePanel', () => {
  it('shows actionable tool states, retries, and native URL failures', async () => {
    const wrapper = mount(PrerequisitePanel, { props: { report: structuredClone(harborWireFixture.project_prerequisites_report), compact: true } })
    expect(wrapper.text()).toContain('Check required tools')
    expect(wrapper.text()).toContain('Start Docker and recheck.')
    await wrapper.findAll('button').find(button => button.text() === 'Recheck')!.trigger('click')
    expect(wrapper.emitted('recheck')).toHaveLength(1)
    const open = vi.spyOn(harborBridge, 'openTerminalURL').mockRejectedValueOnce(new Error('native opener failed'))
    await wrapper.findAll('button').find(button => button.text().includes('Installation'))!.trigger('click')
    await flushPromises()
    expect(open).toHaveBeenCalledWith('https://docs.docker.com/engine/manage-resources/contexts/')
    expect(wrapper.text()).toContain('Could not open the instructions')
    await wrapper.setProps({ checking: true })
    expect(wrapper.text()).toContain('Checking local tools')
    wrapper.unmount()
  })

  it('keeps runtime-only warnings visible while allowing file creation and compacts completed checks', async () => {
    const report = structuredClone(harborWireFixture.project_prerequisites_report)
    const wrapper = mount(PrerequisitePanel, { props: { report, compact: true, stage: 'creation' } })
    expect(wrapper.text()).toContain('Ready to create; runtime tools need attention')
    expect(wrapper.text()).toContain('Needed when you start the project.')
    await wrapper.setProps({ report: { ...report, checks: report.checks.map(check => ({ ...check, status: 'ready' as const })) } })
    expect(wrapper.text()).toContain('Required tools are ready')
    expect(wrapper.text()).not.toContain('Docker engine')
    await wrapper.findAll('button').find(button => button.text().includes('Details'))!.trigger('click')
    expect(wrapper.text()).toContain('Docker engine')
    wrapper.unmount()
  })
})
