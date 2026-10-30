/*
 * End-to-end file transfer through the *real* trzsz client.
 *
 * These tests play the server side of the trzsz wire protocol — banner, ACT,
 * CFG, NUM, NAME, SIZE, DATA, MD5 — against the actual TrzszFilter that GoTerm
 * ships, with the Go file bridge replaced by an in-memory fake. If a download
 * lands byte-exact and an upload reads the right ranges, then `tsz` and `trz`
 * work end to end; there is nothing else in the path.
 */
import { createHash } from 'node:crypto';
import { deflateSync, inflateSync } from 'node:zlib';

import { describe, expect, it } from 'vitest';
// The ESM build is what Vite bundles for the app; the CJS build thinks it is a
// node runtime and would take the fs-and-paths branch instead of the pickers.
import { TrzszFilter } from 'trzsz/lib/trzsz.mjs';

import { base64ToBytes, bytesToBase64, latin1ToBytes, toSendBytes } from './bytes';
import { installFilePickers, type FileBridgeApi } from './fsaccess';
import { ServerOutputStream } from './terminal';

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/*
 * trzsz frames string and binary payloads as base64(zlib(text)) — see Ti()/Fi()
 * in the library. Integers travel as plain digits. Getting this wrong is the
 * "incorrect header check" the transfer logs about.
 */
const b64 = (text: string): string => bytesToBase64(deflateSync(encoder.encode(text)));
const b64bin = (data: Uint8Array): string => bytesToBase64(deflateSync(data));
const unb64 = (payload: string): string => decoder.decode(inflateSync(base64ToBytes(payload)));
const unb64bin = (payload: string): Uint8Array => new Uint8Array(inflateSync(base64ToBytes(payload)));

const md5 = (data: Uint8Array): Uint8Array => new Uint8Array(createHash('md5').update(data).digest());
const concat = (chunks: Uint8Array[]): Uint8Array => {
  const out = new Uint8Array(chunks.reduce((sum, chunk) => sum + chunk.length, 0));
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
};

/** In-memory stand-in for the Go file bridge (files.go). */
class FakeFiles implements FileBridgeApi {
  files = new Map<string, Uint8Array>();
  dirs = new Set<string>(['C:/Downloads']);
  chosenDirectory = 'C:\\Downloads';
  chosenFiles: string[] = [];
  writes: Array<{ path: string; offset: number; length: number }> = [];
  reads: Array<{ path: string; offset: number; length: number }> = [];

  private key(path: string): string {
    return path.replace(/\\/g, '/').replace(/\/{2,}/g, '/');
  }
  async chooseDirectory(): Promise<string> {
    return this.chosenDirectory;
  }
  async chooseFiles(): Promise<string[]> {
    return this.chosenFiles;
  }
  async stat(path: string) {
    const key = this.key(path);
    if (this.dirs.has(key)) return { name: 'Downloads', dir: true, size: 0 };
    const data = this.files.get(key);
    if (!data) throw new Error(`missing ${key}`);
    return { name: key.split('/').pop() ?? key, dir: false, size: data.length };
  }
  async list(path: string) {
    const key = this.key(path);
    return [...this.files.keys()]
      .filter((file) => file.startsWith(`${key}/`) && !file.slice(key.length + 1).includes('/'))
      .map((file) => ({ name: file.slice(key.length + 1), dir: false, size: this.files.get(file)?.length ?? 0 }));
  }
  async readRange(path: string, offset: number, length: number): Promise<string> {
    const key = this.key(path);
    const data = this.files.get(key);
    if (!data) throw new Error(`missing ${key}`);
    this.reads.push({ path: key, offset, length });
    return bytesToBase64(data.slice(offset, offset + length));
  }
  async writeChunk(path: string, offset: number, dataBase64: string): Promise<void> {
    const key = this.key(path);
    const chunk = base64ToBytes(dataBase64);
    this.writes.push({ path: key, offset, length: chunk.length });
    const base = offset === 0 ? new Uint8Array(0) : (this.files.get(key) ?? new Uint8Array(0));
    const next = new Uint8Array(Math.max(base.length, offset + chunk.length));
    next.set(base, 0);
    next.set(chunk, offset);
    this.files.set(key, next);
  }
  async createFile(path: string): Promise<void> {
    const key = this.key(path);
    if (!this.files.has(key)) this.files.set(key, new Uint8Array(0));
  }
  async mkdir(path: string): Promise<void> {
    this.dirs.add(this.key(path));
  }
}

