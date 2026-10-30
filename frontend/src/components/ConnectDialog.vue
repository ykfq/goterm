<script setup lang="ts">
import { Eye, EyeOff, FileKey2, Plug, Trash2, X } from 'lucide-vue-next';
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue';

import { backend, errorMessage } from '../lib/backend';
import { connectAdhoc, connectHost, loadHosts, pushNotice } from '../lib/store';
import type { AuthType, Host } from '../lib/protocol';

const props = defineProps<{ mode: 'new' | 'edit' | 'adhoc'; host?: Host }>();
const emit = defineEmits<{
  (event: 'close'): void;
  (event: 'saved', host: Host): void;
}>();

const form = reactive({
  name: '',
  group: '',
  host: '',
  port: 22,
  user: '',
  authType: 'password' as AuthType,
  password: '',
  keyPath: '',
  // A bastion-style login: password + one-time code appended at connect time.
  twoFactor: false,
  colour: '',
  notes: '',
  // Jump host (SSH proxy). Kept flat to match the saved-host fields.
  proxyEnabled: false,
  proxyHost: '',
  proxyPort: 22,
  proxyUser: '',
  proxyAuthType: 'password' as AuthType,
  proxyKeyPath: '',
  proxyPassword: '',
});
const showSecret = ref(false);
const hostInput = ref<HTMLInputElement | null>(null);

// The HTML autofocus attribute is not honoured for inputs that a v-if mounts
// later (the dialog was opened after the page finished parsing), which left the
// caret nowhere and the first keystroke going to whatever was focused before.
function focusFirstField(): void {
  // Focus only: selecting the text would replace it on the first keystroke,
  // which is wrong when editing a host that already has an address.
  void nextTick(() => hostInput.value?.focus());
}

onMounted(focusFirstField);
const keepSecret = ref(true);
const forgotSecret = ref(false);
const busy = ref(false);
const error = ref('');

// A one-time code only means something next to a password. Leaving the flag set
// while its checkbox is hidden (because the box is only drawn for password
// auth) would block Save with a message about a box the user cannot see.
watch(
  () => form.authType,
  (kind) => {
    if (kind !== 'password') form.twoFactor = false;
  },
);

watch(
  () => [props.mode, props.host?.id] as const,
  () => {
    error.value = '';
    showSecret.value = false;
    keepSecret.value = true;
    forgotSecret.value = false;
    focusFirstField();
    const host = props.host;
    if (!host) {
      Object.assign(form, {
        name: '', group: '', host: '', port: 22, user: '', authType: 'password', password: '', keyPath: '',
        twoFactor: false, colour: '', notes: '', proxyEnabled: false, proxyHost: '', proxyPort: 22, proxyUser: '',
        proxyAuthType: 'password', proxyKeyPath: '', proxyPassword: '',
      });
      return;
    }
    Object.assign(form, {
      name: host.name,
      group: host.group,
      host: host.host,
      port: host.port || 22,
      user: host.user,
      authType: (host.authType || 'password') as AuthType,
      // The stored secret is deliberately unreachable from here: an empty box
      // means "leave it as it is", and Go never sends the old value out.
      password: '',
      keyPath: host.keyPath,
      twoFactor: Boolean(host.twoFactor),
      colour: host.colour,
      notes: host.notes,
      proxyEnabled: Boolean(host.proxyHost),
      proxyHost: host.proxyHost ?? '',
      proxyPort: host.proxyPort || 22,
      proxyUser: host.proxyUser ?? '',
      proxyAuthType: (host.proxyAuthType || 'password') as AuthType,
      proxyKeyPath: host.proxyKeyPath ?? '',
      proxyPassword: '',
    });
  },
  { immediate: true },
);

const needsSecret = computed(() => form.authType === 'password');
const editing = computed(() => props.mode === 'edit' && !!props.host);
const adhoc = computed(() => props.mode === 'adhoc');
const secretPlaceholder = computed(() =>
  editing.value && needsSecret.value && props.host?.hasSecret ? 'saved — leave blank to keep' : '',
);
const savedPassphrase = computed(() => editing.value && !forgotSecret.value && Boolean(props.host?.hasSecret));
const proxySecretPlaceholder = computed(() =>
  editing.value && form.proxyAuthType === 'password' && props.host?.hasProxySecret ? 'saved — leave blank to keep' : '',
);

