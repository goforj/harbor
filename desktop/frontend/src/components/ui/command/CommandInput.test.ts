import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import { nextTick, ref } from "vue"
import Command from "./Command.vue"
import CommandGroup from "./CommandGroup.vue"
import CommandInput from "./CommandInput.vue"
import CommandItem from "./CommandItem.vue"
import CommandList from "./CommandList.vue"

describe("CommandInput", () => {
  it("keeps arrow navigation inside the command list", async () => {
    const wrapper = mount({
      components: { Command, CommandGroup, CommandInput, CommandItem, CommandList },
      template: `
        <Command>
          <CommandInput />
          <CommandList>
            <CommandGroup heading="Results">
              <CommandItem value="alpha">Alpha</CommandItem>
              <CommandItem value="beta">Beta</CommandItem>
            </CommandGroup>
          </CommandList>
        </Command>
      `,
    })
    const input = wrapper.get<HTMLInputElement>('[data-slot="command-input"]')

    expect(input.attributes("autocomplete")).toBe("off")
    expect(input.attributes("autocapitalize")).toBe("off")
    expect(input.attributes("autocorrect")).toBe("off")
    expect(input.attributes("spellcheck")).toBe("false")

    for (const key of ["ArrowDown", "ArrowUp"]) {
      const event = new KeyboardEvent("keydown", {
        key,
        bubbles: true,
        cancelable: true,
      })
      input.element.dispatchEvent(event)
      await nextTick()
      expect(event.defaultPrevented).toBe(true)
    }

    expect(wrapper.find('[data-slot="command-item"][data-highlighted]').exists()).toBe(true)
  })
  it("updates filtered results when entries are renamed, added, or removed", async () => {
    const entries = ref([{ id: "one", value: "Redis" }])
    const wrapper = mount({
      components: { Command, CommandGroup, CommandInput, CommandItem, CommandList },
      setup: () => ({ entries }),
      template: `<Command><CommandInput /><CommandList><CommandGroup heading="Results"><CommandItem v-for="entry in entries" :key="entry.id" :value="entry.value">{{ entry.value }}</CommandItem></CommandGroup></CommandList></Command>`,
    })
    const visible = () => wrapper.findAll('[data-slot="command-item"]')
    await wrapper.get('input').setValue("mysql")
    await nextTick()
    expect(visible()).toHaveLength(0)
    entries.value[0]!.value = "MySQL primary"
    await nextTick()
    await nextTick()
    expect(visible().map(item => item.text())).toEqual(["MySQL primary"])
    entries.value.push({ id: "two", value: "MySQL replica" }, { id: "three", value: "Grafana" })
    await nextTick()
    await nextTick()
    expect(visible()).toHaveLength(2)
    entries.value.splice(0, 2)
    await nextTick()
    await nextTick()
    expect(visible()).toHaveLength(0)
    expect(wrapper.get('[data-slot="command-group"]').attributes('hidden')).toBeDefined()
    wrapper.unmount()
  })

})
