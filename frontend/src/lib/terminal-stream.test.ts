import { afterEach, describe, expect, it, vi } from 'vitest';

import { ServerOutputStream, bannerHoldBack, compatBannerBytes, displayBannerBytes } from './terminal';

const MAGIC = '::TRZSZ:TRANSFER:';
const BANNER = '::TRZSZ:TRANSFER:R:1.2.0:';
const BANNER_WITH_NEWLINE = `${BANNER}\n`;

const encoder = new TextEncoder();

function bytes(text: string): Uint8Array {
  return encoder.encode(text);
}

function text(value: Uint8Array): string {
  return new TextDecoder().decode(value);
}

/** Records what a real TrzszFilter would have received. */
function makeSink(transferring = false) {
  const received: Uint8Array[] = [];
  let isTransferring = transferring;
  return {
    received,
    sink: {
      processServerOutput: (chunk: Uint8Array) => received.push(chunk.slice()),
      isTransferringFiles: () => isTransferring,
    },
    setTransferring: (value: boolean) => {
      isTransferring = value;
    },
    chunks: () => received.map((chunk) => text(chunk)),
  };
}

afterEach(() => {
  vi.useRealTimers();
});

describe('bannerHoldBack', () => {
  it('leaves ordinary shell output alone', () => {
    expect(bannerHoldBack(bytes('user@host:~$ ls -la\r\n'))).toBe(0);
    expect(bannerHoldBack(bytes(''))).toBe(0);
  });

  it('holds a trailing fragment that could still become the banner', () => {
    // "::TRZ" is a prefix of the magic, so the rest may still be on its way.
    expect(bannerHoldBack(bytes('hello::TRZ'))).toBe(5);
    expect(bannerHoldBack(bytes('::'))).toBe(2);
  });

  it('holds a whole banner until the newline behind it arrives', () => {
    // trzsz will not look for a banner in fewer than 26 bytes.
    expect(bannerHoldBack(bytes(BANNER))).toBe(BANNER.length);
  });

  it('releases once the banner is complete with its newline', () => {
    expect(bannerHoldBack(bytes(BANNER_WITH_NEWLINE))).toBe(0);
    expect(bannerHoldBack(bytes(`prompt$ ${BANNER_WITH_NEWLINE}more`))).toBe(0);
  });
});

describe('ServerOutputStream', () => {
  it('passes every byte of a banner split across two events in one chunk', () => {
    vi.useFakeTimers();
    const { sink, chunks } = makeSink();
    const stream = new ServerOutputStream(sink, () => undefined);

    stream.write(bytes(MAGIC));
    expect(chunks()).toEqual([]); // nothing yet: the banner is still half here

    stream.write(bytes('R:1.2.0:\n'));
    // trzsz 1.1.6 cannot parse a banner without a unique id, so the stream adds
    // the smallest one — but the screen still gets exactly what the server sent.
    expect(chunks()).toEqual(['::TRZSZ:TRANSFER:R:1.2.0:0\n']);
    expect(displayBannerBytes(chunks()[0] ?? '')).toBe(BANNER_WITH_NEWLINE);

    // Nothing was lost, nothing was duplicated.
    expect(displayBannerBytes(chunks().join(''))).toBe(BANNER_WITH_NEWLINE);
    stream.dispose();
  });

  it('leaves a banner that already carries an id untouched', () => {
    const withId = '::TRZSZ:TRANSFER:S:1.1.6:0123456789012345\r\n';
    expect(text(compatBannerBytes(bytes(withId)))).toBe(withId);
    expect(displayBannerBytes(withId)).toBe(withId);
  });

  it('releases a held fragment even if the server sends nothing more', () => {
    vi.useFakeTimers();
    const { sink, chunks } = makeSink();
    const stream = new ServerOutputStream(sink, () => undefined, 50);

    stream.write(bytes('Password:'));
    // Only the ambiguous ':' is held back; the rest of the line goes through.
    expect(chunks()).toEqual(['Password']);

    vi.advanceTimersByTime(50);
    expect(chunks().join('')).toBe('Password:');
    stream.dispose();
  });

  it('never holds bytes back while a transfer is running', () => {
    const { sink, chunks } = makeSink(true);
    const stream = new ServerOutputStream(sink, () => undefined);

    stream.write(bytes(BANNER));
    expect(chunks()).toEqual([BANNER]);

    stream.dispose();
  });

  it('keeps shell output flowing when the filter throws', () => {
    const failures: string[] = [];
    const stream = new ServerOutputStream(
      { processServerOutput: () => { throw new Error('boom'); }, isTransferringFiles: () => false },
      (chunk) => failures.push(text(chunk)),
    );

    stream.write(bytes('plain output'));
    expect(failures).toEqual(['plain output']);
    stream.dispose();
  });
});
