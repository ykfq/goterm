/*
 * Native file pickers for trzsz.
 *
 * trzsz.js was built for web terminals, so a download is
 * `window.showDirectoryPicker()` and an upload is `window.showOpenFilePicker()`.
 * Inside WebView2 those are a bad bet: the File System Access API needs a secure
 * context *and* transient user activation, while the transfer banner arrives
 * asynchronously after the keystroke that started it. The practical result was
 * `tsz file` doing nothing at all in a desktop app.
 *
 * So this module installs replacements for exactly those two functions, backed
 * by Go (a real Explorer dialog) and implementing only the slice of the FS
 * Access API that trzsz actually touches:
 *
 *   directory: name, getFileHandle, getDirectoryHandle, values, entries, keys
 *   file:      name, getFile().{name,size,slice().arrayBuffer()}, createWritable()
 *   writable:  write(Uint8Array), close(), abort()
 *
 * File contents never enter this process wholesale: uploads read ranges on
 * demand, downloads stream chunks into Go. Nothing may touch a path outside the
 * folder the user picked (`files.go` enforces that on the Go side too).
 */
import { base64ToBytes, bytesToBase64 } from './bytes';

export interface FileEntry {
  name: string;
  dir: boolean;
  size: number;
}

/** The Go methods this shim needs (bound as main.FileBridge). */
export interface FileBridgeApi {
  chooseDirectory(title: string): Promise<string>;
  chooseFiles(title: string): Promise<string[]>;
  stat(path: string): Promise<FileEntry>;
  list(path: string): Promise<FileEntry[]>;
  readRange(path: string, offset: number, length: number): Promise<string>;
  writeChunk(path: string, offset: number, dataBase64: string): Promise<void>;
  createFile(path: string): Promise<void>;
  mkdir(path: string): Promise<void>;
}

export type PickerMode = 'bridge' | 'browser' | 'none';

let installed: PickerMode = 'none';

/** Which picker implementation file transfer will use. */
export function pickerMode(): PickerMode {
  return installed;
}

/** Downloads are buffered in memory up to this size before one Go call. */
const WRITE_BUFFER = 1 << 20;

function abortError(): Error {
  // trzsz checks `error.name === 'AbortError'` to distinguish "user cancelled"
  // from "transfer failed", in both the download and the upload direction.
  const error = new Error('The user cancelled the file picker.');
  error.name = 'AbortError';
  return error;
}

function assertSafeName(name: string): void {
  if (
    typeof name !== 'string' ||
    name === '' ||
    name === '.' ||
    name === '..' ||
    name.includes('/') ||
    name.includes('\\') ||
    name.includes('\0') ||
    // ':' would make a Windows path drive-relative ("C:evil"), and it is not a
    // legal file name character anyway.
    name.includes(':')
  ) {
    throw new Error(`refusing an unsafe file name from the transfer: ${JSON.stringify(name)}`);
  }
}

function join(root: string, name: string): string {
  assertSafeName(name);
  return root.endsWith('/') || root.endsWith('\\') ? root + name : `${root}/${name}`;
}

