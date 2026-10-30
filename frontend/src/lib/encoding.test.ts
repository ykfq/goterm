/*
 * Reading a host that does not speak UTF-8.
 *
 * The byte pipe is faithful, so mojibake is a decoding problem, not a transport
 * one: GBK's 中文 is `D6 D0 CE C4`, which xterm reads as four replacement-able
 * bytes. The two things that must not break: the UTF-8 path has to stay the very
 * same buffer (byte-for-byte output is the whole point of this app), and a
 * character cut across two output chunks has to come out whole.
 */
import { describe, expect, it } from 'vitest';

import { TERMINAL_ENCODINGS, RemoteTextDecoder, isTerminalEncoding } from './encoding';

const bytes = (...values: number[]) => Uint8Array.from(values);

/** 中文 in GBK/GB18030. */
const ZHONG_WEN = bytes(0xd6, 0xd0, 0xce, 0xc4);

describe('isTerminalEncoding', () => {
  it('accepts exactly what the dropdown offers', () => {
    for (const choice of TERMINAL_ENCODINGS) expect(isTerminalEncoding(choice.id)).toBe(true);
  });

  it('rejects anything else, including a saved value from an older build', () => {
    expect(isTerminalEncoding('utf-16')).toBe(false);
    expect(isTerminalEncoding('')).toBe(false);
    expect(isTerminalEncoding(null)).toBe(false);
    expect(isTerminalEncoding(42)).toBe(false);
  });
});

describe('RemoteTextDecoder', () => {
  it('hands UTF-8 output straight through, as the same array', () => {
    // Not "equal to" — identical. A copy would be a different object graph on the
    // hot path, and the byte-fidelity tests elsewhere compare bytes, not identity,
    // so only this can catch a decoder sneaking in.
    const decoder = new RemoteTextDecoder();
    const chunk = bytes(0xe4, 0xb8, 0xad, 0x00, 0xff);
    expect(decoder.decode(chunk)).toBe(chunk);
  });

  it('decodes GBK to the characters the server meant', () => {
    const decoder = new RemoteTextDecoder('gb18030');
    expect(decoder.decode(ZHONG_WEN)).toBe('中文');
  });

  it('keeps half a character between chunks', () => {
    const decoder = new RemoteTextDecoder('gb18030');
    // A GBK character is two bytes and the pipe cuts wherever the socket does.
    expect(decoder.decode(bytes(0xd6))).toBe('');
    expect(decoder.decode(bytes(0xd0, 0xce, 0xc4))).toBe('中文');
  });

  it('drops the half-character it was holding when the encoding changes', () => {
    // The lead byte belonged to a stream that has ended; finishing it into the new
    // encoding would print a character nobody sent.
    const decoder = new RemoteTextDecoder('gb18030');
    decoder.decode(bytes(0xd6));
    decoder.setEncoding('utf-8');
    const chunk = bytes(0x41);
    expect(decoder.decode(chunk)).toBe(chunk);
    expect(decoder.encoding).toBe('utf-8');
  });

  it('falls back to UTF-8 for a value it does not know', () => {
    const decoder = new RemoteTextDecoder('gb18030');
    // A hand-edited settings file is the usual source of this.
    decoder.setEncoding('latin-1' as never);
    expect(decoder.encoding).toBe('utf-8');
    const chunk = bytes(0xff);
    expect(decoder.decode(chunk)).toBe(chunk);
  });

  it('reads Big5 and Shift_JIS too', () => {
    // 中 in Big5 is `A4 A4`; あ in Shift_JIS is `82 A0`.
    expect(new RemoteTextDecoder('big5').decode(bytes(0xa4, 0xa4))).toBe('中');
    expect(new RemoteTextDecoder('shift_jis').decode(bytes(0x82, 0xa0))).toBe('あ');
  });
});
