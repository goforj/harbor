<script setup lang="ts">
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { bracketMatching, HighlightStyle, StreamLanguage, syntaxHighlighting, type StreamParser } from '@codemirror/language'
import { EditorState } from '@codemirror/state'
import {
  crosshairCursor,
  drawSelection,
  dropCursor,
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  rectangularSelection,
} from '@codemirror/view'
import { tags } from '@lezer/highlight'
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = defineProps<{
  label: string
  modelValue: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

interface DotenvParserState {
  value: boolean
}

const editorRoot = ref<HTMLElement | null>(null)
let editor: EditorView | null = null

const dotenvParser: StreamParser<DotenvParserState> = {
  startState: () => ({ value: false }),
  token(stream, state) {
    if (stream.sol()) state.value = false
    if (stream.eatSpace()) return null
    if (stream.peek() === '#') {
      stream.skipToEnd()
      return 'comment'
    }
    if (!state.value) {
      if (stream.match(/^export(?=\s)/)) return 'keyword'
      if (stream.match(/^[A-Za-z_][A-Za-z0-9_]*/)) return 'variableName'
      if (stream.eat('=')) {
        state.value = true
        return 'operator'
      }
      stream.next()
      return 'invalid'
    }
    const quote = stream.peek()
    if (quote === '"' || quote === '\'') {
      readQuotedValue(stream, quote)
      return 'string'
    }
    if (stream.match(/^\$\{[A-Za-z_][A-Za-z0-9_]*(?::-[^}]*)?\}/)) return 'variableName.special'
    if (stream.match(/^(?:true|false|null)(?=\s|$)/i)) return 'bool'
    if (stream.match(/^[+-]?(?:\d+\.\d+|\d+)(?=\s|$)/)) return 'number'
    stream.match(/^[^\s#$]+/)
    if (stream.pos === stream.start) stream.next()
    return 'string'
  },
}

const dotenvLanguage = StreamLanguage.define(dotenvParser)
const dotenvHighlighting = HighlightStyle.define([
  { tag: tags.comment, color: '#71717a', fontStyle: 'italic' },
  { tag: tags.keyword, color: '#e879f9' },
  { tag: tags.variableName, color: '#7dd3fc', fontWeight: '600' },
  { tag: tags.special(tags.variableName), color: '#67e8f9' },
  { tag: tags.operator, color: '#fb7185' },
  { tag: tags.string, color: '#a7f3d0' },
  { tag: tags.bool, color: '#c4b5fd' },
  { tag: tags.number, color: '#fcd34d' },
  { tag: tags.invalid, color: '#f87171', textDecoration: 'underline' },
])

const dotenvTheme = EditorView.theme({
  '&': {
    height: '100%',
    minHeight: '0',
    backgroundColor: '#050506',
    color: '#f4f4f5',
    fontSize: '12px',
  },
  '&.cm-focused': {
    outline: 'none',
  },
  '.cm-scroller': {
    overflow: 'auto',
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
    lineHeight: '1.65',
  },
  '.cm-content': {
    minHeight: '100%',
    padding: '14px 0',
    caretColor: '#ffffff',
  },
  '.cm-line': {
    padding: '0 16px',
  },
  '.cm-gutters': {
    border: 'none',
    backgroundColor: '#050506',
    color: '#52525b',
  },
  '.cm-lineNumbers .cm-gutterElement': {
    minWidth: '42px',
    padding: '0 10px 0 8px',
  },
  '.cm-activeLine': {
    backgroundColor: '#18181b99',
  },
  '.cm-activeLineGutter': {
    backgroundColor: '#18181b',
    color: '#a1a1aa',
  },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection': {
    backgroundColor: '#7f1d1d99',
  },
  '.cm-cursor, .cm-dropCursor': {
    borderLeftColor: '#ffffff',
  },
})

onMounted(() => {
  if (!editorRoot.value) return
  editor = new EditorView({
    parent: editorRoot.value,
    state: EditorState.create({
      doc: props.modelValue,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        highlightSpecialChars(),
        history(),
        drawSelection(),
        dropCursor(),
        rectangularSelection(),
        crosshairCursor(),
        highlightActiveLine(),
        bracketMatching(),
        keymap.of([indentWithTab, ...defaultKeymap, ...historyKeymap]),
        dotenvLanguage,
        syntaxHighlighting(dotenvHighlighting),
        dotenvTheme,
        EditorView.contentAttributes.of({
          'aria-label': props.label,
          'aria-multiline': 'true',
        }),
        EditorView.updateListener.of((update) => {
          if (update.docChanged) emit('update:modelValue', update.state.doc.toString())
        }),
      ],
    }),
  })
})

watch(() => props.modelValue, (value) => {
  if (!editor || editor.state.doc.toString() === value) return
  editor.dispatch({
    changes: {
      from: 0,
      to: editor.state.doc.length,
      insert: value,
    },
  })
})

onBeforeUnmount(() => {
  editor?.destroy()
  editor = null
})

function readQuotedValue(stream: Parameters<typeof dotenvParser.token>[0], quote: string) {
  stream.next()
  let escaped = false
  while (!stream.eol()) {
    const character = stream.next()
    if (character === quote && !escaped) return
    escaped = character === '\\' && !escaped
    if (character !== '\\') escaped = false
  }
}
</script>

<template>
  <div ref="editorRoot" class="min-h-0 flex-1 overflow-hidden bg-[#050506]" />
</template>
