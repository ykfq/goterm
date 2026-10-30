/*
 * A custom terminal background belongs to the canvas, and to the canvas only.
 *
 * The bug this pins: the active tab was painted with `--term-bg` so it would merge
 * with the pane. With a custom (dark) background chosen — and that choice survives
 * theme switches on purpose — the active tab stayed dark under the light theme
 * while every other tab and the rest of the shell went light. That is a *rule*
 * broken, not a colour picked wrong, so it is checked as a rule: the two files that
 * legitimately paint canvas colour are named here, and every other piece of chrome
 * is not allowed to mention a `--term-` token at all.
 */
import { describe, expect, it } from 'vitest';

/** The only files allowed to use the terminal's own palette. */
const CANVAS_OWNERS = new Set(['TerminalPane.vue', 'SettingsDialog.vue']);

const sources = import.meta.glob('./*.vue', { query: '?raw', import: 'default', eager: true }) as Record<
  string,
  string
>;

function nameOf(path: string): string {
  return path.slice(path.lastIndexOf('/') + 1);
}

describe('terminal colours stay on the canvas', () => {
  it('finds the components it is supposed to check', () => {
    // A glob that silently matches nothing would make this whole file a no-op.
    const names = Object.keys(sources).map(nameOf);
    expect(names).toContain('TabBar.vue');
    expect(names).toContain('TerminalPane.vue');
  });

  it('lets no chrome component take a --term- token', () => {
    const offenders = Object.entries(sources)
      .filter(([path]) => !CANVAS_OWNERS.has(nameOf(path)))
      .filter(([, source]) => source.includes('--term-'))
      .map(([path]) => nameOf(path));

    expect(offenders).toEqual([]);
  });
});
