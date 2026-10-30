/*
 * The palette has to be readable, and element resets have to stay overridable.
 *
 * Two silent bugs, both found by measurement rather than by reading the code:
 *
 * 1. `button { color: inherit }` sat in style.css *outside* every `@layer`. A rule
 *    with no layer outranks every layered rule whatever its specificity, so it beat
 *    each `text-[…]` utility on every button. "New host" and "Save & connect" were
 *    painted with the *inherited* text colour — near-black on dark blue in the
 *    light theme — and the red on Delete, the dim on the unselected auth-type
 *    button and `font-mono` on inputs were swallowed the same way (`font:` is a
 *    shorthand, so it reset font-family too). Nothing threw; it just looked wrong,
 *    and in the dark theme the inherited colour happened to suit the bright accent,
 *    so it went unnoticed there.
 * 2. Nothing anywhere asked whether a token pair can actually be read.
 *
 * So both are checked as rules: element colour/font resets may only live inside
 * `@layer base`, and the token pairs the UI paints with must clear WCAG AA.
 */
import { readFileSync } from 'node:fs';
import path from 'node:path';

import { describe, expect, it } from 'vitest';

/**
 * The stylesheet as written, not as bundled: the point is where each rule *sits*
 * relative to `@layer`, and the bundle has already flattened that. Vitest runs with
 * the frontend folder as the working directory; the first test below fails loudly
 * if that ever stops being true.
 */
const css = readFileSync(path.resolve(process.cwd(), 'src/style.css'), 'utf8')
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/@import[^;]*;/g, '');

interface Rule {
  selector: string;
  body: string;
}

function readBlock(source: string, open: number): { body: string; end: number } {
  let depth = 1;
  let i = open;
  while (i < source.length && depth > 0) {
    if (source[i] === '{') depth += 1;
    else if (source[i] === '}') depth -= 1;
    i += 1;
  }
  return { body: source.slice(open, Math.max(open, i - 1)), end: i };
}

/** Every rule at this level, with the layer status it inherits; layered ones drop out. */
function collect(source: string, layered: boolean, into: Rule[]): void {
  let i = 0;
  for (;;) {
    const open = source.indexOf('{', i);
    if (open < 0) return;
    const selector = source.slice(i, open).trim();
    const { body, end } = readBlock(source, open + 1);
    const insideLayer = layered || /^@layer\b/.test(selector);
    if (!insideLayer) {
      if (selector.startsWith('@')) collect(body, false, into); // @media keeps the layer status
      else into.push({ selector, body });
    }
    i = end;
  }
}

/** An element selector that carries text, plus a property that paints it. */
const TEXT_BEARING = /\b(button|input|select|textarea|a|p|h[1-6]|li|label)\b/;
const PAINTS_TEXT = /(^|[;{\s])(color|font|font-family|font-size|background|background-color)(-color)?\s*:/;

function unlayeredElementResets(): string[] {
  const rules: Rule[] = [];
  collect(css, false, rules);
  return rules
    .filter((rule) => !rule.selector.startsWith('.') && TEXT_BEARING.test(rule.selector) && PAINTS_TEXT.test(rule.body))
    .map((rule) => rule.selector.replace(/\s+/g, ' '));
}

function tokens(theme: 'dark' | 'light'): Record<string, string> {
  const block = new RegExp(`\\[data-theme='${theme}'\\][^{]*\\{([^}]*)\\}`).exec(css);
  if (!block || !block[1]) throw new Error(`style.css has no [data-theme='${theme}'] block`);
  const out: Record<string, string> = {};
  for (const pair of block[1].matchAll(/--([a-z0-9-]+)\s*:\s*(#[0-9a-fA-F]{3,8})/g)) {
    out[pair[1] as string] = pair[2] as string;
  }
  return out;
}

function channel(byte: number): number {
  const v = byte / 255;
  return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
}

function luminance(hex: string): number {
  const digits = hex.replace('#', '');
  const short = digits.length === 3;
  const part = (index: number): number => {
    const raw = short ? digits[index]! + digits[index]! : digits.slice(index * 2, index * 2 + 2);
    return channel(Number.parseInt(raw, 16));
  };
  return 0.2126 * part(0) + 0.7152 * part(1) + 0.0722 * part(2);
}

/** WCAG 2 contrast ratio, 1 (identical) to 21 (black on white). */
function contrast(a: string, b: string): number {
  const one = luminance(a);
  const two = luminance(b);
  return (Math.max(one, two) + 0.05) / (Math.min(one, two) + 0.05);
}

const THEMES = ['dark', 'light'] as const;

describe('element resets stay overridable', () => {
  it('finds the stylesheet it is supposed to check', () => {
    // A glob that silently matched nothing would turn this whole file into a no-op.
    expect(css).toContain('--accent');
    expect(Object.keys(tokens('dark')).length).toBeGreaterThan(10);
  });

  it('keeps no unlayered colour or font reset on an element', () => {
    // Anything here outranks every `text-[…]` / `font-mono` utility in the app.
    expect(unlayeredElementResets()).toEqual([]);
  });
});

describe('the palette can be read', () => {
  it('gives both themes the same tokens', () => {
    expect(Object.keys(tokens('light')).sort()).toEqual(Object.keys(tokens('dark')).sort());
  });

  it('paints text on the accent that a human can read', () => {
    // The pair behind "New host" / "Save & connect": accent fill, accent ink.
    for (const theme of THEMES) {
      const t = tokens(theme);
      expect(contrast(t.accent as string, t['accent-ink'] as string), `${theme}: ink on accent`).toBeGreaterThanOrEqual(
        4.5,
      );
    }
  });

  it('keeps body and secondary text legible on every surface it sits on', () => {
    for (const theme of THEMES) {
      const t = tokens(theme);
      for (const surface of ['bg', 'panel', 'panel-2'] as const) {
        expect(contrast(t.text as string, t[surface] as string), `${theme}: text on ${surface}`).toBeGreaterThanOrEqual(
          4.5,
        );
        expect(
          contrast(t['text-dim'] as string, t[surface] as string),
          `${theme}: secondary text on ${surface}`,
        ).toBeGreaterThanOrEqual(4.5);
      }
    }
  });

  it('keeps the accent legible where it is used as a label', () => {
    // Section headings, links and menu items are accent-coloured text, which only
    // needs the 3:1 bar for non-body text — but it clears 4.5 in both themes.
    for (const theme of THEMES) {
      const t = tokens(theme);
      expect(contrast(t.accent as string, t.panel as string), `${theme}: accent on panel`).toBeGreaterThanOrEqual(3);
    }
  });

  it('keeps a placeholder clearly weaker than typed text', () => {
    // The reason the token exists: the New host form used to look pre-filled.
    for (const theme of THEMES) {
      const t = tokens(theme);
      expect(
        contrast(t['text'] as string, t.panel as string) - contrast(t.placeholder as string, t.panel as string),
        `${theme}: placeholder should sit well below the typed text`,
      ).toBeGreaterThan(2);
    }
  });
});
