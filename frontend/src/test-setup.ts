/*
 * Test setup: the Wails runtime expects a desktop host.
 *
 * Under vitest there is nothing on the other end, and the runtime's browser
 * fallback would start dialling a dev server (and print a warning about it).
 * Replacing the module keeps the suite hermetic; tests that need to observe Go
 * calls mock `lib/backend` directly, which is the layer this project owns.
 */
import { vi } from 'vitest';

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
