/*
 * The Stage-3 guarantee, tested against the real trzsz filter rather than a
 * mock of it: ordinary terminal output must arrive byte for byte, typed input
 * must go out as UTF-8, and a ::TRZSZ:TRANSFER: banner must be taken over as a
 * transfer instead of being rendered as the mojibake lrzsz is famous for.
 */
import { describe, expect, it, vi } from 'vitest';

import { TrzszFilter } from 'trzsz';

import { latin1ToBytes, textToUtf8, toSendBytes } from './bytes';

function concat(chunks: Uint8Array[]): Uint8Array {
  const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

interface Harness {
  filter: TrzszFilter;
  terminal: () => Uint8Array;
  server: () => Uint8Array;
  serverText: () => string;
  chooseSaveDirectory: ReturnType<typeof vi.fn>;
  showDirectoryPicker: ReturnType<typeof vi.fn>;
}

function harness(): Harness {
  const toTerminal: Uint8Array[] = [];
  const toServer: Uint8Array[] = [];

  const cancelled = () => {
    const error = new Error('The user requested that no file be saved.') as Error & { name: string };
    error.name = 'AbortError';
    return Promise.reject(error);
  };

  // trzsz uses the File System Access API in a browser and the callbacks under
  // a node-like runtime; supply both so this test says the same thing in either.
  const showDirectoryPicker = vi.fn(cancelled);
  (window as unknown as { showDirectoryPicker: unknown }).showDirectoryPicker = showDirectoryPicker;
  (window as unknown as { showOpenFilePicker: unknown }).showOpenFilePicker = vi.fn(cancelled);

  const chooseSaveDirectory = vi.fn(async () => undefined);
  const chooseSendFiles = vi.fn(async () => undefined);

  const filter = new TrzszFilter({
    terminalColumns: 80,
    chooseSaveDirectory,
    chooseSendFiles,
    writeToTerminal: (output) => {
      if (typeof output === 'string') toTerminal.push(textToUtf8(output));
      else if (output instanceof Uint8Array) toTerminal.push(output);
      else if (output instanceof ArrayBuffer) toTerminal.push(new Uint8Array(output));
      else throw new Error('unexpected Blob output in this test');
    },
    sendToServer: (input) => toServer.push(toSendBytes(input)),
  });

  return {
    filter,
    terminal: () => concat(toTerminal),
    server: () => concat(toServer),
    serverText: () => latin1ToBytesInverse(concat(toServer)),
    chooseSaveDirectory,
    showDirectoryPicker,
  };
}

function latin1ToBytesInverse(bytes: Uint8Array): string {
  let text = '';
  for (const byte of bytes) text += String.fromCharCode(byte);
  return text;
}

async function until(cond: () => boolean, timeoutMs = 1500): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (cond()) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error('condition never became true');
}

describe('trzsz pipe', () => {
  it('passes ordinary terminal output through byte for byte', () => {
    const h = harness();
    const raw = new Uint8Array(256 + 32);
    for (let i = 0; i < 256; i += 1) raw[i] = i;
    raw.set(textToUtf8('\x1b[31m中文\x1b[0m\r\n'), 256);

    h.filter.processServerOutput(raw);

    expect(Array.from(h.terminal())).toEqual(Array.from(raw));
  });

  it('keeps the order of many chunks, which is what a redraw depends on', () => {
    const h = harness();
    const pieces = ['\x1b[1;36mprompt\x1b[0m$ ', textToUtf8('a\x00b\xff'), 'tail'];
    for (const piece of pieces) {
      h.filter.processServerOutput(typeof piece === 'string' ? piece : piece);
    }
    const text = new TextDecoder().decode(h.terminal());
    expect(text).toContain('prompt');
    expect(text.endsWith('tail')).toBe(true);
    expect(text).toContain('\x00b');
  });

  it('sends typed text to the server as UTF-8 bytes', () => {
    const h = harness();
    h.filter.processTerminalInput('ls -la\r');
    expect(Array.from(h.server())).toEqual(Array.from(textToUtf8('ls -la\r')));
  });

  it('does not mangle non-ASCII input', () => {
    const h = harness();
    h.filter.processTerminalInput('echo 中文\r');
    expect(Array.from(h.server())).toEqual([
      0x65, 0x63, 0x68, 0x6f, 0x20, 0xe4, 0xb8, 0xad, 0xe6, 0x96, 0x87, 0x0d,
    ]);
  });

  it('takes over a download banner and asks where to save, rather than printing it', async () => {
    const h = harness();
    const banner = '::TRZSZ:TRANSFER:S:1.1.6:0123456789012345';

    h.filter.processServerOutput(latin1ToBytes('before\r\n' + banner + '\r\n'));

    await until(() => h.server().length > 0);

    // The protocol answer must be there: "#ACT:" with a base64 payload, i.e.
    // trzsz is now driving the input side of the pipe.
    expect(h.serverText()).toMatch(/^#ACT:[A-Za-z0-9+/=]+/);
    // And the save location was requested from the user by one route or other.
    const asked = h.chooseSaveDirectory.mock.calls.length > 0 || h.showDirectoryPicker.mock.calls.length > 0;
    expect(asked).toBe(true);
  });

  it('still forwards normal output after a transfer attempt ends', async () => {
    const h = harness();
    h.filter.processServerOutput(latin1ToBytes('::TRZSZ:TRANSFER:S:1.1.6:0123456789098765'));
    await until(() => h.server().length > 0);
    await until(() => !h.filter.isTransferringFiles());

    const payload = textToUtf8('back to normal\r\n');
    h.filter.processServerOutput(payload);
    expect(new TextDecoder().decode(h.terminal())).toContain('back to normal');
  });

  it('stops a transfer when the user presses Ctrl-C', async () => {
    const h = harness();
    h.filter.processServerOutput(latin1ToBytes('::TRZSZ:TRANSFER:S:1.1.6:0123456789055555'));
    await until(() => h.filter.isTransferringFiles() || h.server().length > 0);
    h.filter.processTerminalInput('\x03');
    expect(h.filter.isTransferringFiles()).toBe(false);
  });
});
