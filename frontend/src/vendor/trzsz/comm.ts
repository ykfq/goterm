/*
 * Third-party code, vendored: trzsz's transfer progress bar.
 *
 * `progress.ts` next to this file is **byte-for-byte** the source that `trzsz`
 * (https://github.com/trzsz/trzsz.js, MIT, © Lonny Wong, version 1.1.6) publishes
 * in `lib/trzsz.mjs.map`. It is copied rather than imported because the package
 * exports only `TrzszAddon` and `TrzszFilter`: `TextProgressBar` is inside the
 * bundle but unreachable, so there is no import that could get at it.
 *
 * Copying it means an `rz`/`sz` transfer draws *exactly* the bar a `trz`/`tsz`
 * transfer draws — same characters, same fields, same fitting rules — instead of a
 * look-alike that drifts the moment either side changes. The cost is that upstream
 * fixes have to be copied here by hand; the file has not changed since 1.1.6.
 *
 * This shim exists only so that `progress.ts` compiles unchanged: the single thing
 * it imports is this interface (`import { ProgressCallback } from "./comm"`), and
 * restating it here keeps the vendored file verbatim instead of editing its import.
 *
 * Because that one import is not `import type`, Rollup warns on every build that
 * `comm.ts` does not export `ProgressCallback`. It is telling the truth about
 * runtime (an interface has no value behind it) and the whole statement is dropped
 * from the bundle — grep the built `dist/assets/index-*.js` for the name and it is
 * not there. Rewriting the import as `import type` would silence it, at the cost of
 * editing the file this directory exists to keep untouched.
 */
export interface ProgressCallback {
  onNum: (num: number) => void;
  onName: (name: string) => void;
  onSize: (size: number) => void;
  onStep: (step: number) => void;
  onDone: () => void;
}
