/*
 * Proof that keyword colouring sits *in* the pipe, not beside it: bytes written
 * to the bridge reach xterm already decorated, and reach it untouched when the
 * feature is off.
 */
import { describe, expect, it, vi } from 'vitest';

// trzsz refuses to construct under Node (it looks for `require('fs')` and then
// demands the Node-only callbacks). The real filter has its own tests; here the
// question is only what the bridge hands to xterm, so the filter is reduced to
// "pass server output to writeToTerminal".
vi.mock('trzsz', () => ({
  TrzszFilter: class {
    private readonly options: { writeToTerminal: (output: unknown) => void };
    constructor(options: { writeToTerminal: (output: unknown) => void }) {
      this.options = options;
    }
    processServerOutput(bytes: Uint8Array): void {
      this.options.writeToTerminal(bytes);
    }
    processTerminalInput(): void {
      /* the upload request is not what this file tests */
    }
    processBinaryInput(): void {
      /* nothing */
    }
    isTransferringFiles(): boolean {
      return false;
    }
    setTerminalColumns(): void {
      /* nothing */
    }
    stopTransferringFiles(): void {
      /* nothing */
    }
  },
}));

import { createTerminalBridge } from './terminal';

function makeBridge(highlight: string[]) {
  const bridge = createTerminalBridge({
    id: `term-${Math.random().toString(36).slice(2)}`,
    onSend: vi.fn(),
    onResize: vi.fn(),
    onNotice: vi.fn(),
    highlight,
  });
  const written: (string | Uint8Array)[] = [];
  // The bridge captures `term` and calls term.write at emit time, so replacing
  // the method here sees exactly what xterm would have been handed.
  bridge.term.write = ((input: string | Uint8Array) => {
    written.push(input);
  }) as typeof bridge.term.write;
  return { bridge, written };
}

function text(written: (string | Uint8Array)[]): string {
  return written
    .map((chunk) => (typeof chunk === 'string' ? chunk : new TextDecoder().decode(chunk)))
    .join('');
}

describe('keyword colouring in the output pipe', () => {
  it('colours what reaches xterm', () => {
    const { bridge, written } = makeBridge(['shell']);
    bridge.write(new TextEncoder().encode('sudo systemctl restart nginx\n'));
    const out = text(written);
    expect(out).toContain('\x1b[1;36msudo');
    // The visible characters are exactly what the server sent.
    expect(out.replace(/\x1b\[[0-9;]*m/g, '')).toBe('sudo systemctl restart nginx\n');
  });

  it('hands xterm the exact bytes when colouring is off', () => {
    const { bridge, written } = makeBridge([]);
    const bytes = new TextEncoder().encode('sudo systemctl restart nginx\n');
    bridge.write(bytes);
    expect(written).toHaveLength(1);
    expect(Array.from(written[0] as Uint8Array)).toEqual(Array.from(bytes));
  });

  it('can be switched on and off at runtime', () => {
    const { bridge, written } = makeBridge([]);
    bridge.setHighlight(['syslog']);
    bridge.write(
      new TextEncoder().encode('Oct 23 04:11:02 web-01 sshd[4012]: Failed password for root\n'),
    );
    expect(text(written)).toContain('\x1b[1;31mFailed');

    written.length = 0;
    bridge.setHighlight([]);
    const plain = new TextEncoder().encode('ls\n');
    bridge.write(plain);
    expect(Array.from(written[0] as Uint8Array)).toEqual(Array.from(plain));
  });
});

describe('terminal background', () => {
  it('paints the canvas and keeps the theme palette', () => {
    const { bridge } = makeBridge([]);
    const before = bridge.term.options.theme;
    expect(before?.background).toBe('#0d1117');

    bridge.setBackground('#002b36');
    expect(bridge.term.options.theme?.background).toBe('#002b36');
    // Everything else stays the dark palette: only the pane changed.
    expect(bridge.term.options.theme?.foreground).toBe(before?.foreground);

    // Switching theme keeps the chosen pane.
    bridge.setTheme('light');
    expect(bridge.term.options.theme?.background).toBe('#002b36');

    bridge.setBackground('');
    expect(bridge.term.options.theme?.background).toBe('#ffffff');
  });
});