/** The server half: frames out, and parses whatever the client sends back. */
class ServerSide {
  private pending: Uint8Array = new Uint8Array(0);
  private awaitingData = 0;
  private out: Uint8Array[] = [];
  frames: string[] = [];
  data: Uint8Array[] = [];

  constructor(private readonly onFrame: (id: string, payload: string) => void) {}

  /** Accepts one sendToServer() call from the client. */
  push(input: string | Uint8Array): void {
    const bytes = toSendBytes(input);
    const merged = new Uint8Array(this.pending.length + bytes.length);
    merged.set(this.pending, 0);
    merged.set(bytes, this.pending.length);
    this.pending = merged;

    for (;;) {
      if (this.awaitingData > 0) {
        const take = Math.min(this.awaitingData, this.pending.length);
        if (take === 0) return;
        const chunk = this.pending.slice(0, take);
        this.pending = this.pending.slice(take);
        this.awaitingData -= take;
        this.data.push(chunk);
        this.onFrame('DATA-BODY', String(chunk.length));
        continue;
      }
      const newline = this.pending.indexOf(0x0a);
      if (newline < 0) return;
      const line = decoder.decode(this.pending.slice(0, newline));
      this.pending = this.pending.slice(newline + 1);
      if (!line) continue;
      this.frames.push(line);
      const header = /^#DATA:(\d+)$/.exec(line);
      if (header) {
        this.awaitingData = Number(header[1]);
        continue;
      }
      if (process.env.TRZSZ_TRACE) console.log('C-> ' + line.slice(0, 60));
      const colon = line.indexOf(':');
      this.onFrame(colon < 0 ? line.slice(1) : line.slice(1, colon), colon < 0 ? '' : line.slice(colon + 1));
    }
  }

  line(id: string, payload: string): void {
    if (process.env.TRZSZ_TRACE) console.log(`S-> #${id}:` + payload.slice(0, 40));
    this.out.push(encoder.encode(`#${id}:${payload}\n`));
  }
  text(id: string, value: string): void {
    this.line(id, b64(value));
  }
  integer(id: string, value: number): void {
    this.line(id, String(value));
  }
  binary(id: string, value: Uint8Array): void {
    this.line(id, b64bin(value));
  }
  /*
   * Without the server asking for "binary" mode (CFG binary:true + escape
   * chars), trzsz frames file data as #DATA:<base64(zlib(bytes))>. Only the
   * binary-escape variant uses a length header followed by raw bytes.
   */
  fileData(chunk: Uint8Array): void {
    this.binary('DATA', chunk);
  }
  banner(kind: 'S' | 'R', id = '0123456789012345'): void {
    this.out.push(encoder.encode(`::TRZSZ:TRANSFER:${kind}:1.1.6:${id}\r\n`));
  }

  /** Bytes queued for the client, and clears the queue. */
  take(): Uint8Array {
    const bytes = concat(this.out);
    this.out = [];
    return bytes;
  }
}

interface Rig {
  files: FakeFiles;
  server: ServerSide;
  filter: TrzszFilter;
  /** The same stream the app puts between the SSH pump and the filter. */
  output: ServerOutputStream;
  terminalText: () => string;
  /** Delivers queued server output to the client, repeatedly until idle. */
  settle: () => Promise<void>;
}