function valid(): string {
  if (!form.host.trim()) return 'Enter a host address.';
  if (!form.user.trim()) return 'Enter a username.';
  if (!Number.isInteger(form.port) || form.port < 1 || form.port > 65535) return 'Port must be between 1 and 65535.';
  if (form.authType === 'key' && !form.keyPath.trim()) return 'Key authentication needs a private key file.';
  if (form.twoFactor && form.authType !== 'password') {
    return 'Two-factor authentication appends the code to the password — switch to password authentication, or uncheck it.';
  }
  if (form.authType === 'password' && needsSecret.value && !form.password && !(editing.value && props.host?.hasSecret)) {
    return 'Enter the password (or switch to key authentication).';
  }
  if (form.proxyEnabled) {
    if (!form.proxyHost.trim()) return 'Enter the jump host address.';
    if (!form.proxyUser.trim()) return 'Enter the jump host username.';
    if (!Number.isInteger(form.proxyPort) || form.proxyPort < 1 || form.proxyPort > 65535) {
      return 'Jump host port must be between 1 and 65535.';
    }
    if (form.proxyAuthType === 'key' && !form.proxyKeyPath.trim()) return 'Jump host key authentication needs a private key file.';
    if (
      form.proxyAuthType === 'password' &&
      !form.proxyPassword &&
      !(editing.value && props.host?.hasProxySecret)
    ) {
      return 'Enter the jump host password.';
    }
  }
  return '';
}

async function pickKey(): Promise<void> {
  try {
    const path = await backend.pickKeyFile();
    if (path) form.keyPath = path;
  } catch (err) {
    error.value = errorMessage(err);
  }
}

async function pickProxyKey(): Promise<void> {
  try {
    const path = await backend.pickKeyFile();
    if (path) form.proxyKeyPath = path;
  } catch (err) {
    error.value = errorMessage(err);
  }
}

/** Drops a stored passphrase; the next connect will ask for it again. */
async function forgetPassphrase(): Promise<void> {
  if (!props.host) return;
  busy.value = true;
  try {
    await backend.setHostSecret(props.host.id, '');
    forgotSecret.value = true;
    pushNotice('info', 'Removed the saved passphrase for this host');
    await loadHosts();
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    busy.value = false;
  }
}

async function persist(): Promise<Host | null> {
  const saved = await backend.saveHost({
    id: props.host?.id ?? '',
    name: form.name.trim(),
    group: form.group.trim(),
    host: form.host.trim(),
    port: form.port,
    user: form.user.trim(),
    authType: form.authType,
    keyPath: form.keyPath.trim(),
    // A code is appended to a password, so the flag is meaningless for key and
    // agent auth; saving it anyway would make the connect prompt ask for a code
    // that Go has nowhere to put.
    twoFactor: form.authType === 'password' && form.twoFactor,
    colour: form.colour.trim(),
    notes: form.notes,
    // An unchecked jump host is saved as no jump host at all.
    proxyHost: form.proxyEnabled ? form.proxyHost.trim() : '',
    proxyPort: form.proxyEnabled ? form.proxyPort : 0,
    proxyUser: form.proxyEnabled ? form.proxyUser.trim() : '',
    proxyAuthType: form.proxyEnabled ? form.proxyAuthType : '',
    proxyKeyPath: form.proxyEnabled ? form.proxyKeyPath.trim() : '',
    hasSecret: props.host?.hasSecret ?? false,
    createdAt: props.host?.createdAt ?? null,
    updatedAt: props.host?.updatedAt ?? null,
  } as Host);

  // Only touch the secret when the user typed one, or asked to clear it. Key
  // passphrases are not typed here any more: they are collected by the prompt
  // at connect time, which also decides whether to remember them.
  if (form.authType === 'password') {
    if (keepSecret.value && form.password) await backend.setHostSecret(saved.id, form.password);
    else if (!keepSecret.value) await backend.setHostSecret(saved.id, '');
  }

  if (!form.proxyEnabled) await backend.setHostProxySecret(saved.id, '');
  else if (form.proxyPassword) await backend.setHostProxySecret(saved.id, form.proxyPassword);
  return saved;
}

async function save(): Promise<void> {
  const problem = valid();
  if (problem) {
    error.value = problem;
    return;
  }
  busy.value = true;
  error.value = '';
  try {
    const saved = await persist();
    await loadHosts();
    if (saved) emit('saved', saved);
    pushNotice('info', `Saved ${saved?.name || saved?.host}`);
    emit('close');
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    busy.value = false;
  }
}

