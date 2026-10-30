/*
 * How a stacked bar chart looks.
 *
 * `dust` prints one row of block characters per directory, so do `progress`
 * bars and htop's memory meters. xterm can draw those characters itself instead
 * of asking the font, and it paints them across the *whole* cell — including the
 * 15% line-height padding this app uses. Nothing is left between two rows, and
 * eight bars come out as one solid staircase. That is not a hypothesis: rendered
 * through xterm 6's WebGL renderer, `customGlyphs: true` fused every row and
 * `false` left each bar on its own line, which is what other terminals show.
 *
 * The price is box drawing — a `│` from the font leaves the same padding, so a
 * TUI frame gets a hairline gap — so it is a setting. These hold the default in
 * place and prove a live terminal can still be switched.
 */
import { describe, expect, it, vi } from 'vitest';

// The bridge owns a trzsz filter; none of that is what this file is about, and
// the real one insists on a Node-style file picker it will never get here.
vi.mock('trzsz', () => ({
  TrzszFilter: class {
    processServerOutput(): void {}
    processTerminalInput(): void {}
    processBinaryInput(): void {}
    isTransferringFiles(): boolean {
      return false;
    }
    setTerminalColumns(): void {}
    stopTransferringFiles(): void {}
  },
}));

async function makeBridge(continuousGlyphs?: boolean) {
  const { createTerminalBridge } = await import('./terminal');
  return createTerminalBridge({
    id: `glyphs-${Math.random().toString(36).slice(2)}`,
    ...(continuousGlyphs === undefined ? {} : { continuousGlyphs }),
    onSend: vi.fn(),
    onResize: vi.fn(),
  });
}

describe('block characters', () => {
  it('leaves stacked bars separated unless told otherwise', async () => {
    const bridge = await makeBridge();
    expect(bridge.term.options.customGlyphs).toBe(false);
    bridge.dispose();
  });

  it('fills the cell when continuous frames are asked for', async () => {
    const bridge = await makeBridge(true);
    expect(bridge.term.options.customGlyphs).toBe(true);
    bridge.dispose();
  });

  it('switches a terminal that is already open', async () => {
    const bridge = await makeBridge();
    bridge.setContinuousGlyphs(true);
    expect(bridge.term.options.customGlyphs).toBe(true);
    bridge.setContinuousGlyphs(false);
    expect(bridge.term.options.customGlyphs).toBe(false);
    bridge.dispose();
  });
});