function createRig(onFrame: (id: string, payload: string) => void): Rig {
  const files = new FakeFiles();
  installFilePickers(files);

  const terminal: Uint8Array[] = [];
  const server = new ServerSide((id, payload) => {
    // A FAIL frame carries the client's own explanation (deflated). Surfacing it
    // turns a timeout into a message.
    if (id === 'FAIL' || id === 'fail') {
      try {
        console.error('[trzsz] client-side failure:', unb64(payload));
      } catch {
        console.error('[trzsz] client-side failure (undecodable):', payload.slice(0, 120));
      }
      return;
    }
    onFrame(id, payload);
  });
  let filter: TrzszFilter;
  // The app never hands server bytes to the filter directly; it goes through
  // ServerOutputStream, so the tests must use that seam too.
  const output = new ServerOutputStream({
    processServerOutput: (bytes: Uint8Array) => filter.processServerOutput(bytes),
    isTransferringFiles: () => filter.isTransferringFiles(),
  });

  // Every client message can make the server queue more output; delivering it
  // is what keeps the protocol moving. The chain is self-sustaining because
  // each delivery produces the next client message.
  const settle = async (): Promise<void> => {
    for (let round = 0; round < 50; round += 1) {
      const queued = server.take();
      if (!queued.length) return;
      output.write(queued);
    }
  };

  filter = new TrzszFilter({
    terminalColumns: 100,
    maxDataChunkSize: 1 << 20,
    writeToTerminal: (output) => {
      if (typeof output === 'string') terminal.push(encoder.encode(output));
      else if (output instanceof Uint8Array) terminal.push(output);
      else if (output instanceof ArrayBuffer) terminal.push(new Uint8Array(output));
    },
    sendToServer: (input) => {
      server.push(input);
      // Anything the server queued in response is fed back to the client on the
      // next macrotask, the way a real SSH channel would.
      queueMicrotask(() => void settle());
    },
  });

  return { files, server, filter, output, terminalText: () => decoder.decode(concat(terminal)), settle };
}

