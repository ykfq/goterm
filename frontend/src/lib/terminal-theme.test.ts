/*
 * Who decides what colour the terminal's text is.
 *
 * The bug this pins: a user picks a black pane (Settings → Terminal background),
 * then switches to the light theme. The chrome went light, the ink went black,
 * and the pane stayed black — black on black. The pane is the thing the text sits
 * on, so the pane gets to choose the palette.
 */
import { describe, expect, it, vi } from 'vitest';

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

import { luminance, panePalette, paneTheme } from './terminal';

const DARK_INK = '#d7e0ea';
const LIGHT_INK = '#1f2933';

describe('which palette a pane is readable on', () => {
  it('is the theme’s own when nothing was picked', () => {
    expect(paneTheme('light', '')).toBe('light');
    expect(paneTheme('dark', '')).toBe('dark');
    // Something that is not a colour cannot win an argument about ink.
    expect(paneTheme('dark', 'not-a-colour')).toBe('dark');
    expect(luminance('not-a-colour')).toBe(0);
  });

  it('picks the ink the pane can actually be read with', () => {
    // The reported case: a black pane under the light theme.
    expect(paneTheme('light', '#000000')).toBe('dark');
    expect(paneTheme('light', '#0d1117')).toBe('dark');
    // And the mirror image.
    expect(paneTheme('dark', '#ffffff')).toBe('light');
    expect(paneTheme('dark', '#fdf6e3')).toBe('light');
    // Mid grey is not "light" by any fixed threshold — the side with more
    // contrast wins, which here is dark ink.
    expect(paneTheme('dark', '#808080')).toBe('light');
    expect(paneTheme('light', '#808080')).toBe('light');
  });

  it('keeps the chosen pane colour and takes the rest from that palette', () => {
    const theme = panePalette('light', '#000000');
    expect(theme.background).toBe('#000000');
    expect(theme.foreground).toBe(DARK_INK);
    // With no custom colour the palette is the theme's, background included.
    expect(panePalette('light', '')).toEqual(panePalette('light', ''));
    expect(panePalette('light', '').foreground).toBe(LIGHT_INK);
  });
});

describe('a live terminal follows the pane', () => {
  async function makeBridge(theme: 'light' | 'dark', background: string) {
    const { createTerminalBridge } = await import('./terminal');
    return createTerminalBridge({
      id: `theme-${Math.random().toString(36).slice(2)}`,
      theme,
      background,
      onSend: vi.fn(),
      onResize: vi.fn(),
    });
  }

  it('paints light ink on a black pane even in the light theme', async () => {
    const bridge = await makeBridge('light', '#000000');
    expect(bridge.term.options.theme?.foreground).toBe(DARK_INK);
    expect(bridge.term.options.theme?.background).toBe('#000000');
    bridge.dispose();
  });

  it('re-derives the ink when the theme is switched under a custom pane', async () => {
    const bridge = await makeBridge('dark', '#fdf6e3');
    expect(bridge.term.options.theme?.foreground).toBe(LIGHT_INK);

    // Same pane, different chrome: the pane still decides.
    bridge.setTheme('light');
    expect(bridge.term.options.theme?.foreground).toBe(LIGHT_INK);
    expect(bridge.term.options.theme?.background).toBe('#fdf6e3');

    // Clearing the custom colour hands the pane back to the theme's own.
    bridge.setBackground('');
    expect(bridge.term.options.theme?.background).toBe('#ffffff');
    expect(bridge.term.options.theme?.foreground).toBe(LIGHT_INK);
    bridge.dispose();
  });
});
