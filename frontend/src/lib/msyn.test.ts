/*
 * MobaXterm `.msyn` import.
 *
 * The reference file is Windows-1252, hand-maintained, and contains at least one
 * branch that is not a valid JavaScript expression. The import has to be
 * forgiving in exactly one direction: keep everything it can, report what it
 * cannot, never fail the whole file because of one broken alternation.
 */
import { describe, expect, it } from 'vitest';

import { decodeMsyn, looksLikeMsyn, parseMsyn, splitTopLevel } from './msyn';

const FIXTURE = [
  '[CustomSyntax]',
  'Name=Custom: Standard keywords + Shell syntax',
  'UseRegex=1',
  'CaseSensitive=0',
  'Underline=[^A-Za-z_](https?://[A-Za-z0-9./-]+)[^A-Za-z_]',
  'Red=[^A-Za-z_&-](rejected|refused|failed)[^A-Za-z_-]|error',
  'Green=([^A-Za-z_-](session opened|accepted)[^A-Za-z_-])',
  'Cyan=([^A-Za-z_-](notice)[^A-Za-z_-])|(unclosed',
  'Blinking=',
].join('\r\n');

describe('splitTopLevel', () => {
  it('splits only between whole alternatives', () => {
    expect(splitTopLevel('a|b')).toEqual(['a', 'b']);
    expect(splitTopLevel('(a|b)|c')).toEqual(['(a|b)', 'c']);
    expect(splitTopLevel('[a|b]|c')).toEqual(['[a|b]', 'c']);
    expect(splitTopLevel('\\|a|b')).toEqual(['\\|a', 'b']);
  });
});

describe('decodeMsyn', () => {
  it('reads UTF-8 as UTF-8', () => {
    const text = decodeMsyn(new TextEncoder().encode('Name=Ünïcøde'));
    expect(text).toBe('Name=Ünïcøde');
  });

  it('falls back to Windows-1252 instead of producing replacement characters', () => {
    // 0x9A is the byte in the real reference file (a Greek word, mangled by
    // whoever saved it). Windows-1252 keeps it as a single character; the regex
    // syntax around it is ASCII and survives untouched.
    const bytes = new Uint8Array([0x52, 0x65, 0x64, 0x3d, 0x67, 0x72, 0x65, 0x9a, 0x6b, 0x61]);
    const text = decodeMsyn(bytes);
    // 0x9A is Windows-1252 for “š”; strict UTF-8 would have failed outright.
    expect(text).toBe('Red=gre\u0161ka');
    expect(text).not.toContain('\uFFFD');
  });
});

describe('parseMsyn', () => {
  const parsed = parseMsyn(FIXTURE);

  it('reads the header', () => {
    expect(parsed.meta).toEqual({
      name: 'Custom: Standard keywords + Shell syntax',
      useRegex: true,
      caseSensitive: false,
    });
  });

  it('maps every colour group to a GoTerm colour', () => {
    const colours = new Set(parsed.rules.map((rule) => rule.colour));
    expect(colours).toEqual(new Set(['red', 'green', 'cyan', 'underline']));
    expect(parsed.rules.some((rule) => rule.colour === 'red')).toBe(true);
  });

  it('keeps the alternation that is valid and reports the one that is not', () => {
    const cyan = parsed.rules.filter((rule) => rule.colour === 'cyan');
    expect(cyan.map((rule) => rule.pattern)).toEqual(['([^A-Za-z_-](notice)[^A-Za-z_-])']);
    expect(parsed.skipped.map((skip) => skip.source)).toContain('(unclosed');
    expect(parsed.skipped[0]?.reason).toMatch(/Invalid regular expression|Unterminated/i);
  });

  it('does not split inside a group, and drops a group that never closes', () => {
    // A `|` at depth > 0 belongs to its group: this whole value is one branch.
    expect(splitTopLevel('([^a](x|y)[^b]|(unclosed')).toEqual(['([^a](x|y)[^b]|(unclosed']);
    const broken = parseMsyn('[CustomSyntax]\nUseRegex=1\nCyan=([^a](x|y)[^b]|(unclosed');
    expect(broken.rules).toHaveLength(0);
    expect(broken.skipped).toHaveLength(1);
  });

  it('marks everything case-insensitive because CaseSensitive=0', () => {
    expect(parsed.rules.every((rule) => rule.ignoreCase && rule.regex && rule.enabled)).toBe(true);
  });

  it('gives every rule a unique id', () => {
    const ids = new Set(parsed.rules.map((rule) => rule.id));
    expect(ids.size).toBe(parsed.rules.length);
  });

  it('compiles to rules the highlighter can run', () => {
    for (const rule of parsed.rules) {
      expect(() => new RegExp(rule.pattern, rule.ignoreCase ? 'gi' : 'g')).not.toThrow();
    }
  });
});

describe('looksLikeMsyn', () => {
  it('accepts a real file and rejects other text', () => {
    expect(looksLikeMsyn(FIXTURE)).toBe(true);
    expect(looksLikeMsyn('{"hosts":[]}')).toBe(false);
    expect(looksLikeMsyn('[CustomSyntax]\nName=empty')).toBe(false);
  });
});
