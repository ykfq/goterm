/*
 * Guards the vendoring, and pins what it draws.
 *
 * `progress.ts` here is trzsz's own file, copied because the package does not
 * export it. Two things are worth failing over: that it is still *their* file
 * (licence header intact, the one import still the shape the shim provides), and
 * that it draws the line an `rz`/`sz` transfer shows — because that line being
 * identical to a `trz`/`tsz` transfer's is the entire reason for copying instead
 * of writing our own.
 */
import { readFileSync } from 'node:fs';
import path from 'node:path';

import { describe, expect, it, vi } from 'vitest';

import { TextProgressBar } from './progress';

const SOURCE = readFileSync(path.resolve(process.cwd(), 'src/vendor/trzsz/progress.ts'), 'utf8');

describe('the vendored bar is still trzsz’s', () => {
  it('keeps the upstream header and its single import', () => {
    // The one thing added on top of upstream: this tsconfig is stricter than
    // theirs, and their file is not ours to fix.
    expect(SOURCE.startsWith('// @ts-nocheck')).toBe(true);
    expect(SOURCE).toContain('trzsz: https://github.com/trzsz/trzsz.js');
    expect(SOURCE).toContain('Copyright(c) 2022-2026 Lonny Wong');
    expect(SOURCE).toContain('@license MIT');
    // The only edit to the file would be visible here: it must still import the
    // type from `./comm`, which is what the shim beside it provides.
    expect(SOURCE).toContain('import { ProgressCallback } from "./comm";');
    // No local patching has crept in: our own markers never appear in it.
    expect(SOURCE).not.toContain('GoTerm');
    expect(SOURCE).not.toContain('C:\\Users');
  });

  it('draws the line, in place, at the size it is told', () => {
    vi.useFakeTimers();
    try {
      const out: string[] = [];
      const bar = new TextProgressBar((text) => out.push(text), 80);

      bar.hideCursor();
      expect(out.at(-1)).toBe('\x1b[?25l');

      bar.onNum(1);
      bar.onName('backup.tar');
      bar.onSize(1000);
      bar.onStep(0);
      const first = out.at(-1) ?? '';
      expect(first).toContain('backup.tar');
      expect(first).toContain('0%');
      expect(first).toContain('\x1b[36m');
      expect(first).toContain('░');

      vi.advanceTimersByTime(250);
      bar.onStep(500);
      const half = out.at(-1) ?? '';
      expect(half.startsWith('\r')).toBe(true); // repainted in place
      expect(half).toContain('50%');
      expect(half).toContain('█');

      bar.onDone();
      expect(out.at(-1)).toContain('100%');

      bar.showCursor();
      expect(out.at(-1)).toBe('\x1b[?25h');
    } finally {
      vi.useRealTimers();
    }
  });

  it('numbers the file when the transfer carries several', () => {
    vi.useFakeTimers();
    try {
      const out: string[] = [];
      const bar = new TextProgressBar((text) => out.push(text), 100);
      bar.onNum(3);
      bar.onName('a.bin');
      bar.onSize(100);
      bar.onStep(10);
      vi.advanceTimersByTime(250);
      bar.onName('b.bin');
      bar.onSize(100);
      bar.onStep(10);
      expect(out.at(-1)).toContain('(2/3) b.bin');
    } finally {
      vi.useRealTimers();
    }
  });

  it('fits itself to a narrow terminal instead of wrapping', () => {
    vi.useFakeTimers();
    try {
      const out: string[] = [];
      const bar = new TextProgressBar((text) => out.push(text), 40);
      bar.onNum(1);
      bar.onName('a-really-long-file-name.tar.gz');
      bar.onSize(1000);
      bar.onStep(500);
      // Escape sequences take no cells, so measure the rest.
      const visible = (out.at(-1) ?? '').replace(/\x1b\[[0-9;]*m/g, '').replace(/^\r/, '');
      expect(visible.length).toBeLessThanOrEqual(40);
      // The name is what gives way first — shortened, or dropped when even that is
      // too wide. What must survive is the percentage.
      expect(visible).not.toContain('a-really-long-file-name.tar.gz');
      expect(visible).toContain('50%');
    } finally {
      vi.useRealTimers();
    }
  });
});
