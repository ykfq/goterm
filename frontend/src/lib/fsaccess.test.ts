/*
 * The trzsz file-picker shim, tested against a fake Go bridge.
 *
 * These are the behaviours that decide whether `tsz file` lands a correct file
 * on disk and whether `trz` can read what the user picked: chunk offsets, the
 * truncate-on-first-write rule, cancel semantics, and refusing a server-supplied
 * name that tries to escape the chosen folder.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { base64ToBytes, bytesToBase64 } from './bytes';
import {
  createDirectoryHandleForTest,
  createFileHandleForTest,
  installFilePickers,
  pickerMode,
  type FileBridgeApi,
  type FileEntry,
} from './fsaccess';

interface FakeBridge extends FileBridgeApi {
  files: Map<string, Uint8Array>;
  dirs: Set<string>;
  /** What the native dialogs will return next. */
  nextDirectory: string;
  nextFiles: string[];
  writeCalls: Array<{ path: string; offset: number }>;
}

function norm(path: string): string {
  return path.replace(/\\/g, '/').replace(/\/{2,}/g, '/');
}

function fakeBridge(): FakeBridge {
  const files = new Map<string, Uint8Array>();
  const dirs = new Set<string>(['C:/Downloads']);
  const writeCalls: Array<{ path: string; offset: number }> = [];

  const needFile = (path: string): string => {
    const key = norm(path);
    if (!files.has(key)) throw new Error(`no such file: ${key}`);
    return key;
  };

  return {
    files,
    dirs,
    writeCalls,
    nextDirectory: 'C:\\Downloads',
    nextFiles: [],

    async chooseDirectory() {
      return this.nextDirectory;
    },
    async chooseFiles() {
      return this.nextFiles;
    },
    async stat(path): Promise<FileEntry> {
      const key = norm(path);
      if (dirs.has(key)) return { name: key.split('/').pop() ?? key, dir: true, size: 0 };
      const data = files.get(key);
      if (!data) throw new Error(`no such file or directory: ${key}`);
      return { name: key.split('/').pop() ?? key, dir: false, size: data.length };
    },
    async list(path): Promise<FileEntry[]> {
      const key = norm(path).replace(/\/$/, '');
      if (!dirs.has(key)) throw new Error(`no such directory: ${key}`);
      const seen = new Map<string, FileEntry>();
      for (const dir of dirs) {
        if (dir.startsWith(`${key}/`) && dir !== key) {
          const rest = dir.slice(key.length + 1);
          const name = rest.split('/')[0];
          if (name) seen.set(name, { name, dir: true, size: 0 });
        }
      }
      for (const [file, data] of files) {
        if (!file.startsWith(`${key}/`) || file === key) continue;
        const rest = file.slice(key.length + 1);
        if (!rest || rest.includes('/')) continue;
        seen.set(rest, { name: rest, dir: false, size: data.length });
      }
      return [...seen.values()];
    },
    async readRange(path, offset, length) {
      const key = needFile(path);
      const data = files.get(key) as Uint8Array;
      const from = Math.max(0, Math.min(offset, data.length));
      const to = Math.max(from, Math.min(offset + Math.max(0, length), data.length));
      return bytesToBase64(data.slice(from, to));
    },
    async writeChunk(path, offset, dataBase64) {
      const key = norm(path);
      const chunk = base64ToBytes(dataBase64);
      writeCalls.push({ path: key, offset });
      let buffer = files.get(key);
      if (offset === 0 || !buffer) {
        buffer = new Uint8Array(offset + chunk.length);
      }
      if (buffer.length < offset + chunk.length) {
        const grown = new Uint8Array(offset + chunk.length);
        grown.set(buffer, 0);
        buffer = grown;
      }
      buffer.set(chunk, offset);
      files.set(key, buffer);
    },
    async createFile(path) {
      const key = norm(path);
      if (!files.has(key)) files.set(key, new Uint8Array(0));
    },
    async mkdir(path) {
      dirs.add(norm(path));
    },
  };
}

