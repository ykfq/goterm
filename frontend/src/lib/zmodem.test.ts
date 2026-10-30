/*
 * lrzsz compatibility: `sz` and `rz` on a server with no trzsz.
 *
 * Each test connects the pipe to a *real* ZMODEM peer — the other role from the
 * same library, driven through its public session API — across a two-way wire, so
 * what is exercised is everything this app owns: that a ZRQINIT on the screen is
 * picked up and answered, that an offered file lands in the folder the user chose
 * with its bytes in order, that `rz` gets the files the user picked, and that the
 * terminal stops feeding keystrokes into a stream that is carrying a file.
 *
 * What these do NOT prove is zmodem.js's own conformance to the protocol; that is
 * the library's job, and its upstream suite runs against real lrzsz binaries. No
 * frames are hand-written here, so a mistake in our wiring cannot hide behind a
 * matching mistake in a fixture.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import Zmodem from 'zmodem.js';

import { ZmodemPipe, safeFileName, type ZmodemUpload, type ZmodemWritable } from './zmodem';

/** The parts of the peer's session API this suite touches. */
interface Peer {
  consume(bytes: Uint8Array | number[]): void;
  set_sender(handler: (octets: number[]) => void): Peer;
  start(): void;
  close(): Promise<void>;
  send_offer(details: Record<string, unknown>): Promise<PeerTransfer | undefined>;
  on(event: string, handler: (...args: never[]) => void): void;
  aborted(): boolean;
  has_ended(): boolean;
}

interface PeerTransfer {
  send(bytes: Uint8Array): void;
  end(bytes?: Uint8Array): Promise<void>;
}

interface ZmodemInternals {
  Session: { Send: new (zrinit: unknown) => Peer; Receive: new () => Peer };
  Header: {
    build(name: string, ...args: unknown[]): { to_hex(): number[] };
    parse_hex(octets: number[]): unknown;
  };
}

const Z = Zmodem as unknown as ZmodemInternals;

interface WireOptions {
  chooseDownloadTarget?: () => Promise<ZmodemWritable | null>;
  chooseUploads?: () => Promise<ZmodemUpload[] | null>;
}

/**
 * The app's end of the connection plus a two-way wire: everything the pipe sends
 * is handed to the peer, and everything the peer sends is consumed by the pipe.
 */
function wire(options: WireOptions = {}) {
  const toTerminal = vi.fn<(bytes: Uint8Array) => void>();
  const notices = vi.fn<(level: string, message: string) => void>();
  const toServerBytes: Uint8Array[] = [];
  let feedPeer: ((bytes: Uint8Array) => void) | null = null;
  let abortedByPipe = false;

  const pipe = new ZmodemPipe({
    toTerminal,
    toServer: (bytes) => {
      toServerBytes.push(bytes);
      feedPeer?.(bytes);
    },
    chooseDownloadTarget: options.chooseDownloadTarget ?? (async () => null),
    chooseUploads: options.chooseUploads ?? (async () => []),
    onNotice: notices,
  });

  const attach = <P extends Peer>(peer: P): P => {
    peer.set_sender((octets) => {
      pipe.consume(Uint8Array.from(octets));
    });
    feedPeer = (bytes) => {
      // A peer that has been told to go away throws from inside consume(); that
      // is the fake `sz` reacting to our ABORT sequence, not a failure here.
      try {
        peer.consume(Array.from(bytes));
      } catch (error) {
        if (!/abort/i.test(String(error))) throw error;
        abortedByPipe = true;
      }
    };
    return peer;
  };

  const said = (pattern: RegExp): boolean =>
    notices.mock.calls.some((call) => pattern.test(String(call[1])));

  return {
    pipe,
    toTerminal,
    toServerBytes,
    notices,
    attach,
    said,
    abortedByPipe: () => abortedByPipe,
  };
}

/**
 * The sender role, as `sz` on the server plays it.
 *
 * It is constructed from the ZRINIT we actually sent: that header is the
 * handshake, and handing the sender a second copy of it is exactly the case the
 * library rejects with "Unhandled header".
 */