async function saveAndConnect(): Promise<void> {
  const problem = valid();
  if (problem) {
    error.value = problem;
    return;
  }
  busy.value = true;
  error.value = '';
  try {
    const saved = await persist();
    await loadHosts();
    if (saved) {
      emit('saved', saved);
      await connectHost(saved.id);
    }
    emit('close');
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    busy.value = false;
  }
}

async function connectWithoutSaving(): Promise<void> {
  const problem = valid();
  if (problem) {
    error.value = problem;
    return;
  }
  busy.value = true;
  error.value = '';
  try {
    const ok = await connectAdhoc({
      host: form.host.trim(),
      port: form.port,
      user: form.user.trim(),
      authType: form.authType,
      password: form.password,
      keyPath: form.keyPath.trim(),
      twoFactor: form.authType === 'password' && form.twoFactor,
      proxy: form.proxyEnabled
        ? {
            host: form.proxyHost.trim(),
            port: form.proxyPort,
            user: form.proxyUser.trim(),
            authType: form.proxyAuthType,
            password: form.proxyAuthType === 'password' ? form.proxyPassword : '',
            keyPath: form.proxyAuthType === 'key' ? form.proxyKeyPath.trim() : '',
            hostKeyPolicy: 'tofu',
          }
        : undefined,
    });
    if (ok) emit('close');
  } finally {
    busy.value = false;
  }
}

async function remove(): Promise<void> {
  if (!props.host) return;
  busy.value = true;
  try {
    await backend.deleteHost(props.host.id);
    await loadHosts();
    pushNotice('info', `Deleted ${props.host.name || props.host.host}`);
    emit('close');
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    busy.value = false;
  }
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
    event.preventDefault();
    void (adhoc.value ? connectWithoutSaving() : editing.value ? save() : saveAndConnect());
  }
}
</script>

