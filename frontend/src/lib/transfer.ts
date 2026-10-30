/*
 * Telling "the user stopped this" from "this went wrong".
 *
 * Cancelling a trz/tsz transfer is a user action, but it arrives as an error
 * frame: trzsz-go reports a stop as `#fail:` with whatever its internal wording
 * happens to be, and Go, JS and the browser each word a cancelled thing
 * differently. Toasting all of that turns Ctrl+C into a stack trace the user
 * cannot act on — while a real failure (disk full, permission denied, truncated
 * file) still deserves to be said out loud.
 */

/** The ways a stop gets worded, across trzsz-go, trzsz-js and the pickers. */
const CANCELLED = [
  // trzsz-js's own stop message (its `isStopAndDelete()`).
  /stopped and deleted/i,
  // trzsz-go, and every picker that closes without a choice.
  /cancell?ed/i,
  /aborted/i,
  /user (?:closed|interrupted|stopped)/i,
  // The terminal interrupt that usually starts the whole thing.
  /\^C/,
  /ctrl-?c/i,
  /interrupt/i,
  // trzsz's upload path when the picker came back empty.
  /no files to upload/i,
  // A frame the client could not parse because the transfer was already gone.
  /does not start/i,
];

/** True when nothing went wrong: somebody stopped the transfer. */
export function isTransferCancel(message: string): boolean {
  if (!message) return true;
  return CANCELLED.some((pattern) => pattern.test(message));
}

/**
 * A crash inside the remote trzsz binary. The terminal already shows it — the
 * remote program prints it on the same PTY — so a toast would only repeat a
 * Go stack trace at someone who cannot do anything with it.
 */
export function isRemoteStackDump(message: string): boolean {
  return /panic:|goroutine \d+ \[|runtime error:/i.test(message);
}

/**
 * The library complaining about its own framing. When a transfer has just been
 * stopped, the frame that would have explained it often arrives truncated, and
 * trzsz hands over `decode [...] error: …` instead. That is not actionable, and
 * the remote binary printed the real line on the same PTY anyway.
 */
export function isLibraryNoise(message: string): boolean {
  return /decode \[.*\] error:/i.test(message);
}

/** Nothing but control characters: the cleanup a progress bar leaves behind. */
const CONTROL_ONLY = /^[\u0000-\u001f\u007f]*$/;

/**
 * What trzsz-js's exit handler wants to print, filtered: `null` means print
 * nothing.
 *
 * The library writes its exit through Node's `process.stdout`, so whoever
 * hosts it in a browser has to route those writes. Control-only writes are the
 * tidy-up — cursor restore, alt-screen exit, the trailing newline — and must
 * arrive even on a cancel, or the screen keeps the half-drawn progress bar.
 * The words that only narrate a cancellation, or a crash in the remote binary,
 * do not.
 */
export function filterStdoutWrite(text: string): string | null {
  if (!text) return null;
  if (CONTROL_ONLY.test(text)) return text;
  if (isTransferCancel(text) || isRemoteStackDump(text) || isLibraryNoise(text)) return null;
  return text;
}
