/*
 * OSC 52 — what a program's own Copy item sends.
 *
 * This exists because the parser is the only thing standing between a TUI's
 * menu and the system clipboard: the sequence is a few bytes of base64 in the
 * output stream, and every wrong answer to "is this a copy?" either loses the
 * text or overwrites the clipboard with something the user never asked for.
 * The read query is the one case that must stay silent — answering it would
 * hand this machine's clipboard to the host.
 */
import { describe, expect, it } from 'vitest';

import { MAX_OSC52_CHARS, parseOsc52 } from './osc52';

function base64(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

describe('an OSC 52 payload', () => {
  it('is the text a program asked to copy', () => {
    expect(parseOsc52(`c;${base64('hello from the menu')}`)).toEqual({
      kind: 'copy',
      text: 'hello from the menu',
    });
  });

  it('carries UTF-8, not just ASCII', () => {
    // The menu item has to copy Chinese out of a TUI the same way; decoding the
    // base64 as latin1 would produce mojibake instead.
    expect(parseOsc52(`c;${base64('部署完成 ✓')}`)).toEqual({ kind: 'copy', text: '部署完成 ✓' });
  });

  it('takes the default selection when none is named', () => {
    expect(parseOsc52(`;${base64('x')}`)).toEqual({ kind: 'copy', text: 'x' });
  });

  it('takes any named selection — Windows has one clipboard', () => {
    for (const spec of ['p', 's0', 's7', 'c;p']) {
      expect(parseOsc52(`${spec};${base64('y')}`)).toEqual({ kind: 'copy', text: 'y' });
    }
  });

  it('survives a base64 string a log wrapped', () => {
    const payload = base64('wrapped');
    expect(parseOsc52(`c;${payload.slice(0, 4)}\n${payload.slice(4)}`)).toEqual({
      kind: 'copy',
      text: 'wrapped',
    });
  });

  it('never answers a read query', () => {
    // `c;?` means "tell me this machine's clipboard". The only safe reply is
    // none at all.
    expect(parseOsc52('c;?')).toEqual({ kind: 'ignore' });
  });

  it('ignores malformed base64 instead of throwing', () => {
    for (const payload of ['!!!!', '====', 'a===', 'QQ=Q', 'a']) {
      expect(() => parseOsc52(`c;${payload}`)).not.toThrow();
      expect(parseOsc52(`c;${payload}`)).toEqual({ kind: 'ignore' });
    }
  });

  it('ignores what is not a copy at all', () => {
    expect(parseOsc52('')).toEqual({ kind: 'ignore' });
    expect(parseOsc52('c')).toEqual({ kind: 'ignore' });
    // An empty payload decodes to nothing: pasting that would wipe the clipboard
    // for no reason.
    expect(parseOsc52('c;')).toEqual({ kind: 'ignore' });
  });

  it('refuses a payload large enough to be a select-all accident', () => {
    const big = 'A'.repeat(MAX_OSC52_CHARS + 4);
    expect(parseOsc52(`c;${big}`)).toEqual({ kind: 'ignore' });
  });
});
