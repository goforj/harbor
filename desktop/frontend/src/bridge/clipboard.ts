export async function copyText(text: string): Promise<void> {
  if (window.runtime?.ClipboardSetText) {
    const copied = await window.runtime.ClipboardSetText(text)
    if (!copied) {
      throw new Error('The native clipboard rejected the value.')
    }
    return
  }

  if (!navigator.clipboard?.writeText) {
    throw new Error('Clipboard access is unavailable.')
  }

  await navigator.clipboard.writeText(text)
}