async function until(cond: () => boolean, what: string, timeoutMs = 4000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (cond()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error(`timed out waiting for ${what}`);
}

describe('download: tsz <file>', () => {
  it('walks the protocol and writes a byte-exact file', async () => {
    const payload = new Uint8Array(300_000);
    for (let i = 0; i < payload.length; i += 1) payload[i] = (i * 11 + 5) & 0xff;
    const chunkSize = 32_768;
    const chunks = Math.ceil(payload.length / chunkSize);

    let stage = 'idle';
    let acks = 0;
    let exited = false;

    const rig = createRig((id) => {
      if (id === 'ACT') {
        stage = 'awaitNumAck';
        rig.server.line('CFG', b64(JSON.stringify({ lang: 'js', bufsize: chunkSize, timeout: 100 })));
        rig.server.integer('NUM', 1);
        return;
      }
      if (id === 'SUCC') {
        if (stage === 'awaitNumAck') {
          stage = 'awaitNameAck';
          rig.server.text('NAME', 'demo.bin');
          return;
        }
        if (stage === 'awaitNameAck') {
          stage = 'awaitSizeAck';
          rig.server.integer('SIZE', payload.length);
          return;
        }
        if (stage === 'awaitSizeAck') {
          stage = 'streaming';
          for (let offset = 0; offset < payload.length; offset += chunkSize) {
            rig.server.fileData(payload.slice(offset, Math.min(offset + chunkSize, payload.length)));
          }
          return;
        }
        if (stage === 'streaming') {
          acks += 1;
          if (acks === chunks) {
            stage = 'awaitMd5Ack';
            rig.server.binary('MD5', md5(payload));
          }
          return;
        }
        if (stage === 'awaitMd5Ack') {
          stage = 'done';
          return;
        }
        return;
      }
      if (id === 'EXIT') {
        exited = true;
      }
    });

    rig.server.banner('S');
    rig.output.write(rig.server.take());
    await until(() => stage === 'done' && exited, 'the download to finish');

    const written = rig.files.files.get('C:/Downloads/demo.bin');
    expect(written).toBeDefined();
    expect(written?.length).toBe(payload.length);
    expect(Buffer.from(written ?? []).equals(Buffer.from(payload))).toBe(true);

    // Buffered, ascending writes rather than one bridge call per 32 KiB chunk.
    expect(rig.files.writes.length).toBeLessThan(chunks);
    expect(rig.files.writes[0]?.offset).toBe(0);

    // The banner is printed (trzsz writes it, then sniffs it), but the protocol
    // frames themselves must never reach the terminal as text.
    const terminal = rig.terminalText();
    expect(terminal).toContain('demo.bin');
    expect(terminal).toMatch(/%/);
    expect(terminal).not.toContain('#ACT:');
    expect(terminal).not.toContain('#DATA:');
    expect(rig.filter.isTransferringFiles()).toBe(false);
  });

  it('declines cleanly when the user cancels the folder picker', async () => {
    let act = '';
    const rig = createRig((id, payload) => {
      if (id === 'ACT') act = payload;
    });
    rig.files.chosenDirectory = '';

    rig.output.write(latin1ToBytes('::TRZSZ:TRANSFER:S:1.1.6:0123456789012345\r\n'));
    await until(() => act !== '', 'the client to answer the banner');

    const answer = JSON.parse(unb64(act)) as { confirm: boolean };
    expect(answer.confirm).toBe(false);
    expect(rig.filter.isTransferringFiles()).toBe(false);
    expect(rig.files.writes.length).toBe(0);
  });
});

describe('banner arrival', () => {
  it('still starts a transfer when the banner is split across output events', async () => {
    // The reported symptom: `trz` printed ::TRZSZ:TRANSFER:R:1.2.0: and nothing
    // happened, because the pump delivered the banner in pieces.
    let act = '';
    const rig = createRig((id, payload) => {
      if (id === 'ACT') act = payload;
    });
    rig.files.chosenFiles = [];

    const banner = '::TRZSZ:TRANSFER:R:1.2.0:\r\n';
    for (const piece of [banner.slice(0, 7), banner.slice(7, 20), banner.slice(20)]) {
      rig.output.write(latin1ToBytes(piece));
    }

    await until(() => act !== '', 'the client to answer the banner');
    const answer = JSON.parse(unb64(act)) as { confirm: boolean };
    expect(answer.confirm).toBe(false); // no files picked, but the banner was seen
    expect(rig.filter.isTransferringFiles()).toBe(false);
  });
});

describe('upload: trz', () => {
  it('reads the picked file in ranges and streams it byte-exact', async () => {
    const payload = new Uint8Array(200_000);
    for (let i = 0; i < payload.length; i += 1) payload[i] = (i * 3) & 0xff;

    const names: string[] = [];
    const sizes: number[] = [];
    const receivedData: Uint8Array[] = [];
    let digest: Uint8Array | null = null;
    let stage = 'idle';
    let exited = false;

    const rig = createRig((id, payloadText) => {
      switch (id) {
        case 'ACT':
          stage = 'awaitingFile';
          rig.server.line('CFG', b64(JSON.stringify({ lang: 'js', bufsize: 65536, timeout: 100 })));
          return;
        case 'NUM':
          rig.server.integer('SUCC', Number(payloadText));
          return;
        case 'NAME':
          names.push(unb64(payloadText));
          rig.server.text('SUCC', names[names.length - 1] ?? '');
          return;
        case 'SIZE':
          sizes.push(Number(payloadText));
          rig.server.integer('SUCC', Number(payloadText));
          return;
        case 'DATA': {
          // The client sends its file data compressed, one frame per chunk.
          const chunk = unb64bin(payloadText);
          receivedData.push(chunk);
          rig.server.integer('SUCC', chunk.length);
          return;
        }
        case 'MD5':
          digest = unb64bin(payloadText);
          rig.server.binary('SUCC', digest);
          stage = 'done';
          return;
        case 'EXIT':
          exited = true;
          return;
        default:
          return;
      }
    });

    rig.files.files.set('C:/Downloads/upload.bin', payload);
    rig.files.chosenFiles = ['C:\\Downloads\\upload.bin'];

    rig.server.banner('R');
    rig.output.write(rig.server.take());
    await until(() => stage === 'done' && exited, 'the upload to finish');

    expect(names).toEqual(['upload.bin']);
    expect(sizes).toEqual([payload.length]);

    const streamed = concat(receivedData);
    expect(streamed.length).toBe(payload.length);
    expect(Buffer.from(streamed).equals(Buffer.from(payload))).toBe(true);

    // The client's own MD5 must match what the server received.
    expect(digest).not.toBeNull();
    expect(bytesToBase64(md5(streamed))).toBe(bytesToBase64(digest ?? new Uint8Array(0)));

    // Uploads are read in ranges; a 200 KB file must not arrive in one blob.
    expect(rig.files.reads.length).toBeGreaterThan(0);
    expect(Math.max(...rig.files.reads.map((read) => read.length))).toBeLessThanOrEqual(1 << 20);
    expect(rig.filter.isTransferringFiles()).toBe(false);
  });
});
