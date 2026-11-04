/*
 * Test setup: the Wails runtime expects a desktop host.
 *
 * Under vitest there is nothing on the other end, and the runtime's browser
 * fallback would start dialling a dev server (and print a warning about it).
 * Replacing the module keeps the suite hermetic; tests that need to observe Go
 * calls mock `lib/backend` directly, which is the layer this project owns.
 */
import { vi } from 'vitest';

/*
 * xterm's grapheme addon cannot be loaded under Node as shipped.
 *
 * `addon-unicode-graphemes` 0.4.0 decompresses its width table with
 * `new DataView(data.buffer)` over the result of `Buffer.from(raw, 'base64')`.
 * Node hands back a slice of its 8 KiB Buffer pool, so `data.byteOffset` is not
 * 0; the header is read from the wrong bytes, the "uncompressed length" comes
 * out as ~1.7 GB (measured: 1774173167 against a real 51056), and the module
 * throws `RangeError: Array buffer allocation failed` before a single test runs.
 * A webview has no `Buffer`, so the addon takes its `atob` path and is correct
 * there — this is a test-environment trap, not an app defect.
 *
 * What the addon contributes is Unicode width tables, which no headless test can
 * measure anyway (character width comes from a renderer happy-dom does not
 * have). Only its presence is exercised, so it is stubbed here instead of
 * pinning every bridge test to the pool state of the process that loads it.
 */
vi.mock('@xterm/addon-unicode-graphemes', () => ({
  UnicodeGraphemesAddon: class {
    activate(): void {}
    // xterm's AddonManager keeps the addon's own `dispose` and calls it back
    // when the terminal goes away, so a stub with no method there fails teardown
    // even though every test passed.
    dispose(): void {}
  },
}));

vi.mock('@wailsio/runtime', () => {
  const noBackend = async () => {
    throw new Error('no Go backend in tests');
  };
  return {
    Events: {
      On: () => () => undefined,
      Once: () => () => undefined,
      Emit: async () => false,
      Off: () => undefined,
      Types: { Common: { WindowRuntimeReady: 'common:WindowRuntimeReady' } },
    },
    Clipboard: { Text: async () => '', SetText: async () => undefined },
    Call: { ByID: noBackend, ByName: noBackend },
    // The generated models build their return values through these, so they
    // have to behave, not merely exist: `Create.Nullable(...)` is called while
    // the module is being imported, and a bare `{}` fails the whole file.
    Create: {
      Any: (value: unknown) => value,
      Nullable:
        (create: (value: never) => unknown) =>
        (value: unknown) =>
          value === null || value === undefined ? null : create(value as never),
      Array:
        (create: (value: never) => unknown) =>
        (value: unknown) =>
          Array.isArray(value) ? value.map((entry) => create(entry as never)) : value,
      Map:
        (create: (value: never) => unknown) =>
        (value: Record<string, unknown>) => {
          const out: Record<string, unknown> = {};
          for (const [key, entry] of Object.entries(value ?? {})) out[key] = create(entry as never);
          return out;
        },
      Events: () => ({}),
    },
  };
});
