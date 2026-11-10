/*
 * What the terminal admits it has not drawn yet.
 *
 * `term.write` only queues, so the queue is the app's memory during a firehose.
 * These tests pin the accounting the store turns into flow control — and the
 * scrollback bound, which is the other half of a tab's retained bytes.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';

// happy-dom has no WebGL2; the addon complains asynchronously after the
// bridge's own try/catch, which vitest reports as an unhandled error. The
// renderer is not what this file is about.
vi.mock('@xterm/addon-webgl', () => ({
  WebglAddon: class {
    constructor(..._args: unknown[]) {}
    onContextLoss(_handler: () => void): void {
      /* nothing */
    }
    dispose(): void {
      /* nothing */
    }
  },
}));

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
      /* nothing */
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
    uploadFiles(): Promise<void> {
      return Promise.resolve();
    }
  },
}));

vi.mock('./clipboard', () => ({
  writeClipboard: vi.fn(async () => true),
  readClipboard: vi.fn(async () => ''),
}));

vi.mock('./backend', () => ({
  backend: {
    sendInputB64: vi.fn(async () => undefined),
    resizePTY: vi.fn(async () => undefined),
  },
  fileBridgeApi: () => ({}),
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import { SCROLLBACK_MAX, SCROLLBACK_MIN, createTerminalBridge } from './terminal';

const bridges: { dispose: () => void }[] = [];

function makeBridge(onBacklog?: (queued: number) => void) {
  const bridge = createTerminalBridge({
    id: `backlog-${bridges.length}`,
    cols: 100,
    rows: 30,
    onSend: () => undefined,
    onResize: () => undefined,
    onBacklog,
  });
  bridges.push(bridge);
  return bridge;
}

afterEach(() => {
  for (const bridge of bridges.splice(0)) bridge.dispose();
});

describe('output backlog', () => {
  it('counts what has been written but not parsed, then drains to zero', async () => {
    const seen: number[] = [];
    const bridge = makeBridge((queued) => seen.push(queued));

    bridge.writeText('x'.repeat(512 * 1024));
    expect(bridge.queuedBytes()).toBe(512 * 1024);
    expect(seen[0]).toBe(512 * 1024);

    // xterm parses on its own schedule; the callback is what reports the drain.
    await vi.waitFor(() => expect(bridge.queuedBytes()).toBe(0), { timeout: 5000 });
    expect(seen.at(-1)).toBe(0);
  });

  it('counts server bytes too, through the transfer pipeline', async () => {
    const bridge = makeBridge();
    const bytes = new TextEncoder().encode('line\r\n'.repeat(2000));
    bridge.write(bytes);
    // Whatever the decoder and the filter do with them, they were handed to
    // xterm and have to be counted until parsed.
    expect(bridge.queuedBytes()).toBeGreaterThan(0);
    await vi.waitFor(() => expect(bridge.queuedBytes()).toBe(0), { timeout: 5000 });
  });
});

describe('scrollback', () => {
  it('keeps the setting inside the bounds the terminal can honour', () => {
    const bridge = makeBridge();
    bridge.setScrollback(10);
    expect(bridge.term.options.scrollback).toBe(SCROLLBACK_MIN);
    bridge.setScrollback(999_999);
    expect(bridge.term.options.scrollback).toBe(SCROLLBACK_MAX);
    bridge.setScrollback(1234);
    expect(bridge.term.options.scrollback).toBe(1234);
  });

  it('applies the size it was created with', () => {
    const bridge = createTerminalBridge({
      id: 'created-with-scrollback',
      onSend: () => undefined,
      onResize: () => undefined,
      scrollback: 2500,
    });
    bridges.push(bridge);
    expect(bridge.term.options.scrollback).toBe(2500);
  });
});
