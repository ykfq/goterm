/*
 * Cancelling a transfer, and the noise it used to make.
 *
 * Two separate mechanisms produced it:
 *
 *  1. trzsz-js writes its closing message through Node's `process.stdout`,
 *     which a webview does not have — so cancelling threw "process is not
 *     defined" out of the library even though the cancel had worked.
 *  2. The same message was also being surfaced as an error toast, in whatever
 *     wording trzsz-go happened to use.
 *
 * These tests pin the policy and the shim, not the protocol.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';

import { filterStdoutWrite, isLibraryNoise, isRemoteStackDump, isTransferCancel } from './transfer';
import { bindTrzszStdout } from './terminal';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('isTransferCancel', () => {
  it('recognises the wordings a stop arrives in', () => {
    for (const message of [
      'Stopped and deleted',
      'transfer cancelled',
      'Transfer Canceled',
      'user aborted the transfer',
      'trzsz-go: received interrupt signal',
      '^C',
      'Ctrl-C',
      'No files to upload',
      'the transfer does not start',
      'user closed the session',
    ]) {
      expect(isTransferCancel(message), message).toBe(true);
    }
  });

  it('leaves real failures alone', () => {
    for (const message of [
      'disk full',
      'permission denied: /root/backup.tar',
      'checksum mismatch after 12 MiB',
      'connection reset by peer',
      'the remote closed the channel',
    ]) {
      expect(isTransferCancel(message), message).toBe(false);
    }
  });
});

describe('isRemoteStackDump', () => {
  it('spots a Go crash from the remote binary', () => {
    expect(isRemoteStackDump('panic: runtime error: invalid memory address')).toBe(true);
    expect(isRemoteStackDump('goroutine 12 [running]:')).toBe(true);
    expect(isRemoteStackDump('runtime error: index out of range [3] with length 2')).toBe(true);
  });

  it('does not treat an ordinary message as a crash', () => {
    expect(isRemoteStackDump('timed out waiting for the other side')).toBe(false);
  });
});

describe('isLibraryNoise', () => {
  it('recognises the library failing to decode its own frame', () => {
    expect(isLibraryNoise('decode [eJxjYGBgAAAABAAB] error: invalid stored block lengths')).toBe(true);
  });

  it('does not swallow a decoded, meaningful message', () => {
    expect(isLibraryNoise('disk full')).toBe(false);
  });
});

describe('filterStdoutWrite', () => {
  it('keeps the cleanup a progress bar needs', () => {
    // Cursor restore, alt-screen exit, and the CRLF that ends the message.
    expect(filterStdoutWrite('\x1b8\x1b[0J')).toBe('\x1b8\x1b[0J');
    expect(filterStdoutWrite('\r\n')).toBe('\r\n');
    expect(filterStdoutWrite('\x1b[?25h')).toBe('\x1b[?25h');
  });

  it('drops the narration of a cancel, and a remote crash', () => {
    expect(filterStdoutWrite('Stopped and deleted')).toBeNull();
    expect(filterStdoutWrite('trzsz-go: transfer canceled by user')).toBeNull();
    expect(filterStdoutWrite('panic: runtime error: index out of range')).toBeNull();
  });

  it('passes a real failure through, because someone has to read it', () => {
    expect(filterStdoutWrite('disk full')).toBe('disk full');
  });

  it('writes nothing for an empty chunk', () => {
    expect(filterStdoutWrite('')).toBeNull();
  });
});

describe('the process.stdout shim trzsz needs', () => {
  // The webview has no `process`; Node (where vitest runs) has a real one, so
  // the condition is recreated explicitly.
  function withoutNodeStdout(): void {
    vi.stubGlobal('process', { env: {} });
  }

  it('gives trzsz a stdout, so its exit handler cannot throw', () => {
    withoutNodeStdout();
    const said: string[] = [];
    bindTrzszStdout('tab-1', (text) => said.push(text));

    const stdout = (globalThis as unknown as { process: { stdout: { write: (chunk: string) => boolean } } }).process
      .stdout;
    expect(() => stdout.write('\x1b8\x1b[0J')).not.toThrow();
    expect(() => stdout.write('Stopped and deleted')).not.toThrow();
    expect(() => stdout.write('\r\n')).not.toThrow();

    // Cleanup and the newline arrived; the cancel narration did not.
    expect(said.join('')).toBe('\x1b8\x1b[0J\r\n');
  });

  it('keeps a real failure visible', () => {
    withoutNodeStdout();
    const said: string[] = [];
    bindTrzszStdout('tab-1', (text) => said.push(text));

    const stdout = (globalThis as unknown as { process: { stdout: { write: (chunk: string) => boolean } } }).process
      .stdout;
    stdout.write('disk full');

    expect(said).toEqual(['disk full']);
  });

  it('routes to the pane that was last seen writing, not to a dead one', () => {
    withoutNodeStdout();
    const first: string[] = [];
    const second: string[] = [];
    bindTrzszStdout('tab-1', (text) => first.push(text));
    bindTrzszStdout('tab-2', (text) => second.push(text));

    const stdout = (globalThis as unknown as { process: { stdout: { write: (chunk: string) => boolean } } }).process
      .stdout;
    stdout.write('\r\n');

    expect(first).toEqual([]);
    expect(second).toEqual(['\r\n']);
  });
});