describe('installFilePickers', () => {
  it('replaces the browser pickers with the Go-backed ones', () => {
    expect(installFilePickers(fakeBridge())).toBe('bridge');
    expect(pickerMode()).toBe('bridge');
    expect(typeof (window as unknown as { showDirectoryPicker?: unknown }).showDirectoryPicker).toBe('function');
    expect(typeof (window as unknown as { showOpenFilePicker?: unknown }).showOpenFilePicker).toBe('function');
  });

  it('reports browser/none when there is no backend', () => {
    const target = window as unknown as Record<string, unknown>;
    delete target.showDirectoryPicker;
    delete target.showOpenFilePicker;
    expect(installFilePickers(null)).toBe('none');

    target.showDirectoryPicker = () => undefined;
    expect(installFilePickers(null)).toBe('browser');
    delete target.showDirectoryPicker;
  });

  it('turns a cancelled dialog into AbortError, which trzsz treats as a decline', async () => {
    const api = fakeBridge();
    api.nextDirectory = '';
    installFilePickers(api);
    const showDirectoryPicker = (window as unknown as { showDirectoryPicker: () => Promise<unknown> }).showDirectoryPicker;
    await expect(showDirectoryPicker()).rejects.toMatchObject({ name: 'AbortError' });

    const openApi = fakeBridge();
    openApi.nextFiles = [];
    installFilePickers(openApi);
    const showOpenFilePicker = (window as unknown as { showOpenFilePicker: (options?: unknown) => Promise<unknown> })
      .showOpenFilePicker;
    await expect(showOpenFilePicker({ multiple: true })).rejects.toMatchObject({ name: 'AbortError' });
  });
});

describe('directory handle (the download path)', () => {
  let api: FakeBridge;

  beforeEach(() => {
    api = fakeBridge();
  });

  it('writes chunks at ascending offsets and lands the exact bytes', async () => {
    const handle = createDirectoryHandleForTest(api, 'C:\\Downloads');
    const file = await handle.getFileHandle('report.bin', { create: true });
    expect(api.files.has('C:/Downloads/report.bin')).toBe(true);

    const writer = await file.createWritable();
    const total = 1_500_000; // larger than the shim's 1 MiB write buffer
    const payload = new Uint8Array(total);
    for (let i = 0; i < total; i += 1) payload[i] = (i * 7) & 0xff;

    // trzsz hands over ~32 KiB at a time; the shim buffers to ~1 MiB so the
    // bridge sees few, large calls.
    for (let offset = 0; offset < total; offset += 32_768) {
      await writer.write(payload.slice(offset, Math.min(offset + 32_768, total)));
    }
    await writer.close();

    const written = api.files.get('C:/Downloads/report.bin');
    expect(written?.length).toBe(total);
    // Compared as bytes, not as two 1.5-million-element JS arrays: the slow part
    // used to be the comparison, not the write, and it starved under load.
    expect(Buffer.from(written ?? []).equals(Buffer.from(payload))).toBe(true);

    const offsets = api.writeCalls.map((call) => call.offset);
    expect(offsets[0]).toBe(0);
    // Fewer Go calls than writes, strictly ascending, and no gaps: the last
    // chunk ends exactly at the file size.
    expect(offsets.length).toBeLessThan(10);
    expect(offsets).toEqual([...offsets].sort((a, b) => a - b));
    expect(new Set(offsets).size).toBe(offsets.length);
  });

  it('flushes when the buffer crosses 1 MiB and again on close', async () => {
    const handle = createDirectoryHandleForTest(api, 'C:\\Downloads');
    const writer = await (await handle.getFileHandle('two.bin')).createWritable();
    const payload = new Uint8Array(1_500_000).fill(3);

    await writer.write(payload.slice(0, 1_200_000)); // crosses the buffer: flush at 0
    await writer.write(payload.slice(1_200_000)); // 300_000 still buffered
    expect(api.writeCalls.map((call) => call.offset)).toEqual([0]);
    await writer.close();

    expect(api.writeCalls.map((call) => call.offset)).toEqual([0, 1_200_000]);
    expect(api.files.get('C:/Downloads/two.bin')?.length).toBe(1_500_000);
  });

  it('truncates on the first write of a new transfer', async () => {
    api.files.set('C:/Downloads/old.txt', new TextEncoder().encode('previous contents that must not survive'));
    const handle = createDirectoryHandleForTest(api, 'C:\\Downloads');
    const file = await handle.getFileHandle('old.txt', { create: true });
    const writer = await file.createWritable();
    await writer.write(new TextEncoder().encode('new'));
    await writer.close();
    expect(new TextDecoder().decode(api.files.get('C:/Downloads/old.txt'))).toBe('new');
  });

  it('still creates an empty file when nothing was written', async () => {
    const handle = createDirectoryHandleForTest(api, 'C:\\Downloads');
    const file = await handle.getFileHandle('empty.txt');
    const writer = await file.createWritable();
    await writer.close();
    expect(api.files.get('C:/Downloads/empty.txt')?.length).toBe(0);
  });

  it('creates nested directories for directory transfers', async () => {
    const handle = createDirectoryHandleForTest(api, 'C:\\Downloads');
    const sub = await handle.getDirectoryHandle('sub folder', { create: true });
    expect(api.dirs.has('C:/Downloads/sub folder')).toBe(true);
    const inner = await sub.getFileHandle('a.txt', { create: true });
    expect(inner.name).toBe('a.txt');
  });

  it('iterates children so trzsz can pick a free name', async () => {
    api.files.set('C:/Downloads/a.txt', new Uint8Array(3));
    api.files.set('C:/Downloads/b.txt', new Uint8Array(1));
    api.dirs.add('C:/Downloads/keep');
    const handle = createDirectoryHandleForTest(api, 'C:\\Downloads');

    const names: string[] = [];
    for await (const child of handle.values()) names.push(child.name);
    expect(names.sort()).toEqual(['a.txt', 'b.txt', 'keep']);

    const entries: string[] = [];
    for await (const [name] of handle.entries()) entries.push(name);
    expect(entries.sort()).toEqual(['a.txt', 'b.txt', 'keep']);
  });

  it('refuses a name that tries to escape the chosen folder', async () => {
    const handle = createDirectoryHandleForTest(api, 'C:\\Downloads');
    await expect(handle.getFileHandle('../evil.txt')).rejects.toThrow(/unsafe file name/);
    await expect(handle.getFileHandle('..\\evil.txt')).rejects.toThrow(/unsafe file name/);
    await expect(handle.getFileHandle('C:evil.txt')).rejects.toThrow(/unsafe file name/);
    await expect(handle.getDirectoryHandle('a/b', { create: true })).rejects.toThrow(/unsafe file name/);
  });
});

