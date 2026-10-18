import { harborWireFixture } from './harbor.fixture'
import type { WailsAppBindings, WailsEventName, WailsEventPayloads } from './harbor.fixture'

type NativeRuntime = Pick<typeof import('@wailsio/runtime'), 'Call' | 'Events' | 'Clipboard'>

/** Adapts the pinned v3 transport to Harbor's existing Go-owned method and event contract. */
export function installWailsV3Bridge(native: NativeRuntime): void {
  const app = Object.fromEntries(
    Object.values(harborWireFixture.methods).map(name => [
      name,
      (...args: unknown[]) => native.Call.ByName(`main.App.${name}`, ...args),
    ]),
  ) as unknown as WailsAppBindings

  window.go = { main: { App: app } }
  window.runtime = {
    EventsOn<Name extends WailsEventName>(name: Name, callback: (payload: WailsEventPayloads[Name]) => void) {
      return native.Events.On(name, event => callback(event.data as WailsEventPayloads[Name]))
    },
    EventsOff(name: WailsEventName) { native.Events.Off(name) },
    async ClipboardSetText(text: string) {
      await native.Clipboard.SetText(text)
      return true
    },
  }
}

/** Loads the native runtime only inside Wails, keeping browser fixtures independent of native IPC. */
export async function bootstrapWailsV3(): Promise<void> {
  // Packaged windows have platform-specific origins; native development may expose the bootstrap marker.
  const nativeWindow = window as Window & { _wails?: unknown }
  if (location.protocol !== 'wails:' && location.hostname !== 'wails.localhost' && nativeWindow._wails === undefined) return
  installWailsV3Bridge(await import('@wailsio/runtime'))
}
