/*
 * Paste hygiene.
 *
 * A clipboard block that submits more than one line executes commands the user
 * has not looked at, so it gets shown to them first. Everything else — one line,
 * with or without its trailing newline — goes straight through, because that is
 * what "copy a command, paste it, it runs" means at a terminal.
 */

/** Leading blank lines: what a web page or a `cat` selection likes to include. */
export function trimLeadingNewlines(text: string): string {
  return text.replace(/^(?:[ \t]*(?:\r\n|\r|\n))+/, '');
}

/**
 * Trailing blank lines, for the review dialog only.
 *
 * Not applied to a single-line paste: `"ls\n"` reaching the terminal is supposed
 * to run `ls`, so its newline is content, not padding.
 */
export function trimTrailingBlankLines(text: string): string {
  return text.replace(/(?:(?:\r\n|\r|\n)[ \t]*)+$/, '');
}

export function lineCount(text: string): number {
  if (!text) return 0;
  return text.split(/\r\n|\r|\n/).length;
}

/**
 * True when pasting this would submit more than one line to the shell.
 *
 * A single trailing newline is not a second line — it is the Enter that runs the
 * one line before it.
 */
export function isMultilinePaste(text: string): boolean {
  const normalised = text.replace(/\r\n|\r/g, '\n');
  return normalised.endsWith('\n')
    ? normalised.slice(0, -1).includes('\n')
    : normalised.includes('\n');
}
