/*
 * Searching a tab's own buffer.
 *
 * Two things can only be checked with a real terminal: that the search addon's
 * decorations register at all (xterm 6 gates `registerDecoration` behind
 * `allowProposedApi`, and a terminal created without it throws on the first
 * match — the component tests cannot see that, because they mock the bridge), and
 * that the count handed to the find bar comes from the *buffer* rather than from
 * whatever the last chunk of output happened to contain.
 *
 * One bridge for the whole file: each terminal drags in a grapheme table, and the
 * suite runs close to the memory one vitest worker has.
 */
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

// The bridge owns a trzsz filter, reduced here to "pass server output through":
// what this file asks is how the terminal searches, not how trzsz parses.
vi.mock('trzsz', () => ({
  TrzszFilter: class {
    private readonly options: { writeToTerminal: (output: unknown) => void };
    constructor(options: { writeToTerminal: (output: unknown) => void }) {
      this.options = options;
    }
    processServerOutput(bytes: Uint8Array): void {
      this.options.writeToTerminal(bytes);
    }
    processTerminalInput(): void {}
    processBinaryInput(): void {}
    isTransferringFiles(): boolean {
      return false;
    }
    setTerminalColumns(): void {}
    stopTransferringFiles(): void {}
  },
}));

import { createTerminalBridge } from './terminal';

const results: { index: number; count: number }[] = [];

const bridge = createTerminalBridge({
  id: 'find-pipe',
  onSend: vi.fn(),
  onResize: vi.fn(),
  onSearchResults: (info) => results.push({ ...info }),
});

// An *opened* terminal owns a selection service, and the addon selects what it
// finds. Nothing here opens a window (happy-dom has no renderer to open one
// into), so only that one step is replaced — searching, counting and decorating
// all stay real.
const term = bridge.term as unknown as {
  hasSelection: () => boolean;
  select: (column: number, row: number, length: number) => void;
};
term.hasSelection = () => false;
term.select = () => undefined;

/** Server output in through the pipe, then xterm's parser caught up. */
function feed(text: string): Promise<void> {
  return new Promise((resolve) => {
    bridge.write(new TextEncoder().encode(text));
    bridge.term.write('', () => resolve());
  });
}

beforeAll(async () => {
  await feed('alpha\nBRAVO gamma\nbravo delta\nbravo\n');
});

beforeEach(() => {
  results.length = 0;
  bridge.clearSearch();
});

afterAll(() => {
  bridge.dispose();
});

describe('search a tab', () => {
  it('finds matches, decorations and all', () => {
    // A throw here is the failure the component tests hide: without
    // `allowProposedApi` the addon cannot register its decorations, and find dies
    // in the real app on the first keystroke.
    expect(bridge.findNext('bravo')).toBe(true);
    expect(results.at(-1)?.count).toBe(3);
  });

  it('walks forward and back through them', () => {
    expect(bridge.findNext('bravo')).toBe(true);
    expect(bridge.findNext('bravo')).toBe(true);
    expect(bridge.findPrevious('bravo')).toBe(true);
    expect(results.at(-1)?.count).toBe(3);
  });

  it('honours match case', () => {
    expect(bridge.findNext('BRAVO', { caseSensitive: true })).toBe(true);
    expect(results.at(-1)?.count).toBe(1);
  });

  it('reports nothing for a query that is not there', () => {
    expect(bridge.findNext('zulu')).toBe(false);
    // `index` is `resultIndex + 1`, so "no match" has to come out as 0 rather than
    // let the addon's -1 reach the bar as `-1 / 0`.
    expect(results.at(-1)).toEqual({ index: 0, count: 0 });
  });

  it('clears what it highlighted, and can search again afterwards', () => {
    expect(bridge.findNext('alpha')).toBe(true);
    bridge.clearSearch();
    expect(bridge.findNext('alpha')).toBe(true);
    expect(results.at(-1)?.count).toBe(1);
  });
});