function szPeer(w: ReturnType<typeof wire>): Peer {
  const ours = Array.from(joined(w.toServerBytes));
  // The Sentry confirms and our receiver answers before this is called, so
  // `toServerBytes` holds the ZRINIT and nothing else.
  return new Z.Session.Send(Z.Header.parse_hex(ours));
}

/** A writable that records what lands in it. */
function recordingTarget(failWith?: Error) {
  const chunks: Uint8Array[] = [];
  const state = { closed: false, aborted: false };
  const target: ZmodemWritable = {
    async write(bytes) {
      if (failWith) throw failWith;
      chunks.push(bytes.slice());
    },
    async close() {
      state.closed = true;
    },
    async abort() {
      state.aborted = true;
    },
  };
  return { target, chunks, state };
}

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

/** A payload with bytes that any text handling in between would mangle. */
const PAYLOAD = Uint8Array.from(Array.from({ length: 5000 }, (_, i) => (i * 37 + 11) % 256));

describe('routing', () => {
  it('passes ordinary terminal output straight through', () => {
    const w = wire();
    const bytes = new TextEncoder().encode('user@host:~$ ls /etc\n');

    expect(w.pipe.consume(bytes)).toBe(false);
    expect(w.toTerminal).toHaveBeenCalledWith(bytes);
    expect(w.pipe.active()).toBe(false);
    w.pipe.dispose();
  });

  // Go's output pump batches by time and size, so a ZMODEM header can arrive in
  // pieces — and the piece that *finishes* a header is pure hex, with no asterisk
  // in it. Treating "this chunk has no asterisk" as "cannot be ZMODEM" tears the
  // header apart, and the transfer the user started simply never completes.
  it('finds a header that the output pump split in two', () => {
    const w = wire();
    const zrqinit = Uint8Array.from(Z.Header.build('ZRQINIT').to_hex());

    // The first piece carries `**` plus ZDLE; the rest is all hex.
    expect(w.pipe.consume(zrqinit.slice(0, 4))).toBe(false);
    expect(w.pipe.consume(zrqinit.slice(4))).toBe(true);

    expect(w.pipe.active()).toBe(true);
    expect(joined(w.toServerBytes).length).toBeGreaterThan(0);
    w.pipe.dispose();
  });

  it('answers a ZRQINIT with a ZRINIT and takes the stream over', () => {
    const w = wire();

    // `sz` on the far side: a hex ZRQINIT is the first thing it puts on the wire.
    const zrqinit = Uint8Array.from(Z.Header.build('ZRQINIT').to_hex());
    expect(w.pipe.consume(zrqinit)).toBe(true);

    expect(w.pipe.active()).toBe(true);
    expect(joined(w.toServerBytes).length).toBeGreaterThan(0);
    // The user is told a transfer has begun instead of watching bytes appear.
    expect(w.said(/download/i)).toBe(true);
    w.pipe.dispose();
  });
});