function baseName(path: string): string {
  const parts = path.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

export interface NativeWritable {
  write(chunk: Uint8Array | ArrayBuffer | Blob): Promise<void>;
  close(): Promise<void>;
  abort(): Promise<void>;
}

class ChunkedWriter implements NativeWritable {
  private offset = 0;
  private buffer: Uint8Array = new Uint8Array(0);
  private closed = false;

  constructor(
    private readonly api: FileBridgeApi,
    private readonly path: string,
  ) {}

  async write(chunk: Uint8Array | ArrayBuffer | Blob): Promise<void> {
    if (this.closed) throw new Error(`write after close: ${this.path}`);
    const bytes = await toBytes(chunk);
    if (!bytes.length) return;
    this.buffer = concat(this.buffer, bytes);
    if (this.buffer.length >= WRITE_BUFFER) await this.flush();
  }

  /** Pushes buffered bytes through one Go call. */
  private async flush(): Promise<void> {
    if (!this.buffer.length) return;
    const chunk = this.buffer;
    this.buffer = new Uint8Array(0);
    await this.api.writeChunk(this.path, this.offset, bytesToBase64(chunk));
    this.offset += chunk.length;
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    if (this.offset === 0 && this.buffer.length === 0) {
      // A zero-byte download still has to exist on disk.
      await this.api.writeChunk(this.path, 0, '');
      return;
    }
    await this.flush();
  }

  async abort(): Promise<void> {
    // Keep whatever arrived: trzsz deletes partial files itself when it decides
    // to, and a silent truncation here would lose the user's bytes.
    await this.close().catch(() => undefined);
  }
}

async function toBytes(chunk: Uint8Array | ArrayBuffer | Blob): Promise<Uint8Array> {
  if (chunk instanceof Uint8Array) return chunk;
  if (chunk instanceof ArrayBuffer) return new Uint8Array(chunk);
  return new Uint8Array(await chunk.arrayBuffer());
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  if (!a.length) return b;
  const out = new Uint8Array(a.length + b.length);
  out.set(a, 0);
  out.set(b, a.length);
  return out;
}

/** A File-alike: only the members trzsz reads. */
export interface NativeFile {
  name: string;
  size: number;
  slice(start?: number, end?: number): { arrayBuffer(): Promise<ArrayBuffer> };
  arrayBuffer(): Promise<ArrayBuffer>;
  text(): Promise<string>;
}

function makeFile(api: FileBridgeApi, path: string, name: string, size: number): NativeFile {
  const read = async (start: number, end: number): Promise<ArrayBuffer> => {
    const from = Math.max(0, Math.min(start, size));
    const to = Math.max(from, Math.min(end, size));
    if (to === from) return new ArrayBuffer(0);
    const b64 = await api.readRange(path, from, to - from);
    const bytes = base64ToBytes(b64);
    // Uint8Array.buffer may be a view into a larger allocation only when we
    // created it from an offset; base64ToBytes always allocates exactly.
    return bytes.buffer as ArrayBuffer;
  };
  return {
    name,
    size,
    slice: (start = 0, end = size) => ({ arrayBuffer: () => read(start, end) }),
    arrayBuffer: () => read(0, size),
    text: async () => new TextDecoder().decode(new Uint8Array(await read(0, size))),
  };
}

export interface NativeFileHandle {
  kind: 'file';
  name: string;
  getFile(): Promise<NativeFile>;
  createWritable(): Promise<NativeWritable>;
}

export interface NativeDirectoryHandle {
  kind: 'directory';
  name: string;
  getFileHandle(name: string, options?: { create?: boolean }): Promise<NativeFileHandle>;
  getDirectoryHandle(name: string, options?: { create?: boolean }): Promise<NativeDirectoryHandle>;
  values(): AsyncIterableIterator<NativeFileHandle | NativeDirectoryHandle>;
  entries(): AsyncIterableIterator<[string, NativeFileHandle | NativeDirectoryHandle]>;
  keys(): AsyncIterableIterator<string>;
  [Symbol.asyncIterator](): AsyncIterableIterator<NativeFileHandle | NativeDirectoryHandle>;
}

function makeFileHandle(api: FileBridgeApi, path: string, nameHint?: string): NativeFileHandle {
  const name = nameHint ?? baseName(path);
  return {
    kind: 'file',
    name,
    async getFile() {
      const entry = await api.stat(path);
      return makeFile(api, path, entry.name || name, entry.size);
    },
    async createWritable() {
      return new ChunkedWriter(api, path);
    },
  };
}

function makeDirectoryHandle(api: FileBridgeApi, path: string): NativeDirectoryHandle {
  const self: NativeDirectoryHandle = {
    kind: 'directory',
    name: baseName(path),
    async getFileHandle(name, options) {
      const child = join(path, name);
      if (options?.create) {
        // Create-if-missing, never truncate: createWritable() is what decides
        // whether existing bytes survive.
        await api.createFile(child);
      }
      return makeFileHandle(api, child, name);
    },
    async getDirectoryHandle(name, options) {
      const child = join(path, name);
      if (options?.create) await api.mkdir(child);
      return makeDirectoryHandle(api, child);
    },
    async *values() {
      for (const entry of await api.list(path)) {
        assertSafeName(entry.name);
        const child = join(path, entry.name);
        yield entry.dir ? makeDirectoryHandle(api, child) : makeFileHandle(api, child, entry.name);
      }
    },
    async *entries() {
      for await (const handle of self.values()) {
        yield [handle.name, handle] as [string, NativeFileHandle | NativeDirectoryHandle];
      }
    },
    async *keys() {
      for await (const handle of self.values()) yield handle.name;
    },
    [Symbol.asyncIterator]() {
      return self.values();
    },
  };
  return self;
}

/**
 * Installs the shims. Returns the mode that is now in force.
 *
 * The Go-backed pickers win whenever the bridge exists: a desktop user should
 * get an Explorer dialog, and behaviour should not depend on whether the webview
 * happens to expose the FS Access API this month.
 */
export function installFilePickers(api: FileBridgeApi | null): PickerMode {
  const target = window as unknown as Record<string, unknown>;
  if (!api) {
    const browserHas = typeof target.showDirectoryPicker === 'function';
    installed = browserHas ? 'browser' : 'none';
    return installed;
  }

  target.showDirectoryPicker = async (options?: { id?: string; mode?: string; startIn?: string }) => {
    void options;
    // The same call serves downloads ("choose where to save") and directory
    // uploads; the title is the only way to tell the user which it is.
    const dir = await api.chooseDirectory('Choose a folder for the file transfer');
    if (!dir) throw abortError();
    return makeDirectoryHandle(api, dir);
  };

  target.showOpenFilePicker = async (options?: { multiple?: boolean }) => {
    void options;
    const paths = await api.chooseFiles('Choose files to upload');
    if (!paths.length) throw abortError();
    return paths.map((path) => makeFileHandle(api, path));
  };

  installed = 'bridge';
  return installed;
}

/** Test seam: build a directory handle without touching `window`. */
export function createDirectoryHandleForTest(api: FileBridgeApi, path: string): NativeDirectoryHandle {
  return makeDirectoryHandle(api, path);
}

/** Test seam: build a file handle without touching `window`. */
export function createFileHandleForTest(api: FileBridgeApi, path: string): NativeFileHandle {
  return makeFileHandle(api, path);
}
