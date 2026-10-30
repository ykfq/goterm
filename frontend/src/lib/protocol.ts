/*
 * The wire contract with internal/sshx. Keep in step with the Go constants in
 * internal/sshx/manager.go — these strings are the only coupling between the
 * two sides' event names.
 */

export const EVENT_DATA = 'ssh:data';
export const EVENT_CLOSED = 'ssh:closed';
export const EVENT_NOTICE = 'ssh:notice';

export interface DataEvent {
  sessionId: string;
  /** base64 of the raw PTY bytes. */
  data: string;
}

export interface SessionInfo {
  id: string;
  target: string;
  host: string;
  port: number;
  user: string;
  serverVersion: string;
  fingerprint: string;
  cols: number;
  rows: number;
  createdAt: unknown;
  status: string;
  bytesIn: number;
  bytesOut: number;
  closeReason?: string;
  /** Jump host this session tunnels through, absent when direct. */
  via?: string;
  /** SSH keepalive interval in seconds; 0 or absent means keepalives are off. */
  keepAliveSeconds?: number;
}

export interface ClosedEvent {
  sessionId: string;
  reason: string;
  session: SessionInfo;
}

export type NoticeLevel = 'info' | 'warn' | 'error';

/** What the UI may offer the user in response to a notice. */
export type NoticeAction = 'enable-anti-idle';

export interface NoticeEvent {
  sessionId?: string;
  level: NoticeLevel;
  message: string;
  /** An action name the UI knows how to render; anything else is ignored. */
  action?: string;
}

export interface AppInfo {
  version: string;
  goVersion: string;
  platform: string;
  configDir: string;
  secretBackend: string;
  secretSecure: boolean;
  openSessions: number;
  uptimeSeconds: number;
}

/**
 * Where GoTerm keeps its files. `dir` is what this process uses; `pendingDir` is
 * what the next start will use, so a change can be shown as pending instead of
 * pretending the move already happened.
 */
export interface StorageInfo {
  dir: string;
  defaultDir: string;
  configuredDir: string;
  pendingDir: string;
  envDir: string;
  envOverride: boolean;
  restartPending: boolean;
}

export interface HostKeyRecord {
  address: string;
  type: string;
  fingerprint: string;
  addedAt: unknown;
  seenAt: unknown;
}

export type AuthType = 'password' | 'key' | 'agent';

export interface Host {
  id: string;
  name: string;
  group: string;
  host: string;
  port: number;
  user: string;
  authType: AuthType | '';
  keyPath: string;
  colour: string;
  notes: string;
  hasSecret: boolean;
  /** Password + one-time code: the code is asked for at connect time, never stored. */
  twoFactor?: boolean;

  /** Jump host, when this host is reached through one. */
  proxyHost?: string;
  proxyPort?: number;
  proxyUser?: string;
  proxyAuthType?: AuthType | '';
  proxyKeyPath?: string;
  hasProxySecret?: boolean;
  createdAt: unknown;
  updatedAt: unknown;
  lastUsed?: unknown;
}

/** What `ExportHosts` reports; an empty path means the dialog was cancelled. */
export interface HostExport {
  path: string;
  hosts: number;
  secrets: number;
  /** Secrets the key ring would not hand over — a partial export, said out loud. */
  missing: number;
}

/** What `ImportHosts` reports; an empty path means the dialog was cancelled. */
export interface HostImport {
  path: string;
  added: number;
  updated: number;
  skipped: number;
  secrets: number;
}

export interface SSHConfig {
  host: string;
  port: number;
  user: string;
  authType: AuthType | '';
  password: string;
  keyPath: string;
  passphrase: string;
  cols: number;
  rows: number;
  term: string;
  hostKeyPolicy: string;
  /** Jump host to tunnel through; omitted when connecting directly. */
  proxy?: ProxyConfig;
}

/** A jump host: dialled first, then the target is reached through it. */
export interface ProxyConfig {
  host: string;
  port: number;
  user: string;
  authType: AuthType | '';
  password?: string;
  keyPath?: string;
  passphrase?: string;
  hostKeyPolicy?: string;
}
