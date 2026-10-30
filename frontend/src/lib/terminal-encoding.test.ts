/*
 * Where a remote's bytes stop being bytes.
 *
 * `paint()` is the single throat between the socket and xterm, and it is the one
 * place that may translate. UTF-8 has to stay a no-op — the same `Uint8Array`, not
 * an equal one, because the app's promise is that what the server sent is what the
 * screen got. A legacy host is the opposite case: GBK's 中文 (`D6 D0 CE C4`) is
 * four bytes xterm cannot make sense of, and the terminal has to receive the two
 * characters instead.
 *
 * These go through a real bridge so the wiring cannot rot: the unit tests on
 * `RemoteTextDecoder` cannot see a decoder that was never plugged in. One bridge
 * for the whole file — every terminal costs a grapheme table, and the suite already
 * runs close to the memory a worker has.
 */
import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest';

// trzsz refuses to construct under Node (it looks for `require('fs')` and then
// demands the Node-only callbacks). Here the question is only what the bridge
// hands to xterm, so the filter is reduced to "pass server output through".
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

const written: (string | Uint8Array)[] = [];
const bridge = createTerminalBridge({
  id: 'enc-pipe',
  onSend: vi.fn(),
  onResize: vi.fn(),
});
// What xterm is handed, captured at emit time.
bridge.term.write = ((input: string | Uint8Array) => {
  written.push(input);
}) as typeof bridge.term.write;

beforeEach(() => {
  written.length = 0;
  bridge.setEncoding('utf-8');
});

afterAll(() => {
  bridge.dispose();
});

const ZHONG_WEN = Uint8Array.from([0xd6, 0xd0, 0xce, 0xc4]);

describe('remote encoding in the output pipe', () => {
  it('hands UTF-8 through as the very same bytes', () => {
    const bytes = Uint8Array.from([0x41, 0xd6, 0xd0, 0xce, 0xc4, 0xff]);
    bridge.write(bytes);
    expect(written).toHaveLength(1);
    // Identity, not equality: a copy would still pass a byte comparison and would
    // also mean a decoder had crept onto the default path.
    expect(written[0]).toBe(bytes);
  });

  it('decodes a GBK host into characters the terminal can draw', () => {
    bridge.setEncoding('gb18030');
    bridge.write(ZHONG_WEN);
    expect(written.join('')).toBe('中文');
  });

  it('keeps a character split across two output chunks whole', () => {
    // The socket cuts wherever it likes; a decoder that is not streaming turns this
    // into two replacement characters.
    bridge.setEncoding('gb18030');
    bridge.write(ZHONG_WEN.subarray(0, 1));
    expect(written.join('')).toBe('');
    bridge.write(ZHONG_WEN.subarray(1));
    expect(written.join('')).toBe('中文');
  });

  it('switches an open terminal over, without waiting for a new tab', () => {
    // The setting used to mean "next time"; a user trying encodings to read a host
    // should not have to reconnect for each guess.
    bridge.setEncoding('gb18030');
    bridge.write(ZHONG_WEN);
    expect(written.join('')).toBe('中文');

    bridge.setEncoding('utf-8');
    const bytes = Uint8Array.from([0x41]);
    bridge.write(bytes);
    expect(written[written.length - 1]).toBe(bytes);
  });

  it('leaves GoTerm’s own words alone', () => {
    // Notices, the trzsz banner and the progress bar are written as strings by us;
    // running them through the GBK decoder would be nonsense.
    bridge.setEncoding('gb18030');
    bridge.term.write('中文 from GoTerm');
    expect(written[0]).toBe('中文 from GoTerm');
  });
});
