import { describe, expect, it } from 'vitest'
import { TerminalModel } from './terminal'
import { maximumHighlightedMatches, searchTerminalLines, terminalSearchSegments } from './terminalSearch'

// renderedLines exercises search against the same terminal controls and retained rows used by the viewer.
function renderedLines(output: string) {
  const model = new TerminalModel()
  model.feed(output)
  return model.renderLines()
}

describe('terminal search', () => {
  it('finds case-insensitive literal text across ANSI styles and keeps Unicode offsets', () => {
    const lines = renderedLines('😀 RE\u001b[31mADY\u001b[0m [db].* ready\nretry')
    expect(searchTerminalLines(lines, 'ready').matches.map(match => [match.start, match.end])).toEqual([[3, 8], [16, 21]])
    expect(searchTerminalLines(lines, '[db].*').total).toBe(1)
    expect(searchTerminalLines(lines, '').matches).toEqual([])
    expect(searchTerminalLines(lines, 'absent').total).toBe(0)
  })

  it('searches visible redraws rather than erased progress or raw escape sequences', () => {
    const lines = renderedLines('old output\r\u001b[2Knew output')
    expect(searchTerminalLines(lines, 'old').total).toBe(0)
    expect(searchTerminalLines(lines, 'new').total).toBe(1)
  })

  it('preserves safe links and ANSI styles when a match crosses a run boundary', () => {
    const lines = renderedLines('re\u001b[31mady\u001b[0m https://orders.test/ready')
    const result = searchTerminalLines(lines, 'ready')
    const segments = terminalSearchSegments(lines[0]!, result.matches.map((match, index) => ({ ...match, index })))
    expect(segments.flatMap(segment => segment.parts).filter(part => part.matchIndex === 0).map(part => part.text).join('')).toBe('ready')
    expect(segments.find(segment => segment.text === 'ady')?.style.color).toBe('var(--terminal-red)')
    expect(segments.find(segment => segment.parts.some(part => part.matchIndex === 1))?.url).toBe('https://orders.test/ready')
  })

  it('bounds highlighted results while counting every retained match', () => {
    const result = searchTerminalLines(renderedLines('a '.repeat(3100) + '\n' + 'a '.repeat(3100)), 'a')
    expect(result.total).toBeGreaterThan(maximumHighlightedMatches)
    expect(result.matches).toHaveLength(maximumHighlightedMatches)
  })
})
