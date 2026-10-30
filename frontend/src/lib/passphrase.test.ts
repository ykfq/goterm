/*
 * The encrypted-key passphrase round trip.
 *
 * The bug this covers: the form had one "secret" box whose value went to the
 * wrong field for key auth, so an encrypted key never got its passphrase and
 * every connection failed with "private key is encrypted". The fix moves the
 * ask to a prompt at connect time, and this file pins the JS half of that
 * contract — Go's wording is pinned in internal/sshx/passphrase_test.go.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  answerSecretPrompt,
  connectWithPassphrasePrompt,
  needsPassphrase,
  secretPrompt,
  wasCancelled,
} from './store';

const ENCRYPTED = 'passphrase required: private key C:\\Users\\me\\.ssh\\id_ed25519 is encrypted';
const WRONG = 'passphrase required: wrong passphrase for private key C:\\keys\\id_ed25519';
const PROXY_ENCRYPTED = 'passphrase required (proxy): private key C:\\keys\\jump is encrypted';
// A target key that happens to be named after a proxy. Nothing but Go's marker
// may decide which field the answer goes to.
const TARGET_NAMED_PROXY = 'passphrase required: private key C:\\keys\\proxy.pem is encrypted';

/** Lets the rejection from `attempt` reach the prompt before we answer it. */
async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

afterEach(() => {
  if (secretPrompt.open) answerSecretPrompt(null);
});

describe('needsPassphrase', () => {
  it('recognises the wording Go uses', () => {
    expect(needsPassphrase(new Error(ENCRYPTED))).toBe(true);
    expect(needsPassphrase(new Error(WRONG))).toBe(true);
    expect(needsPassphrase(new Error(PROXY_ENCRYPTED))).toBe(true);
  });

  it('leaves every other failure alone', () => {
    expect(needsPassphrase(new Error('connection refused by 10.0.0.8:22'))).toBe(false);
    expect(needsPassphrase(new Error('authentication failed for bob@h (offered password)'))).toBe(false);
    // A host key change must never be answered with a passphrase prompt.
    expect(needsPassphrase(new Error('host key for 10.0.0.8:22 has changed'))).toBe(false);
  });
});