describe('file handle (the upload path)', () => {
  it('exposes size and slices without loading the whole file', async () => {
    const api = fakeBridge();
    const payload = new Uint8Array(10_000);
    for (let i = 0; i < payload.length; i += 1) payload[i] = i & 0xff;
    api.files.set('C:/Downloads/big.bin', payload);

    const handle = createFileHandleForTest(api, 'C:\\Downloads\\big.bin');
    const file = await handle.getFile();
    expect(file.name).toBe('big.bin');
    expect(file.size).toBe(10_000);

    // trzsz reads with exactly this call pattern.
    const first = new Uint8Array(await file.slice(0, 4096).arrayBuffer());
    const tail = new Uint8Array(await file.slice(9_000, 10_000).arrayBuffer());
    expect(Array.from(first)).toEqual(Array.from(payload.slice(0, 4096)));
    expect(Array.from(tail)).toEqual(Array.from(payload.slice(9_000)));

    // Slicing at the end yields nothing rather than an error.
    expect(new Uint8Array(await file.slice(10_000, 20_000).arrayBuffer()).length).toBe(0);
  });

  it('maps the picked paths into handles', async () => {
    const api = fakeBridge();
    api.files.set('C:/Downloads/one.txt', new TextEncoder().encode('1'));
    api.files.set('C:/Downloads/two.txt', new TextEncoder().encode('22'));
    api.nextFiles = ['C:\\Downloads\\one.txt', 'C:\\Downloads\\two.txt'];
    installFilePickers(api);

    const showOpenFilePicker = (
      window as unknown as {
        showOpenFilePicker: (
          o: { multiple: boolean },
        ) => Promise<Array<{ name: string; getFile: () => Promise<{ name: string; size: number }> }>>;
      }
    ).showOpenFilePicker;
    const picked = await showOpenFilePicker({ multiple: true });
    expect(picked.map((handle) => handle.name)).toEqual(['one.txt', 'two.txt']);
    const files = await Promise.all(picked.map((handle) => handle.getFile()));
    expect(files.map((file) => [file.name, file.size])).toEqual([
      ['one.txt', 1],
      ['two.txt', 2],
    ]);
  });

  it('surfaces a Go-side refusal instead of pretending the write worked', async () => {
    const api = fakeBridge();
    const failing: FileBridgeApi = {
      ...api,
      writeChunk: vi.fn(async () => {
        throw new Error('/etc/passwd is outside every folder you chose in this session');
      }),
    };
    const handle = createDirectoryHandleForTest(failing, 'C:\\Downloads');
    const file = await handle.getFileHandle('x.bin');
    const writer = await file.createWritable();
    await writer.write(new Uint8Array([1, 2, 3]));
    await expect(writer.close()).rejects.toThrow(/outside every folder/);
  });
});
