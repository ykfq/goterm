/*
 * Vitest runs in Node, where the CJS build of trzsz consoles "require" and
 * therefore believes it is in a node runtime — which sends it down the
 * fs-and-paths branch instead of the browser picker branch this app uses.
 * Importing the ESM build directly reproduces production behaviour.
 */
declare module 'trzsz/lib/trzsz.mjs' {
  export { TrzszFilter, TrzszAddon } from 'trzsz';
  export type { TrzszOptions } from 'trzsz';
}