describe('connectWithPassphrasePrompt', () => {
  it('asks, then retries with what the user typed', async () => {
    const attempt = vi
      .fn<(passphrase: string, proxyPassphrase: string) => Promise<string>>()
      .mockRejectedValueOnce(new Error(ENCRYPTED))
      .mockResolvedValueOnce('s_42');

    const result = connectWithPassphrasePrompt(attempt, {
      title: 'Key passphrase',
      hint: 'The private key for bob@server is encrypted.',
    });

    await settle();
    expect(secretPrompt.open).toBe(true);
    expect(secretPrompt.title).toBe('Key passphrase');
    expect(secretPrompt.hint).toContain('encrypted');
    // Quick connect has nowhere to remember a passphrase.
    expect(secretPrompt.rememberable).toBe(false);
    // The first attempt must not have blocked on a prompt that was never needed.
    expect(attempt).toHaveBeenCalledTimes(1);

    answerSecretPrompt({ secret: 'hunter2', remember: false });
    await expect(result).resolves.toBe('s_42');
    expect(secretPrompt.open).toBe(false);
    expect(attempt).toHaveBeenNthCalledWith(2, 'hunter2', '', '');
  });

  it('says so when the passphrase was wrong, instead of starting fresh', async () => {
    const attempt = vi
      .fn<(passphrase: string, proxyPassphrase: string) => Promise<string>>()
      .mockRejectedValueOnce(new Error(ENCRYPTED))
      .mockRejectedValueOnce(new Error(WRONG))
      .mockResolvedValueOnce('s_7');

    const result = connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted' });
    const settled = result.catch((error: unknown) => error);

    await settle();
    // The first ask is a question, not an accusation.
    expect(secretPrompt.error).toBe('');
    answerSecretPrompt({ secret: 'typo', remember: false });

    await settle();
    expect(secretPrompt.open).toBe(true);
    expect(secretPrompt.error).toContain('wrong passphrase');

    answerSecretPrompt({ secret: 'hunter2', remember: false });
    expect(await settled).toBe('s_7');
  });

  it('attributes a proxy key to the jump host', async () => {
    const attempt = vi.fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>().mockRejectedValueOnce(new Error(PROXY_ENCRYPTED)).mockResolvedValueOnce('s_9');

    const result = connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted' });

    await settle();
    expect(secretPrompt.title).toBe('Jump-host key passphrase');

    answerSecretPrompt({ secret: 'jump-secret', remember: false });
    await expect(result).resolves.toBe('s_9');
    // Second argument is the proxy passphrase; the target stays untouched.
    expect(attempt).toHaveBeenNthCalledWith(2, '', 'jump-secret', '');
  });

  // The bug this pins: the jump host was guessed from the word "proxy" appearing
  // anywhere in the message, so a target key called proxy.pem had its passphrase
  // sent in the jump host's field. Both ends then failed with no usable prompt.
  it('does not blame a target key named proxy.pem on the jump host', async () => {
    const attempt = vi.fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>().mockRejectedValueOnce(new Error(TARGET_NAMED_PROXY)).mockResolvedValueOnce('s_11');

    const result = connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted' });
    await settle();
    expect(secretPrompt.title).toBe('Key passphrase');

    answerSecretPrompt({ secret: 'the-targets-own', remember: false });
    await expect(result).resolves.toBe('s_11');
    expect(attempt).toHaveBeenNthCalledWith(2, 'the-targets-own', '', '');
  });

  // Even the marker's own shape must not be satisfiable by a directory name: the
  // whole phrase has to match, so the file path can never supply the routing hint.
  it('does not let a folder named (proxy) route the answer to the jump host', async () => {
    const attempt = vi.fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>().mockRejectedValueOnce(new Error('passphrase required: private key C:\\keys\\(proxy)\\id_ed25519 is encrypted')).mockResolvedValueOnce('s_12');

    const result = connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted' });
    await settle();
    expect(secretPrompt.title).toBe('Key passphrase');

    answerSecretPrompt({ secret: 'targets-own', remember: false });
    await expect(result).resolves.toBe('s_12');
    expect(attempt).toHaveBeenNthCalledWith(2, 'targets-own', '', '');
  });

  it('treats cancelling as a cancellation, not a failure', async () => {
    const attempt = vi.fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>().mockRejectedValueOnce(new Error(ENCRYPTED));

    const result = connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted' });
    const settled = result.catch((error: unknown) => error);
    await settle();
    answerSecretPrompt(null);

    expect(wasCancelled(await settled)).toBe(true);
    expect(attempt).toHaveBeenCalledTimes(1);
  });

  it('does not loop forever on a key that will not unlock', async () => {
    const attempt = vi.fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>().mockRejectedValue(new Error(ENCRYPTED));

    const result = connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted' });
    const settled = result.catch((error: unknown) => error);
    for (let round = 0; round < 6; round += 1) {
      await settle();
      if (!secretPrompt.open) break;
      answerSecretPrompt({ secret: 'nope', remember: false });
    }

    expect(String(await settled)).toMatch(/still did not decrypt/);
    expect(secretPrompt.open).toBe(false);
    // Four asks is plenty; the fifth would be a loop the user cannot escape.
    expect(attempt).toHaveBeenCalledTimes(4);
  });

  it('passes straight through when no passphrase is needed', async () => {
    const attempt = vi.fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>().mockResolvedValue('s_1');

    await expect(connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted' })).resolves.toBe('s_1');
    expect(secretPrompt.open).toBe(false);
    expect(attempt).toHaveBeenCalledWith('', '', '');
  });

  it('offers "remember" only when there is a saved host to remember it on', async () => {
    const attempt = vi.fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>().mockRejectedValueOnce(new Error(ENCRYPTED)).mockResolvedValue('s_2');

    const result = connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted', hostId: 'h_1' });
    await settle();
    expect(secretPrompt.rememberable).toBe(true);
    answerSecretPrompt({ secret: 'hunter2', remember: false });
    await expect(result).resolves.toBe('s_2');
  });
});
