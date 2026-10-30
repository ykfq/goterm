/*
 * Types for zmodem.js (https://github.com/FGasper/zmodemjs), which ships no
 * .d.ts and is CommonJS — `import Zmodem from 'zmodem.js'` gives the whole
 * namespace object.
 *
 * Only the surface this app touches is described: the Sentry (which scans a
 * byte stream for the start of a ZMODEM session) and the two session roles.
 * Byte arrays are plain `number[]` here because that is what the library uses
 * in and out.
 */
declare module 'zmodem.js' {
  /** What the remote offered (download) or what we offer (upload). */
  export interface ZmodemFileDetails {
    name: string;
    size: number;
    mtime: Date;
    mode?: number;
    serial?: number;
    files_remaining?: number;
    bytes_remaining?: number;
  }

  /** The receiver's side of one offered file. */
  export interface ZmodemOffer {
    get_details(): ZmodemFileDetails;
    /** Resolve when the whole file has arrived. */
    accept(): Promise<void>;
    refuse(): void;
    skip(): void;
    on(event: 'input', handler: (octets: number[]) => void): void;
    on(event: 'complete', handler: () => void): void;
  }

  /** The sender's side of one file. */
  export interface ZmodemTransfer {
    send(bytes: Uint8Array): void;
    /** Resolve when the receiver has acknowledged the last packet. */
    end(bytes?: Uint8Array): Promise<void>;
    get_offset(): number;
  }

  export interface ZmodemDetection {
    /** `receive` when the remote wants to send us files (`sz`), `send` for `rz`. */
    get_session_role(): 'receive' | 'send';
    confirm(): ZmodemSession;
    deny(): void;
    is_valid(): boolean;
  }

  export interface ZmodemSession {
    type: 'receive' | 'send';
    start(): void;
    close(): Promise<void>;
    abort(): void;
    aborted(): boolean;
    has_ended(): boolean;
    send_offer(details: ZmodemFileDetails): Promise<ZmodemTransfer | undefined>;
    on(event: 'offer', handler: (offer: ZmodemOffer) => void): void;
    on(event: 'session_end', handler: (reason?: unknown) => void): void;
    on(event: string, handler: (...args: unknown[]) => void): void;
  }

  export interface ZmodemSentryOptions {
    /** Bytes that are not ZMODEM traffic — they belong on the screen. */
    to_terminal(octets: number[]): void;
    /** Bytes the protocol wants the remote to have. */
    sender(octets: number[]): void;
    on_detect(detection: ZmodemDetection): void;
    on_retract(): void;
  }

  export interface ZmodemSentry {
    consume(input: Uint8Array | number[]): void;
    get_confirmed_session(): ZmodemSession | null;
  }

  export interface ZmodemSentryConstructor {
    new (options: ZmodemSentryOptions): ZmodemSentry;
  }

  export interface ZmodemNamespace {
    Sentry: ZmodemSentryConstructor;
  }

  const Zmodem: ZmodemNamespace;
  export default Zmodem;
}
