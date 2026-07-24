import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import { nextTick } from "vue"
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
})
