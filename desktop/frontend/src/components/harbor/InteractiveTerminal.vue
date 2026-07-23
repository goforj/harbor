<script setup lang="ts">
import '@xterm/xterm/css/xterm.css'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { TerminalDimensions, TerminalSession } from '@/lib/terminalSession'

const props = withDefaults(defineProps<{
  session: TerminalSession
  ariaLabel?: string
}>(), {
  ariaLabel: 'Interactive terminal',
})

const emit = defineEmits<{
  error: [error: Error]
  ready: [dimensions: TerminalDimensions]
}>()

const container = ref<HTMLElement | null>(null)
let terminal: Terminal | null = null
let fitAddon: FitAddon | null = null
let resizeObserver: ResizeObserver | null = null
let removeOutputListener: (() => void) | null = null
let disposeInputListener: (() => void) | null = null
let lastDimensions: TerminalDimensions | null = null
let disposed = false

onMounted(() => {
  void initialize()
})

onBeforeUnmount(() => {
  disposed = true
  resizeObserver?.disconnect()
  resizeObserver = null
  removeOutputListener?.()
  removeOutputListener = null
  disposeInputListener?.()
  disposeInputListener = null
  fitAddon?.dispose()
  fitAddon = null
  terminal?.dispose()
  terminal = null
  void reportSessionError(props.session.close())
})

// initialize connects the emulator before starting the transport so early process output has a destination.
async function initialize() {
  const target = container.value
  if (!target || disposed) return

  const emulator = new Terminal({
    cursorBlink: true,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
    fontSize: 13,
    scrollback: 10_000,
    theme: {
      background: '#09090b',
      foreground: '#e4e4e7',
    },
  })
  const fit = new FitAddon()
  terminal = emulator
  fitAddon = fit
  emulator.loadAddon(fit)
  emulator.open(target)

  const inputListener = emulator.onData((data) => {
    void reportSessionError(props.session.write(data))
  })
  disposeInputListener = () => inputListener.dispose()
  removeOutputListener = props.session.onOutput((data) => emulator.write(data))

  resizeObserver = new ResizeObserver(() => resizeToContainer())
  resizeObserver.observe(target)
  resizeToContainer()

  await reportSessionError(props.session.start())
}

// resizeToContainer keeps the PTY's grid in lockstep with the emulator's fitted viewport.
function resizeToContainer() {
  const emulator = terminal
  const fit = fitAddon
  if (!emulator || !fit || disposed) return

  fit.fit()
  const dimensions = { cols: emulator.cols, rows: emulator.rows }
  if (lastDimensions?.cols === dimensions.cols && lastDimensions.rows === dimensions.rows) return
  lastDimensions = dimensions
  emit('ready', dimensions)
  void reportSessionError(props.session.resize(dimensions))
}

// reportSessionError turns asynchronous transport failures into a component event without leaking rejections.
async function reportSessionError(result: void | Promise<void>) {
  try {
    await result
  }
  catch (error) {
    emit('error', error instanceof Error ? error : new Error(String(error)))
  }
}
</script>

<template>
  <div ref="container" class="harbor-interactive-terminal h-full min-h-0 w-full overflow-hidden bg-zinc-950" :aria-label="ariaLabel" role="application" />
</template>
