import { describe, expect, it } from 'vitest';

import {
  base64ToBytes,
  bytesToBase64,
  formatBytes,
  latin1ToBytes,
  shortFingerprint,
  textToUtf8,
  toDate,
  toSendBytes,
  utf8ToText,
} from './bytes';

describe('base64 round trip', () => {
  it('survives the whole byte range', () => {
    const raw = new Uint8Array(256);
    for (let i = 0; i < 256; i += 1) raw[i] = i;
    expect(Array.from(base64ToBytes(bytesToBase64(raw)))).toEqual(Array.from(raw));
  });

  it('survives payloads larger than the encoder chunk size', () => {
    // 200 KiB crosses the 32 KiB chunks the encoder uses to keep btoa's input
    // small; an off-by-one there would truncate or duplicate a chunk.
    const raw = new Uint8Array(200 * 1024);
    for (let i = 0; i < raw.length; i += 1) raw[i] = (i * 31 + 7) & 0xff;
    const round = base64ToBytes(bytesToBase64(raw));
    expect(round.length).toBe(raw.length);
    expect(Array.from(round)).toEqual(Array.from(raw));
  });

  it('matches what Go produces for the same bytes', () => {
    // base64.StdEncoding of "hello" — the Go side uses the same alphabet and
    // padding, so a mismatch here means one side invented its own dialect.
    expect(bytesToBase64(textToUtf8('hello'))).toBe('aGVsbG8=');
    expect(Array.from(base64ToBytes('aGVsbG8='))).toEqual([104, 101, 108, 108, 111]);
  });

  it('ignores whitespace a copy-paste may have added', () => {
    expect(Array.from(base64ToBytes('aGVs\nbG8=\r\n'))).toEqual([104, 101, 108, 108, 111]);
  });

  it('handles empty input', () => {
    expect(bytesToBase64(new Uint8Array(0))).toBe('');
    expect(base64ToBytes('').length).toBe(0);
  });

  it('rejects garbage rather than returning partial bytes', () => {
    expect(() => base64ToBytes('not base64 !!!')).toThrow();
  });
});

describe('text encoding', () => {
  it('encodes typed Unicode as UTF-8, which is what a remote PTY expects', () => {
    expect(Array.from(textToUtf8('é'))).toEqual([0xc3, 0xa9]);
    expect(Array.from(textToUtf8('中'))).toEqual([0xe4, 0xb8, 0xad]);
    expect(utf8ToText(textToUtf8('中文 abc\r'))).toBe('中文 abc\r');
  });

  it('latin1 differs from UTF-8 above U+007F', () => {
    expect(Array.from(latin1ToBytes('é'))).toEqual([0xe9]);
  });
});

describe('toSendBytes', () => {
  it('passes byte arrays through untouched', () => {
    const bytes = new Uint8Array([0, 1, 254, 255]);
    expect(toSendBytes(bytes)).toBe(bytes);
  });

  it('accepts an ArrayBuffer', () => {
    const buffer = new Uint8Array([5, 6, 7]).buffer;
    expect(Array.from(toSendBytes(buffer))).toEqual([5, 6, 7]);
  });

  it('sends ASCII identically in either encoding, so trzsz frames are safe', () => {
    const frame = '#ACT:dg==\n';
    expect(Array.from(toSendBytes(frame))).toEqual(Array.from(latin1ToBytes(frame)));
  });

  it('sends non-ASCII text as UTF-8, not as one byte per character', () => {
    expect(Array.from(toSendBytes('café'))).toEqual([0x63, 0x61, 0x66, 0xc3, 0xa9]);
  });
});

describe('formatting helpers', () => {
  it('formats byte counts', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(999)).toBe('999 B');
    expect(formatBytes(1024)).toBe('1.00 KB');
    expect(formatBytes(1536)).toBe('1.50 KB');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.00 MB');
  });

  it('turns the Go time.Time JSON into a Date or null', () => {
    expect(toDate('2026-10-21T10:00:00Z')).toBeInstanceOf(Date);
    expect(toDate(null)).toBeNull();
    expect(toDate('')).toBeNull();
    expect(toDate('not a date')).toBeNull();
    expect(toDate({ wallClock: 'nonsense' })).toBeNull();
  });

  it('shortens a fingerprint without losing its prefix', () => {
    expect(shortFingerprint('SHA256:abcdefghijklmnop')).toBe('SHA256:abcdefgh…');
    expect(shortFingerprint('abcdef')).toBe('abcdef');
  });
});
