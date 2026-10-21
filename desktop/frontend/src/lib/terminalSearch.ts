import { terminalLinkSegments } from '@/lib/terminal'
import type { TerminalLine, TerminalRunStyle, TerminalTextSegment } from '@/lib/terminal'

export interface TerminalMatch {
  lineId: number
  start: number
  end: number
}

export interface TerminalSearch {
  matches: TerminalMatch[]
  total: number
}

export interface TerminalSearchSegment extends TerminalTextSegment {
  style: TerminalRunStyle
  parts: Array<{ text: string; matchIndex?: number }>
}

// maximumHighlightedMatches bounds DOM growth for very broad queries without hiding the total count.
export const maximumHighlightedMatches = 5000

// searchTerminalLines searches displayed text, including phrases split by ANSI styles, as literal case-insensitive text.
export function searchTerminalLines(lines: TerminalLine[], query: string): TerminalSearch {
  const result: TerminalSearch = { matches: [], total: 0 }
  if (!query) return result
  const pattern = new RegExp(query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'giu')
  for (const line of lines) {
    const text = line.runs.map(run => run.text).join('')
    for (const match of text.matchAll(pattern)) {
      result.total += 1
      if (result.matches.length < maximumHighlightedMatches) {
        result.matches.push({ lineId: line.id, start: match.index, end: match.index + match[0].length })
      }
    }
  }
  return result
}

// terminalSearchSegments intersects match ranges with styled runs and safe links without interpreting process HTML.
export function terminalSearchSegments(line: TerminalLine, matches: Array<TerminalMatch & { index: number }>): TerminalSearchSegment[] {
  const segments: TerminalSearchSegment[] = []
  let offset = 0
  let matchCursor = 0
  for (const run of line.runs) {
    for (const link of terminalLinkSegments(run.text)) {
      const parts: TerminalSearchSegment['parts'] = []
      let position = 0
      while (position < link.text.length) {
        while (matches[matchCursor] && matches[matchCursor]!.end <= offset + position) matchCursor += 1
        const match = matches[matchCursor]
        const inside = match && match.start <= offset + position
        const end = Math.min(link.text.length, match ? (inside ? match.end : match.start) - offset : link.text.length)
        parts.push({
          text: link.text.slice(position, end),
          matchIndex: inside ? match.index : undefined,
        })
        position = end
      }
      segments.push({ ...link, style: run.style, parts })
      offset += link.text.length
    }
  }
  return segments
}
