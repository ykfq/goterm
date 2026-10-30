import { describe, expect, it } from 'vitest';

import { isMultilinePaste, lineCount, trimLeadingNewlines, trimTrailingBlankLines } from './paste';

describe('trimLeadingNewlines', () => {
  it('removes the blank lines a copied block starts with', () => {
    expect(trimLeadingNewlines('\n\nls -l\n')).toBe('ls -l\n');
    expect(trimLeadingNewlines('\r\n\r\nls -l\r\n')).toBe('ls -l\r\n');
    expect(trimLeadingNewlines('   \n\t\nls')).toBe('ls');
  });

  it('leaves text that simply starts with content alone', () => {
    expect(trimLeadingNewlines('ls\n\n')).toBe('ls\n\n');
    expect(trimLeadingNewlines('')).toBe('');
    // Indentation on the first real line is content, not padding.
    expect(trimLeadingNewlines('  ls')).toBe('  ls');
  });
});

describe('trimTrailingBlankLines', () => {
  it('drops trailing blank lines but keeps the last real one', () => {
    expect(trimTrailingBlankLines('a\nb\n\n\n')).toBe('a\nb');
    expect(trimTrailingBlankLines('a\r\nb\r\n\r\n')).toBe('a\r\nb');
  });

  it('strips the trailing newline of a one-line block too', () => {
    // Only ever applied to the review dialog, where a trailing Enter would run
    // the last line before the user has pressed Paste.
    expect(trimTrailingBlankLines('ls\n')).toBe('ls');
    expect(trimTrailingBlankLines('ls')).toBe('ls');
  });
});

describe('isMultilinePaste', () => {
  it('treats one line plus its newline as a single line', () => {
    expect(isMultilinePaste('ls\n')).toBe(false);
    expect(isMultilinePaste('ls\r\n')).toBe(false);
    expect(isMultilinePaste('ls')).toBe(false);
  });

  it('catches real multi-line blocks in every line ending', () => {
    expect(isMultilinePaste('ls\npwd\n')).toBe(true);
    expect(isMultilinePaste('ls\r\npwd')).toBe(true);
    expect(isMultilinePaste('a\nb\nc')).toBe(true);
  });

  it('does not mistake a blank line inside the text for the end', () => {
    expect(isMultilinePaste('a\n\nb\n')).toBe(true);
  });
});

describe('lineCount', () => {
  it('counts the lines a paste would submit', () => {
    expect(lineCount('')).toBe(0);
    expect(lineCount('a')).toBe(1);
    expect(lineCount('a\nb')).toBe(2);
    expect(lineCount('a\r\nb\r\nc')).toBe(3);
  });
});
