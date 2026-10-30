/*
 * The only place the frontend talks to Go.
 *
 * Wails v3 generates typed bindings into `frontend/bindings` (run
 * `wails3 generate bindings -ts`); everything the UI can call comes from there.
 * Bound methods reject with a plain string, so every call is funnelled through
 * `call()` to give callers one error shape.
 */
import * as App from '../../bindings/github.com/ykfq/goterm/app';
import * as Files from '../../bindings/github.com/ykfq/goterm/filebridge';
import type { FileBridgeApi, FileEntry } from './fsaccess';
import type { AppInfo, Host, HostExport, HostImport, HostKeyRecord, SessionInfo, SSHConfig, StorageInfo } from './protocol';

export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === 'string') return error;
  if (error && typeof error === 'object' && 'message' in error) {
    const message = (error as { message?: unknown }).message;
    if (typeof message === 'string') return message;
  }
  return String(error);
}

async function call<T>(what: string, run: () => Promise<T>): Promise<T> {
  try {
    return await run();
  } catch (error) {
    const message = errorMessage(error);
    throw new Error(message.replace(/^\[[^\]]+\]\s*/, '') || `${what} failed`);
  }
}

/** Go-side struct shapes; the UI keeps its own narrower types in protocol.ts. */
type GoConfig = Parameters<typeof App.Connect>[0];
type GoHost = Parameters<typeof App.SaveHost>[0];
/**
 * The fields of Go's `ConnectAnswers`, stated structurally on purpose. Importing
 * the generated model class would put the whole bindings graph behind every
 * module that loads `backend.ts` — and the settings tests re-import the store a
 * dozen times each with `vi.resetModules()`, where that weight is not free.
 */
type GoAnswers = Parameters<typeof App.ConnectHost>[3];

export const backend = {
  connect: (config: SSHConfig) => call('connect', () => App.Connect(config as unknown as GoConfig)),
  /**
   * Connects to a saved host. `answers` is what the prompts collected for this
   * one attempt: an empty field means "use whatever is stored", and nothing here
   * is written to disk — "remember" is a separate SetHostSecret call. The
   * two-factor code is appended to the password inside Go.
   *
   * It travels as an object, not as five positional strings, because three of
   * those strings are answers for different machines and swapping two of them
   * produces a login failure that looks like a wrong password.
   */
  connectHost: (hostId: string, cols: number, rows: number, answers: Partial<GoAnswers>) =>
    call('connectHost', () =>
      App.ConnectHost(hostId, cols, rows, {
        passphrase: '',
        proxyPassphrase: '',
        password: '',
        proxyPassword: '',
        twoFactorCode: '',
        ...answers,
      })),

  sendInputB64: (sessionId: string, dataB64: string) => call('sendInput', () => App.SendInputB64(sessionId, dataB64)),
  /** Hard cap on one connection, in hours. Go clamps and returns what it enforces. */
  setSessionLifetime: (hours: number) => call('setSessionLifetime', () => App.SetSessionLifetime(hours)),
  resizePTY: (sessionId: string, cols: number, rows: number) => call('resize', () => App.ResizePTY(sessionId, cols, rows)),
  closeSession: (sessionId: string) => call('closeSession', () => App.CloseSession(sessionId)),
  /**
   * Opens a second terminal on a live connection. Nothing is re-authenticated —
   * Go opens another channel on the same SSH client.
   */
  duplicateSession: (sessionId: string) => call('duplicateSession', () => App.DuplicateSession(sessionId)),
  listSessions: () => call('listSessions', () => App.ListSessions() as Promise<SessionInfo[]>),

  listHosts: () => call('listHosts', () => App.ListHosts() as Promise<Host[]>),
  saveHost: (host: Host) => call('saveHost', () => App.SaveHost(host as unknown as GoHost) as Promise<Host>),
  setHostSecret: (hostId: string, secret: string) => call('setHostSecret', () => App.SetHostSecret(hostId, secret)),
  setHostProxySecret: (hostId: string, secret: string) =>
    call('setHostProxySecret', () => App.SetHostProxySecret(hostId, secret)),
  deleteHost: (hostId: string) => call('deleteHost', () => App.DeleteHost(hostId)),

  /** Both of these open a native dialog; an empty path means "cancelled". */
  exportHosts: (passphrase: string) =>
    call('exportHosts', () => App.ExportHosts(passphrase) as Promise<HostExport>),
  importHosts: (passphrase: string) =>
    call('importHosts', () => App.ImportHosts(passphrase) as Promise<HostImport>),

  knownHosts: () => call('knownHosts', () => App.KnownHosts() as Promise<HostKeyRecord[]>),
  forgetHostKey: (address: string) => call('forgetHostKey', () => App.ForgetHostKey(address)),

  info: () => call('info', () => App.Info() as Promise<AppInfo>),
  /** Where state lives now, and where it will live after a restart. */
  storage: () => call('storage', () => App.Storage() as Promise<StorageInfo>),
  /** "" returns to the default folder; rejects while GOTERM_DIR decides. */
  setStorageDir: (dir: string) =>
    call('setStorageDir', () => App.SetStorageDir(dir) as Promise<StorageInfo>),
  pickStorageDir: () => call('pickStorageDir', () => App.PickStorageDir()),
  /**
   * Paint the native title bar and window border in the app theme. The frame is
   * drawn by the OS, so it has to be told; a failure only means the frame keeps
   * the OS colours, which is why callers ignore the result.
   */
  setWindowTheme: (dark: boolean) => call('setWindowTheme', () => App.SetWindowTheme(dark)),
  /** Monospace families installed on this machine (Go filters the pitch). */
  fonts: () => call('fonts', () => App.Fonts() as Promise<{ name: string; mono: boolean }[]>),
  ping: () => call('ping', () => App.Ping()),
  selfTest: () => call('selfTest', () => App.DebugSelfTest()),
  pickKeyFile: () => call('pickKeyFile', () => App.PickKeyFile()),
  openURL: (url: string) => call('openURL', () => App.OpenURL(url)),
};

/**
 * The scoped file API that backs trzsz's pickers (see lib/fsaccess.ts).
 *
 * Whether this is actually installed is decided by `bootstrap()`, once the
 * backend probe has answered — in a plain browser there is no Go to call, and
 * the shim falls back to whatever the browser offers.
 */
export function fileBridgeApi(): FileBridgeApi {
  return {
    chooseDirectory: (title: string) => call('chooseDirectory', () => Files.ChooseDirectory(title)),
    chooseFiles: (title: string) =>
      // v3 reports "user cancelled" as null; the shim wants an empty list.
      call('chooseFiles', () => Files.ChooseFiles(title)).then((paths) => paths ?? []),
    stat: (path: string) => call('stat', () => Files.Stat(path) as Promise<FileEntry>),
    list: (path: string) => call('list', () => Files.List(path) as Promise<FileEntry[]>),
    readRange: (path: string, offset: number, length: number) =>
      call('readRange', () => Files.ReadRange(path, offset, length)),
    writeChunk: (path: string, offset: number, dataBase64: string) =>
      call('writeChunk', () => Files.WriteChunk(path, offset, dataBase64)),
    createFile: (path: string) => call('createFile', () => Files.CreateFile(path)),
    mkdir: (path: string) => call('mkdir', () => Files.Mkdir(path)),
  };
}
