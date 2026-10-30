/*
 * Importing MobaXterm syntax files (`.msyn`).
 *
 * The format is an INI section with one regular expression per colour:
 *
 *   [CustomSyntax]
 *   Name=Custom: Standard keywords + Shell syntax
 *   UseRegex=1
 *   Underline=...
 *   Red=...
 *   Green=...
 *   ...
 *   CaseSensitive=0
 *
 * Every value is a big alternation. Two things make this importable at all:
 *
 *  - **Branches are compiled one at a time.** A .NET regex and a JavaScript regex
 *    are close enough for this style of pattern, but not identical, and the files
 *    in the wild are often damaged (this one is Windows-1252 with a few broken
 *    character classes). A branch that will not compile is reported and dropped
 *    instead of failing the whole import.
 *  - **The colour names are mapped, not guessed.** Red/Green/Yellow/Blue/Magenta/
 *    Cyan map to the same SGR codes GoTerm's built-in sets use; Underline is an
 *    attribute.
 *
 * MobaXterm wraps many branches in invisible guard classes (`[^A-Za-z_-](…)[^…]`)
 * to fake word boundaries. They are kept as written: the guard characters are
 * punctuation, so colouring them changes nothing a reader can see.
 */
import { COLOUR_SGR, type CustomRule, type HighlightColour } from './highlight';

const COLOUR_KEYS: Record<string, HighlightColour> = {
  Red: 'red',
  Green: 'green',
  Yellow: 'yellow',
  Blue: 'blue',
  Magenta: 'magenta',
  Cyan: 'cyan',
  Underline: 'underline',
};

export interface MsynSkip {
  source: string;
  reason: string;
}

export interface MsynImport {
  rules: CustomRule[];
  skipped: MsynSkip[];
  meta: { name: string; useRegex: boolean; caseSensitive: boolean };
}

/**
 * Decode the file: strict UTF-8 when it is valid, otherwise Windows-1252.
 *
 * The reference file is not UTF-8 (it has single bytes in the 0x80-0x9f range),
 * and a regex is ASCII, so a byte-preserving single-byte decode is exactly right
 * — no replacement characters, no broken character classes.
 */
export function decodeMsyn(bytes: Uint8Array): string {
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    return new TextDecoder('windows-1252').decode(bytes);
  }
}

/** Split an alternation at top level, respecting groups, classes and escapes. */
export function splitTopLevel(value: string): string[] {
  const out: string[] = [];
  let current = '';
  let depth = 0;
  let inClass = false;
  for (let i = 0; i < value.length; i += 1) {
    const ch = value[i] as string;
    if (ch === '\\') {
      current += ch;
      const next = value[i + 1];
      if (next !== undefined) {
        current += next;
        i += 1;
      }
      continue;
    }
    if (inClass) {
      if (ch === ']') inClass = false;
      current += ch;
      continue;
    }
    if (ch === '[') {
      inClass = true;
      current += ch;
      continue;
    }
    if (ch === '(') {
      depth += 1;
      current += ch;
      continue;
    }
    if (ch === ')') {
      depth = Math.max(0, depth - 1);
      current += ch;
      continue;
    }
    if (ch === '|' && depth === 0) {
      out.push(current);
      current = '';
      continue;
    }
    current += ch;
  }
  out.push(current);
  return out.map((part) => part.trim()).filter(Boolean);
}

/** Parse one `.msyn` file into rules GoTerm can run. */
export function parseMsyn(text: string): MsynImport {
  const values = new Map<string, string>();
  let section = '';
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith(';') || line.startsWith('#')) continue;
    if (line.startsWith('[') && line.endsWith(']')) {
      section = line.slice(1, -1).trim();
      continue;
    }
    if (section && section.toLowerCase() !== 'customsyntax') continue;
    const eq = line.indexOf('=');
    if (eq <= 0) continue;
    values.set(line.slice(0, eq).trim(), line.slice(eq + 1).trim());
  }

  const useRegex = (values.get('UseRegex') ?? '1').trim() !== '0';
  const caseSensitive = (values.get('CaseSensitive') ?? '0').trim() === '1';
  const name = values.get('Name') ?? 'MobaXterm syntax';

  const rules: CustomRule[] = [];
  const skipped: MsynSkip[] = [];
  const seen = new Set<string>();
  let seq = 0;

  for (const [key, colour] of Object.entries(COLOUR_KEYS)) {
    const value = values.get(key);
    if (!value) continue;

    for (const branch of splitTopLevel(value)) {
      // A branch may still be a group of keywords separated by spaces when the
      // file is in keyword mode; regex mode treats it as one pattern.
      const patterns = useRegex ? [branch] : branch.split(/[,\s]+/).filter(Boolean);
      for (const pattern of patterns) {
        if (useRegex) {
          try {
            new RegExp(pattern, caseSensitive ? 'g' : 'gi');
          } catch (error) {
            skipped.push({ source: pattern, reason: error instanceof Error ? error.message : String(error) });
            continue;
          }
        }
        const dedupe = `${colour}\u0000${pattern}`;
        if (seen.has(dedupe)) continue;
        seen.add(dedupe);
        rules.push({
          id: `msyn-${seq++}`,
          pattern,
          colour,
          regex: useRegex,
          ignoreCase: !caseSensitive,
          enabled: true,
        });
      }
    }
  }

  return { rules, skipped, meta: { name, useRegex, caseSensitive } };
}

/** True when this text looks like a `.msyn` rather than something else. */
export function looksLikeMsyn(text: string): boolean {
  return /\[CustomSyntax\]/i.test(text) && /(?:^|\n)\s*(Red|Green|Yellow|Blue|Magenta|Cyan)=/m.test(text);
}

/** The SGR a colour renders as, for the settings swatch. */
export function colourSgr(colour: HighlightColour): string {
  return COLOUR_SGR[colour] ?? COLOUR_SGR.yellow;
}
