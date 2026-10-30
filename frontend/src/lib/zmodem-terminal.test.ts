/*
 * lrzsz end to end, through the real terminal pipe — not beside it.
 *
 * `zmodem.test.ts` proves the protocol engine works when bytes are handed to it
 * in whole headers. That is not how bytes arrive in the app: the Go pump batches
 * by time and size, so a ZMODEM header can straddle two chunks, and every byte
 * has already passed through the bridge's own routing before the protocol sees
 * it. This file runs the *bridge* against a real ZMODEM peer and feeds it the way
 * a live session does, which is the only way a wiring mistake shows up as a
 * failure instead of a hang on someone's machine.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

// trzsz refuses to construct under Node (it looks for `require('fs')` and then
// demands the Node-only callbacks). Here the question is whether ZMODEM bytes
// survive the bridge, so the filter is reduced to "pass server output on".
vi.mock('trzsz', () => ({
  TrzszFilter: class {
    private readonly options: { writeToTerminal: (output: unknown) => void };
    constructor(options: { writeToTerminal: (output: unknown) => void }) {
      this.options = options;
    }
    processServerOutput(bytes: Uint8Array): void {
      this.options.writeToTerminal(bytes);
    }
    processTerminalInput(): void {
      /* not what this file is about */
    }
    processBinaryInput(): void {
      /* nothing */
    }
    isTransferringFiles(): boolean {
      return false;
    }
    setTerminalColumns(): void {
      /* nothing */
    }
    stopTransferringFiles(): void {
      /* nothing */
    }
  },
}));

// The pickers are what the bridge hands to the pipe. They are stubbed here so the
// test can say what the user chose; the pipe itself stays real.
const picker = vi.hoisted(() => ({
  download: null as null | (() => Promise<unknown>),
  upload: null as null | (() => Promise<unknown[] | null>),
}));

vi.mock('./zmodem', async (importOriginal) => {
  const real = await importOriginal<typeof import('./zmodem')>();
  return {
    ...real,
    chooseDownloadTarget: async () => (picker.download ? picker.download() : null),
    chooseUploads: async () => (picker.upload ? picker.upload() : []),
  };
});

import Zmodem from 'zmodem.js';

import { createTerminalBridge } from './terminal';
import type { ZmodemUpload, ZmodemWritable } from './zmodem';

interface Peer {
  consume(bytes: Uint8Array | number[]): void;
  set_sender(handler: (octets: number[]) => void): Peer;
  start(): void;
  close(): Promise<void>;
  send_offer(details: Record<string, unknown>): Promise<PeerTransfer | undefined>;
  on(event: string, handler: (...args: never[]) => void): void;
}

interface PeerTransfer {
  on(event: string, handler: (octets: number[]) => void): void;
  accept(): Promise<unknown>;
  send(bytes: Uint8Array): void;
  end(bytes?: Uint8Array): Promise<void>;
}

const Z = Zmodem as unknown as {
  Session: { Send: new (zrinit: unknown) => Peer; Receive: new () => Peer };
  Header: { build(name: string, ...args: unknown[]): { to_hex(): number[] }; parse_hex(octets: number[]): unknown };
};

/** A payload no accidental text handling in the middle would survive. */
const PAYLOAD = Uint8Array.from(Array.from({ length: 5000 }, (_, i) => (i * 37 + 11) % 256));