<template>
  <div class="fixed inset-0 z-40 flex items-start justify-center bg-black/45 p-6 pt-16" @click.self="emit('close')" @keydown="onKeydown">
    <div class="w-[560px] overflow-hidden rounded-xl border border-[var(--border)] bg-[var(--panel)] shadow-2xl">
      <header class="flex items-center gap-2 border-b border-[var(--border)] px-4 py-3">
        <Plug :size="15" class="text-[var(--accent)]" />
        <h2 class="text-[13px] font-semibold">
          {{ adhoc ? 'Quick connect' : editing ? 'Edit host' : 'New host' }}
        </h2>
        <button class="focus-ring ml-auto rounded p-1 text-[var(--text-dim)] hover:text-[var(--text)]" @click="emit('close')">
          <X :size="14" />
        </button>
      </header>

      <form class="grid grid-cols-2 gap-3 px-4 py-4" @submit.prevent="adhoc ? connectWithoutSaving() : saveAndConnect()">
        <label v-if="!adhoc" class="col-span-2 flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
          Name
          <input v-model="form.name" class="focus-ring rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 text-[13px] text-[var(--text)]" placeholder="web-01 (blank uses the address)" />
        </label>

        <label class="col-span-2 flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
          Host address
          <input
            ref="hostInput"
            v-model="form.host"
            class="focus-ring rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 font-mono text-[13px] text-[var(--text)]"
            placeholder="10.0.0.8 or server.example.com"
          />
        </label>

        <label class="flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
          Port
          <input v-model.number="form.port" type="number" min="1" max="65535" class="focus-ring rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 font-mono text-[13px] text-[var(--text)]" />
        </label>
        <label class="flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
          Username
          <input v-model="form.user" class="focus-ring rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 font-mono text-[13px] text-[var(--text)]" placeholder="root" />
        </label>

        <div v-if="!adhoc" class="col-span-2 flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
          Group
          <input v-model="form.group" class="focus-ring rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 text-[13px] text-[var(--text)]" placeholder="prod, lab, home…" />
        </div>

        <div class="col-span-2 flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
          Authentication
          <div class="flex gap-1">
            <button
              v-for="kind in (['password', 'key', 'agent'] as const)"
              :key="kind"
              type="button"
              class="focus-ring flex-1 rounded border px-2 py-1.5 text-[12px] capitalize"
              :class="form.authType === kind ? 'border-[var(--accent)] bg-[var(--accent)]/15 text-[var(--text)]' : 'border-[var(--border)] text-[var(--text-dim)] hover:text-[var(--text)]'"
              @click="form.authType = kind"
            >
              {{ kind === 'agent' ? 'ssh-agent' : kind }}
            </button>
          </div>
        </div>

        <label v-if="form.authType === 'key'" class="col-span-2 flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
          Private key
          <div class="flex gap-2">
            <input v-model="form.keyPath" class="focus-ring min-w-0 flex-1 rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 font-mono text-[12px] text-[var(--text)]" placeholder="C:\Users\me\.ssh\id_ed25519" />
            <button type="button" class="focus-ring flex items-center gap-1 rounded border border-[var(--border)] px-2 text-[12px] text-[var(--text-dim)] hover:text-[var(--text)]" @click="pickKey">
              <FileKey2 :size="13" /> browse
            </button>
          </div>
        </label>

        <label v-if="form.authType === 'password'" class="col-span-2 flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
          Password
          <div class="flex gap-2">
            <input
              v-model="form.password"
              :type="showSecret ? 'text' : 'password'"
              :placeholder="secretPlaceholder"
              class="focus-ring min-w-0 flex-1 rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 font-mono text-[13px] text-[var(--text)]"
            />
            <button type="button" class="focus-ring rounded border border-[var(--border)] px-2 text-[var(--text-dim)] hover:text-[var(--text)]" :title="showSecret ? 'Hide' : 'Show'" @click="showSecret = !showSecret">
              <Eye v-if="!showSecret" :size="13" />
              <EyeOff v-else :size="13" />
            </button>
          </div>
        </label>

        <!--
          Key authentication has no passphrase field on purpose: a field the
          user may leave empty is how "the passphrase does nothing" happened.
          An encrypted key asks in its own prompt, at the moment it is needed.
        -->
        <div v-else-if="form.authType === 'key'" class="col-span-2 rounded border border-[var(--border-soft)] bg-[var(--bg)] px-2.5 py-2 text-[11px] leading-snug text-[var(--text-dim)]">
          An encrypted key asks for its passphrase when you connect — nothing to type here.
          <span v-if="savedPassphrase" class="text-[var(--text)]">A passphrase is saved for this host.</span>
          <button v-if="savedPassphrase" type="button" class="focus-ring ml-1 rounded text-[var(--err)] hover:underline" @click="forgetPassphrase">
            Forget it
          </button>
        </div>

        <label v-if="!adhoc && form.authType === 'password'" class="col-span-2 flex items-center gap-2 text-[12px] text-[var(--text-dim)]">
          <input v-model="keepSecret" type="checkbox" class="focus-ring accent-[var(--accent-ink)]" />
          {{ keepSecret ? 'Store this password in the OS key ring' : 'Remove any stored password for this host' }}
        </label>

        <label v-if="form.authType === 'password'" class="col-span-2 flex items-start gap-2 text-[12px] text-[var(--text)]">
          <input v-model="form.twoFactor" data-test="two-factor" type="checkbox" class="focus-ring mt-0.5 accent-[var(--accent-ink)]" />
          <span>
            Two-factor authentication
            <span class="mt-0.5 block text-[11px] leading-snug text-[var(--text-dim)]">
              For bastions such as JumpServer: every connect asks for a one-time code and appends it to the saved
              password. The code is never stored.
            </span>
          </span>
        </label>

        <div class="col-span-2 flex flex-col gap-3 rounded-lg border border-[var(--border-soft)] bg-[var(--bg)] px-3 py-2.5">
          <label class="flex items-start gap-2 text-[12px] text-[var(--text)]">
            <input v-model="form.proxyEnabled" type="checkbox" class="focus-ring mt-0.5 accent-[var(--accent-ink)]" />
            <span>
              Connect through a jump host (SSH proxy)
              <span class="mt-0.5 block text-[11px] leading-snug text-[var(--text-dim)]">
                Dials the jump host first and tunnels to this server through it. The jump host needs nothing but a normal
                sshd — it never sees the session, and its host key is pinned separately.
              </span>
            </span>
          </label>

          <div v-if="form.proxyEnabled" class="grid grid-cols-2 gap-3">
            <label class="col-span-2 flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Jump host address
              <input v-model="form.proxyHost" class="focus-ring rounded border border-[var(--border)] bg-[var(--panel)] px-2 py-1.5 font-mono text-[13px] text-[var(--text)]" placeholder="bastion.example.com" />
            </label>
            <label class="flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Port
              <input v-model.number="form.proxyPort" type="number" min="1" max="65535" class="focus-ring rounded border border-[var(--border)] bg-[var(--panel)] px-2 py-1.5 font-mono text-[13px] text-[var(--text)]" />
            </label>
            <label class="flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Username
              <input v-model="form.proxyUser" class="focus-ring rounded border border-[var(--border)] bg-[var(--panel)] px-2 py-1.5 font-mono text-[13px] text-[var(--text)]" placeholder="jump" />
            </label>

            <div class="col-span-2 flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Jump host authentication
              <div class="flex gap-1">
                <button
                  v-for="kind in (['password', 'key', 'agent'] as const)"
                  :key="kind"
                  type="button"
                  class="focus-ring flex-1 rounded border px-2 py-1.5 text-[12px] capitalize"
                  :class="form.proxyAuthType === kind ? 'border-[var(--accent)] bg-[var(--accent)]/15 text-[var(--text)]' : 'border-[var(--border)] text-[var(--text-dim)] hover:text-[var(--text)]'"
                  @click="form.proxyAuthType = kind"
                >
                  {{ kind === 'agent' ? 'ssh-agent' : kind }}
                </button>
              </div>
            </div>

            <label v-if="form.proxyAuthType === 'key'" class="col-span-2 flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Jump host private key
              <div class="flex gap-2">
                <input v-model="form.proxyKeyPath" class="focus-ring min-w-0 flex-1 rounded border border-[var(--border)] bg-[var(--panel)] px-2 py-1.5 font-mono text-[12px] text-[var(--text)]" placeholder="C:\Users\me\.ssh\id_ed25519" />
                <button type="button" class="focus-ring flex items-center gap-1 rounded border border-[var(--border)] px-2 text-[12px] text-[var(--text-dim)] hover:text-[var(--text)]" @click="pickProxyKey">
                  <FileKey2 :size="13" /> browse
                </button>
              </div>
            </label>

            <label v-if="form.proxyAuthType === 'password'" class="col-span-2 flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Jump host password
              <input v-model="form.proxyPassword" type="password" :placeholder="proxySecretPlaceholder" class="focus-ring rounded border border-[var(--border)] bg-[var(--panel)] px-2 py-1.5 font-mono text-[13px] text-[var(--text)]" />
            </label>

            <p v-if="form.proxyAuthType === 'key'" class="col-span-2 text-[11px] leading-snug text-[var(--text-dim)]">
              An encrypted jump-host key asks for its passphrase when you connect.
            </p>
          </div>
        </div>

        <div v-if="error" class="col-span-2 rounded border border-[var(--err)]/50 bg-[var(--err)]/10 px-2 py-1.5 text-[12px] text-[var(--err)]">{{ error }}</div>
        <p v-if="adhoc" class="col-span-2 text-[11px] text-[var(--text-dim)]">
          This connection is not saved. Save it as a host to keep it and to reconnect later.
        </p>

        <div class="col-span-2 mt-1 flex items-center gap-2">
          <button v-if="editing" type="button" class="focus-ring flex items-center gap-1 rounded border border-[var(--border)] px-2.5 py-1.5 text-[12px] text-[var(--err)] hover:bg-[var(--err)]/10" @click="remove">
            <Trash2 :size="13" /> Delete
          </button>
          <span class="ml-auto flex gap-2">
            <button type="button" class="focus-ring rounded border border-[var(--border)] px-3 py-1.5 text-[12px] text-[var(--text-dim)] hover:text-[var(--text)]" @click="emit('close')">Cancel</button>
            <button v-if="adhoc" type="submit" :disabled="busy" class="focus-ring rounded bg-[var(--accent)] px-3 py-1.5 text-[12px] font-medium text-[var(--accent-ink)] disabled:opacity-60">
              {{ busy ? 'Connecting…' : 'Connect' }}
            </button>
            <template v-else>
              <button type="button" :disabled="busy" class="focus-ring rounded border border-[var(--border)] px-3 py-1.5 text-[12px] hover:text-[var(--text)] disabled:opacity-60" @click="save">Save</button>
              <button type="submit" :disabled="busy" class="focus-ring rounded bg-[var(--accent)] px-3 py-1.5 text-[12px] font-medium text-[var(--accent-ink)] disabled:opacity-60">
                {{ busy ? 'Working…' : 'Save & connect' }}
              </button>
            </template>
          </span>
        </div>
      </form>
    </div>
  </div>
</template>