describe('the fast path, and what may switch it off', () => {
  // A stray asterisk is ordinary terminal output: `echo *.txt`, `**kwargs`, a
  // C comment's `*/`, markdown. If seeing one switched the scanner on for good,
  // every later 64 KiB chunk would be copied into the scanner for the life of the
  // tab, for a ZMODEM session that never comes.
  //
  // The fast path is observable by identity: it hands xterm the caller's own
  // buffer, while a chunk that went through the scanner comes back as a copy.
  it('comes back after output that merely contains an asterisk', () => {
    const w = wire();

    // `*` followed by anything but another `*` cannot begin a header, so this
    // chunk is scanned once and that is the end of it.
    w.pipe.consume(new TextEncoder().encode('echo *.txt\n'));

    const next = new TextEncoder().encode(`total 4 ${'x'.repeat(40)}\n`);
    expect(w.pipe.consume(next)).toBe(false);
    const delivered = w.toTerminal.mock.calls[w.toTerminal.mock.calls.length - 1]?.[0] as Uint8Array;
    expect(delivered).toBe(next);
    expect(w.pipe.active()).toBe(false);
    w.pipe.dispose();
  });

  // A `**` at the end of a chunk, the rest of the header in the next one. The
  // scanner cannot bridge this itself: its detection cache keeps the first 21
  // bytes it retained, so the asterisk at the *tail* of a long chunk is gone
  // before the header finishes arriving. This is the shape Go's 64 KiB batching
  // actually produces, and the shape that made `sz`/`rz` start and never finish.
  it('detects a header whose `**` ends a chunk of ordinary output', () => {
    const w = wire();
    const zrqinit = Array.from(Z.Header.build('ZRQINIT').to_hex());
    const filler = new TextEncoder().encode('loading sprites... 42% done, please wait\n\r');

    const first = new Uint8Array([...filler, 0x2a, 0x2a]);
    const rest = Uint8Array.from(zrqinit.slice(2));

    expect(w.pipe.consume(first)).toBe(false);
    expect(w.pipe.consume(rest)).toBe(true);
    expect(w.pipe.active()).toBe(true);
    w.pipe.dispose();
  });

  it('shows a held tail when no header follows it', () => {
    vi.useFakeTimers();
    try {
      const w = wire();
      // `**` really could still become a header, so it waits — briefly. What comes
      // before it cannot, and must not wait with it.
      w.pipe.consume(new TextEncoder().encode('gcc -o a.out **'));
      const early = w.toTerminal.mock.calls.map((call) => new TextDecoder().decode(call[0] as Uint8Array)).join('');
      expect(early).toBe('gcc -o a.out ');

      vi.advanceTimersByTime(200);
      const shown = w.toTerminal.mock.calls.map((call) => new TextDecoder().decode(call[0] as Uint8Array)).join('');
      // Once, in order, nothing lost and nothing doubled.
      expect(shown).toBe('gcc -o a.out **');
      w.pipe.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  // The opposite half of the same rule: a whole header that is already in hand
  // must go to the scanner now. Holding it because it *looks* like a prefix is how
  // a transfer would stall waiting for a chunk that never comes.
  it('does not hold a header that is already complete', () => {
    const w = wire();
    const header = Uint8Array.from(Z.Header.build('ZRQINIT').to_hex());
    expect(header.length).toBe(21);

    expect(w.pipe.consume(header)).toBe(true);
    expect(w.pipe.active()).toBe(true);
    w.pipe.dispose();
  });

  // Bytes the user's server sent are the user's; a scanner that cannot handle them
  // is no excuse to drop them. The real trigger is zmodem.js spreading its input
  // through `Function.prototype.apply`, which gives up somewhere past roughly
  // 131 072 bytes — but "somewhere" depends on how much stack is in use at the
  // time, so the fault is injected rather than provoked, and the big-chunk case is
  // tested separately below for what it can be tested for: nothing is lost.
  it('hands the terminal a chunk the scanner chokes on instead of losing it', () => {
    const w = wire();
    const chunk = new TextEncoder().encode(`a comment line with a * in it ${'a'.repeat(60)}\n`);

    // Fault injection on purpose: this reaches past the class's privacy because the
    // thing being tested is what the pipe does when *someone else's* code throws.
    (w.pipe as unknown as { sentry: { consume(input: Uint8Array): void } }).sentry.consume = () => {
      throw new RangeError('Maximum call stack size exceeded');
    };

    expect(w.pipe.consume(chunk)).toBe(false);
    const delivered = w.toTerminal.mock.calls[w.toTerminal.mock.calls.length - 1]?.[0] as Uint8Array;
    expect(delivered).toBe(chunk);
    expect(w.said(/ZMODEM scan disabled/i)).toBe(true);

    // And it stays out of the way for the rest of this pipe's life.
    const next = new TextEncoder().encode(`still fine ${'z'.repeat(40)}*done`);
    expect(w.pipe.consume(next)).toBe(false);
    expect(w.toTerminal.mock.calls[w.toTerminal.mock.calls.length - 1]?.[0]).toBe(next);
    w.pipe.dispose();
  });

  it('passes a chunk far larger than Go ever sends without losing bytes', () => {
    const w = wire();
    const huge = new Uint8Array(128 * 1024);
    huge.fill(0x41);
    huge[0] = 0x2a; // an asterisk, so the chunk is offered to the scanner

    w.pipe.consume(huge);
    const shown = w.toTerminal.mock.calls.map((call) => call[0] as Uint8Array);
    const total = shown.reduce((sum, bytes) => sum + bytes.length, 0);
    expect(total).toBe(huge.length);
    expect(Uint8Array.from(shown.flatMap((bytes) => Array.from(bytes)))).toEqual(huge);
    w.pipe.dispose();
  });

  it('never shows held bytes twice when the next chunk arrives', () => {
    const w = wire();
    w.pipe.consume(new TextEncoder().encode('make: *** [Makefile:12] Error 2\n'));
    w.pipe.consume(new TextEncoder().encode('make: nothing to be done\n'));
    w.pipe.dispose();

    const shown = w.toTerminal.mock.calls
      .map((call) => new TextDecoder().decode(call[0] as Uint8Array))
      .join('');
    expect(shown).toContain('make: *** [Makefile:12] Error 2\n');
    expect(shown).toContain('make: nothing to be done\n');
    expect(shown.match(/nothing to be done/g)).toHaveLength(1);
    expect(shown.match(/\*\*\*\s\[Makefile:12\]/g)).toHaveLength(1);
  });
});

describe('a session that stops answering', () => {
  // While a session owns the stream the terminal sets keystrokes aside, so a far
  // side that dies mid-handshake would otherwise leave a tab nobody can type in
  // and nothing can revive short of closing it.
  it('ends a silent session and gives the terminal back', () => {
    vi.useFakeTimers();
    try {
      const w = wire();
      w.pipe.consume(Uint8Array.from(Z.Header.build('ZRQINIT').to_hex()));
      expect(w.pipe.active()).toBe(true);

      vi.advanceTimersByTime(45_000 + 50);

      expect(w.pipe.active()).toBe(false);
      expect(w.said(/nothing came from the server/i)).toBe(true);
      w.pipe.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  // The clock is a watchdog, not a deadline: a transfer that is still talking is
  // never cut off, so every chunk has to reset it. Disposing has to take the timer
  // with it, or a closed tab would report a stall on a session it no longer has.
  it('has nothing to say once the pipe is gone', () => {
    vi.useFakeTimers();
    try {
      const w = wire();
      w.pipe.consume(Uint8Array.from(Z.Header.build('ZRQINIT').to_hex()));
      vi.advanceTimersByTime(10_000);
      w.pipe.dispose();
      vi.advanceTimersByTime(120_000);

      expect(w.said(/nothing came from the server/i)).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('receiving (`sz` on the server)', () => {
  it('saves the offered file where the user pointed, byte for byte', async () => {
    const saved = recordingTarget();
    const chooseDownloadTarget = vi.fn(async () => saved.target);
    const w = wire({ chooseDownloadTarget });

    // `sz backup.tar` announces itself; our receiver answers with ZRINIT.
    w.pipe.consume(Uint8Array.from(Z.Header.build('ZRQINIT').to_hex()));
    const peer = w.attach(szPeer(w));

    const transfer = await peer.send_offer({
      name: 'backup.tar',
      size: PAYLOAD.length,
      mtime: new Date(),
      files_remaining: 1,
      bytes_remaining: PAYLOAD.length,
    });
    expect(transfer).toBeDefined();
    if (!transfer) throw new Error('the offer was not accepted');
    transfer.send(PAYLOAD.slice(0, 4096));
    await transfer.end(PAYLOAD.slice(4096));
    await peer.close();

    // `close()` only happens after every packet has been written and flushed, so
    // waiting for it is what makes the byte comparison meaningful.
    await vi.waitFor(() => {
      expect(saved.state.closed).toBe(true);
    });
    expect(chooseDownloadTarget).toHaveBeenCalled();
    expect(joined(saved.chunks)).toEqual(PAYLOAD);
    expect(w.said(/Downloaded backup\.tar/)).toBe(true);
    expect(w.pipe.active()).toBe(false);
    w.pipe.dispose();
  });

  // A known gap, pinned here so it cannot be quietly forgotten.
  //
  // `sz file.tar` does not ask permission: it sends ZFILE and waits for the
  // receiver's ZRINIT. zmodem.js's scanner wakes on ZRINIT/ZRQINIT only, so a bare
  // ZFILE is passed through as text and `sz` waits until it times out. In other
  // words: **`sz` on a host that has only lrzsz does not work**, while `rz` does
  // (there the server leads with ZRINIT, which is why every download test below
  // starts by hand-shaking with ZRQINIT).
  //
  // Answering the ZFILE with a session built by hand was tried and is measurably
  // worse: the session aborts with “CRC check failed!” on the header it was handed,
  // which trades silence for a false accusation. The real fix belongs one layer
  // down — a ZMODEM receiver that can be handed a ZFILE-initiated stream. Until
  // then this test says out loud what is broken, and fails the day it changes.
  it('does not start a session from a bare ZFILE (known gap: `sz` needs trzsz)', () => {
    const w = wire();

    w.pipe.consume(Uint8Array.from(Z.Header.build('ZFILE').to_hex()));

    expect(w.pipe.active()).toBe(false);
    // Not swallowed on the way through, at least: the bytes reach the screen.
    expect(w.toTerminal).toHaveBeenCalled();
    expect(w.said(/ZMODEM/)).toBe(false);
    w.pipe.dispose();
  });

  it('skips the file when the user cancels the dialog', async () => {
    const w = wire({ chooseDownloadTarget: async () => null });
    w.pipe.consume(Uint8Array.from(Z.Header.build('ZRQINIT').to_hex()));
    const peer = w.attach(szPeer(w));

    // A cancelled dialog must resolve the offer as *declined*, so the sender
    // moves on instead of waiting for a receiver that will never answer.
    await expect(
      peer.send_offer({ name: 'nope.bin', size: 4, mtime: new Date(), files_remaining: 1, bytes_remaining: 4 }),
    ).resolves.toBeUndefined();

    w.pipe.dispose();
  });

  it('reports a write failure instead of pretending the file arrived', async () => {
    const saved = recordingTarget(new Error('disk is full'));
    const w = wire({ chooseDownloadTarget: async () => saved.target });
    w.pipe.consume(Uint8Array.from(Z.Header.build('ZRQINIT').to_hex()));
    const peer = w.attach(szPeer(w));

    const transfer = await peer.send_offer({
      name: 'oops.bin',
      size: PAYLOAD.length,
      mtime: new Date(),
      files_remaining: 1,
      bytes_remaining: PAYLOAD.length,
    });
    transfer?.send(PAYLOAD);
    await transfer?.end().catch(() => undefined);

    await vi.waitFor(() => {
      expect(w.said(/disk is full/)).toBe(true);
    });
    expect(saved.state.aborted).toBe(true);
    expect(saved.state.closed).toBe(false);
    // And the sender was told to stop rather than left resending into a file
    // nobody is writing any more.
    await vi.waitFor(() => {
      expect(w.abortedByPipe()).toBe(true);
    });
    w.pipe.dispose();
  });
});

describe('sending (`rz` on the server)', () => {
  it('offers the files the user picked and streams their bytes', async () => {
    const first = Uint8Array.from([1, 2, 3, 4, 5]);
    const uploads: ZmodemUpload[] = [
      { name: 'notes.txt', size: first.length, read: async (offset, length) => first.slice(offset, offset + length) },
    ];
    const w = wire({ chooseUploads: async () => uploads });

    // The far side: a real receiver (`rz`), which starts the session with ZRINIT.
    const peer = w.attach(new Z.Session.Receive());
    const received: number[] = [];
    let offeredName = '';
    let accepted = false;

    peer.on('offer', ((offer: {
      get_details(): { name: string };
      on(event: string, handler: (octets: number[]) => void): void;
      accept(): Promise<void>;
    }) => {
      offeredName = offer.get_details().name;
      accepted = true;
      offer.on('input', (octets) => {
        received.push(...octets);
      });
      void offer.accept();
    }) as never);

    peer.start();

    await vi.waitFor(() => {
      expect(accepted).toBe(true);
    });
    await vi.waitFor(() => {
      expect(Uint8Array.from(received)).toEqual(first);
    });
    expect(offeredName).toBe('notes.txt');
    expect(w.said(/upload started/i)).toBe(true);
    w.pipe.dispose();
  });

  it('closes the session politely when the user picks nothing', async () => {
    const w = wire({ chooseUploads: async () => [] });
    const peer = w.attach(new Z.Session.Receive());
    let ended = false;
    peer.on('session_end', (() => {
      ended = true;
    }) as never);

    peer.start();

    await vi.waitFor(() => {
      expect(ended).toBe(true);
    });
    expect(w.said(/cancelled/i)).toBe(true);
    expect(w.pipe.active()).toBe(false);
    w.pipe.dispose();
  });
});

/*
 * The progress line, and why it lives in the terminal rather than in a widget.
 *
 * `rz` and `sz` draw their spinner on the *server's* tty, so this window sees
 * nothing at all while a large file moves. So the pipe draws the bar `trz`/`tsz`
 * draw — same shape, same fields, same place (the terminal grid) — driven by the
 * bytes it moves. These tests read what it actually writes: the cursor off a
 * `[████░░░░]` line carrying the name, the percentage, and at the end `\r\n` with
 * the cursor back.
 */
describe('progress while a file moves', () => {
  /** Everything the pipe has shown the terminal so far. */
  function shown(w: ReturnType<typeof wire>): string {
    return new TextDecoder().decode(joined(w.toTerminal.mock.calls.map((call) => call[0] as Uint8Array)));
  }

  it('draws a bar for a download and ends the line when it is done', async () => {
    const saved = recordingTarget();
    const w = wire({ chooseDownloadTarget: async () => saved.target });

    w.pipe.consume(Uint8Array.from(Z.Header.build('ZRQINIT').to_hex()));
    const peer = w.attach(szPeer(w));
    const transfer = await peer.send_offer({
      name: 'backup.tar',
      size: PAYLOAD.length,
      mtime: new Date(),
      files_remaining: 1,
      bytes_remaining: PAYLOAD.length,
    });
    if (!transfer) throw new Error('the offer was not accepted');

    // The bar is up before the first packet: a large file spends seconds in the
    // handshake, and an empty screen reads as "nothing is happening".
    const started = shown(w);
    expect(started).toContain('\x1b[?25l');
    expect(started).toContain('backup.tar');
    expect(started).toContain('\x1b[36m');
    expect(started).toContain('[');

    transfer.send(PAYLOAD.slice(0, 4096));
    await transfer.end(PAYLOAD.slice(4096));
    await peer.close();
    await vi.waitFor(() => {
      expect(saved.state.closed).toBe(true);
    });

    const finished = shown(w);
    expect(finished).toContain('█');
    expect(finished).toContain('░');
    expect(finished).toContain('100%');
    // A finished transfer gives the cursor back and leaves the line ending.
    expect(finished).toContain('\r\n\x1b[?25h');
    w.pipe.dispose();
  });

  it('numbers the files of a multi-file upload', async () => {
    const first = Uint8Array.from([1, 2, 3, 4]);
    const second = Uint8Array.from([5, 6]);
    const uploads: ZmodemUpload[] = [
      { name: 'a.bin', size: first.length, read: async (offset, length) => first.slice(offset, offset + length) },
      { name: 'b.bin', size: second.length, read: async (offset, length) => second.slice(offset, offset + length) },
    ];
    const w = wire({ chooseUploads: async () => uploads });

    const peer = w.attach(new Z.Session.Receive());
    peer.on('offer', ((offer: {
      on(event: string, handler: (octets: number[]) => void): void;
      accept(): Promise<void>;
    }) => {
      offer.on('input', () => undefined);
      void offer.accept();
    }) as never);
    peer.start();

    await vi.waitFor(() => {
      expect(w.said(/Sent 2 files/)).toBe(true);
    });

    const screen = shown(w);
    expect(screen).toContain('(1/2) a.bin');
    expect(screen).toContain('(2/2) b.bin');
    expect(screen).toContain('\x1b[?25h');
    w.pipe.dispose();
  });

  it('draws nothing at all when the user cancels the picker', async () => {
    const w = wire({ chooseUploads: async () => [] });
    const peer = w.attach(new Z.Session.Receive());
    let ended = false;
    peer.on('session_end', (() => {
      ended = true;
    }) as never);
    peer.start();

    await vi.waitFor(() => {
      expect(ended).toBe(true);
    });
    // Nothing moved, so no bar — not even the cursor being taken away.
    expect(shown(w)).not.toContain('\x1b[?25l');
    w.pipe.dispose();
  });
});

describe('file names from the wire', () => {
  it('cannot escape the folder the user chose', () => {
    expect(safeFileName('../../etc/passwd')).toBe('passwd');
    expect(safeFileName('C:\\Windows\\system32\\config.sam')).toBe('config.sam');
    expect(safeFileName('..')).toBe('download.bin');
    expect(safeFileName('')).toBe('download.bin');
    expect(safeFileName('report Final.pdf')).toBe('report Final.pdf');
    // Control bytes and shell-hostile punctuation are flattened, not passed on.
    expect(safeFileName('a\x00b|c?d')).toBe('a_b_c_d');
  });
});

/**
 * `rz` on a real server, where a person has to pick the file.
 *
 * That wait is the bug. zmodem.js runs a five-second "keepalive" that sends a
 * ZSINIT and leaves `{ZACK}` as the only header it will accept; lrzsz's `rz`
 * spends the same seconds polling back with a ZRPOS at the offset it is waiting
 * for; the library throws `Unhandled header: ZRPOS` out of `consume()`. Before the
 * fix, that error printed "ZMODEM scan disabled", left the session *active* (so
 * keystrokes were still swallowed) and never answered `rz` — the tab looked hung.
 */
describe('a receiver that polls while the picker is open', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  /** Run promise chains *and* timers until `done()`: fake timers need both. */
  async function settle(done: () => boolean): Promise<void> {
    for (let step = 0; step < 200 && !done(); step += 1) await vi.advanceTimersByTimeAsync(5);
  }

  function pickerFor(files: ZmodemUpload[]) {
    const state: { release: (() => void) | null } = { release: null };
    const chooseUploads = () =>
      new Promise<ZmodemUpload[] | null>((resolve) => {
        state.release = () => resolve(files);
      });
    return { state, chooseUploads };
  }

  function acceptOffers(peer: Peer): number[] {
    const received: number[] = [];
    peer.on('offer', ((offer: {
      on(event: string, handler: (octets: number[]) => void): void;
      accept(): Promise<void>;
    }) => {
      offer.on('input', (octets) => {
        received.push(...octets);
      });
      void offer.accept();
    }) as never);
    return received;
  }

  /** The library's own way of putting a header on the wire, as `rz` would. */
  function poke(peer: Peer, name: string, offset: number): void {
    (peer as unknown as { _send_header(name: string, offset: number): void })._send_header(name, offset);
  }

  it('still uploads the file the user ends up picking', async () => {
    const first = Uint8Array.from([9, 8, 7, 6, 5]);
    const picker = pickerFor([
      { name: 'a.bin', size: first.length, read: async (offset, length) => first.slice(offset, offset + length) },
    ]);
    const w = wire({ chooseUploads: picker.chooseUploads });
    const peer = w.attach(new Z.Session.Receive());
    const received = acceptOffers(peer);
    peer.start();

    // Long enough for the keepalive's ZSINIT to take over the expected header.
    await vi.advanceTimersByTimeAsync(6_000);
    expect(picker.state.release).not.toBeNull();

    // `rz`, still waiting, pokes at us.
    poke(peer, 'ZRPOS', 0);

    expect(w.said(/scan disabled/i)).toBe(false);
    expect(w.pipe.active()).toBe(true);

    const release = picker.state.release;
    if (!release) throw new Error('the picker was never opened');
    release();

    await settle(() => received.length >= first.length);
    expect(Uint8Array.from(received)).toEqual(first);
    w.pipe.dispose();
  });

  it('gives the terminal back when the far side sends something unusable', async () => {
    const picker = pickerFor([]);
    const w = wire({ chooseUploads: picker.chooseUploads });
    const peer = w.attach(new Z.Session.Receive());
    peer.start();
    await vi.advanceTimersByTimeAsync(6_000);

    // A ZFILE means nothing to us while we are the sender. The library throws;
    // the pipe has to end the session rather than keep holding the keyboard.
    poke(peer, 'ZFILE', 0);

    expect(w.said(/ZMODEM ended/i)).toBe(true);
    expect(w.said(/scan disabled/i)).toBe(false);
    expect(w.pipe.active()).toBe(false);
    w.pipe.dispose();
  });
});