function joined(chunks: Uint8Array[]): Uint8Array {
  const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

/**
 * A bridge wired to a two-way wire. `deliver` writes to the bridge the way the
 * output pump does: in pieces the protocol never chose, so a header that
 * straddles a chunk boundary has to survive it.
 */
function wireBridge(chunkSize: number) {
  const toServer: Uint8Array[] = [];
  const notices: string[] = [];
  let feedPeer: ((bytes: Uint8Array) => void) | null = null;

  const bridge = createTerminalBridge({
    id: `zmodem-e2e-${Math.random().toString(36).slice(2)}`,
    onSend: (bytes) => {
      toServer.push(bytes.slice());
      feedPeer?.(bytes);
    },
    onResize: () => undefined,
    onNotice: (level, message) => notices.push(`${level}: ${message}`),
  });

  // What the terminal itself was shown. The progress line goes there rather than
  // into a widget, so this is where to look for it — and it is the whole point of
  // the design: `rz` looks like `trz` because both draw in the same place.
  const terminal: string[] = [];
  const writeToTerminal = bridge.term.write.bind(bridge.term);
  bridge.term.write = ((data: string | Uint8Array, callback?: () => void) => {
    terminal.push(typeof data === 'string' ? data : new TextDecoder().decode(data));
    writeToTerminal(data, callback);
  }) as typeof bridge.term.write;

  const deliver = (bytes: Uint8Array) => {
    for (let offset = 0; offset < bytes.length; offset += chunkSize) {
      bridge.write(bytes.slice(offset, offset + chunkSize));
    }
  };

  const attach = <P extends Peer>(peer: P): P => {
    peer.set_sender((octets) => deliver(Uint8Array.from(octets)));
    feedPeer = (bytes) => {
      try {
        peer.consume(Array.from(bytes));
      } catch {
        /* the peer reacting to our abort sequence: expected, not a failure */
      }
    };
    return peer;
  };

  return { bridge, deliver, attach, toServer, notices, terminal };
}

beforeEach(() => {
  picker.download = null;
  picker.upload = null;
});

describe('lrzsz through the terminal pipe', () => {
  // The cut is the point of this file. Go batches output by time and size, and a
  // ZMODEM header is ~30 bytes of `**` followed by hex — so the piece that
  // finishes a header contains no asterisk at all. Anything that decides
  // "can't be ZMODEM" from a single chunk tears the handshake apart, and the
  // user sees a transfer that starts and never finishes.
  it('completes a download when the header straddles two output chunks', async () => {
    const saved: Uint8Array[] = [];
    const writable: ZmodemWritable = {
      write: async (bytes) => void saved.push(bytes.slice()),
      close: async () => undefined,
      abort: async () => undefined,
    };
    picker.download = async () => writable;

    const w = wireBridge(7);
    w.deliver(Uint8Array.from(Z.Header.build('ZRQINIT').to_hex()));

    await vi.waitFor(() => {
      expect(joined(w.toServer).length).toBeGreaterThan(0);
    }, { timeout: 4000 });
    const peer = w.attach(new Z.Session.Send(Z.Header.parse_hex(Array.from(joined(w.toServer)))));

    const transfer = await peer.send_offer({
      name: 'backup.tar',
      size: PAYLOAD.length,
      mtime: new Date(),
      files_remaining: 1,
      bytes_remaining: PAYLOAD.length,
    });
    expect(transfer).toBeDefined();
    if (!transfer) throw new Error('the offer never reached us');
    transfer.send(PAYLOAD.slice(0, 4096));
    await transfer.end(PAYLOAD.slice(4096));

    await vi.waitFor(() => {
      expect(joined(saved)).toEqual(PAYLOAD);
    }, { timeout: 8000 });
    w.bridge.dispose();
  });

  // The bar is drawn by this pipe and lands in the terminal through the real
  // bridge, so the width (from `term.cols`) and the delivery path are both
  // exercised here, not just beside it.
  it('draws the progress bar into the terminal', async () => {
    const saved: Uint8Array[] = [];
    const writable: ZmodemWritable = {
      write: async (bytes) => void saved.push(bytes.slice()),
      close: async () => undefined,
      abort: async () => undefined,
    };
    picker.download = async () => writable;

    const w = wireBridge(7);
    w.deliver(Uint8Array.from(Z.Header.build('ZRQINIT').to_hex()));

    await vi.waitFor(() => {
      expect(joined(w.toServer).length).toBeGreaterThan(0);
    }, { timeout: 4000 });
    const peer = w.attach(new Z.Session.Send(Z.Header.parse_hex(Array.from(joined(w.toServer)))));

    const transfer = await peer.send_offer({
      name: 'backup.tar',
      size: PAYLOAD.length,
      mtime: new Date(),
      files_remaining: 1,
      bytes_remaining: PAYLOAD.length,
    });
    if (!transfer) throw new Error('the offer never reached us');
    transfer.send(PAYLOAD.slice(0, 4096));
    await transfer.end(PAYLOAD.slice(4096));
    await vi.waitFor(() => {
      expect(joined(saved)).toEqual(PAYLOAD);
    }, { timeout: 8000 });

    const screen = w.terminal.join('');
    expect(screen).toContain('\x1b[?25l');
    expect(screen).toContain('backup.tar');
    expect(screen).toContain('█');
    expect(screen).toContain('100%');
    expect(screen).toContain('\r\n\x1b[?25h');
    w.bridge.dispose();
  });

  // lrzsz puts `0xff` padding (or `0x8a` on a 7-bit link) in front of its first
  // header to flush whatever the line still owes, and the terminal has to show or
  // swallow that junk without losing the header behind it.
  it('gets through a header that arrives behind padding, in pieces', async () => {
    const saved: Uint8Array[] = [];
    const writable: ZmodemWritable = {
      write: async (bytes) => void saved.push(bytes.slice()),
      close: async () => undefined,
      abort: async () => undefined,
    };
    picker.download = async () => writable;

    const w = wireBridge(5);
    const padding = Uint8Array.from([0x0d, 0x0a, ...new Array(20).fill(0xff), 0x0d, 0x0a]);
    w.deliver(Uint8Array.from([...padding, ...Z.Header.build('ZRQINIT').to_hex()]));

    await vi.waitFor(() => {
      expect(joined(w.toServer).length).toBeGreaterThan(0);
    }, { timeout: 4000 });
    const peer = w.attach(new Z.Session.Send(Z.Header.parse_hex(Array.from(joined(w.toServer)))));

    const transfer = await peer.send_offer({
      name: 'pad.bin',
      size: PAYLOAD.length,
      mtime: new Date(),
      files_remaining: 1,
      bytes_remaining: PAYLOAD.length,
    });
    if (!transfer) throw new Error('the offer never reached us');
    transfer.send(PAYLOAD);
    await transfer.end();

    await vi.waitFor(() => {
      expect(joined(saved)).toEqual(PAYLOAD);
    }, { timeout: 8000 });
    w.bridge.dispose();
  });

  it('completes an upload when the ZRINIT straddles two output chunks', async () => {
    const content = Uint8Array.from(Array.from({ length: 3000 }, (_, i) => (i * 17 + 5) % 256));
    const uploads: ZmodemUpload[] = [
      { name: 'notes.bin', size: content.length, read: async (offset, length) => content.slice(offset, offset + length) },
    ];
    picker.upload = async () => uploads;

    // 3-byte pieces: every header the far side sends arrives in fragments.
    const w = wireBridge(3);
    const peer = w.attach(new Z.Session.Receive());
    const received: Uint8Array[] = [];
    let accepted = false;

    peer.on('offer', ((offer: {
      on(event: string, handler: (octets: number[]) => void): void;
      accept(): Promise<unknown>;
    }) => {
      accepted = true;
      offer.on('input', (octets) => received.push(Uint8Array.from(octets)));
      void offer.accept();
    }) as never);

    peer.start();

    await vi.waitFor(() => {
      expect(accepted).toBe(true);
    }, { timeout: 4000 });
    await vi.waitFor(() => {
      expect(joined(received)).toEqual(content);
    }, { timeout: 8000 });
    w.bridge.dispose();
  });
});
