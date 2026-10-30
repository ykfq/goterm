<script setup lang="ts">
import { Activity, FolderOpen, Moon, Plug, Settings, Shield, ShieldAlert, Sun, X } from 'lucide-vue-next';
import { computed } from 'vue';

import { formatBytes, shortFingerprint } from '../lib/bytes';
import { activeTab, pingBackend, pushNotice, runSelfTest, runtimeSummary, setTheme, state } from '../lib/store';

const emit = defineEmits<{ (event: 'show-settings'): void }>();

const backend = computed(() => state.info);
// Reactive on purpose: the picker shim is installed after the backend probe, so
// reading the plain module variable here would leave the badge stuck on its
// first (pre-install) value.
const pickers = computed(() => state.filePickerMode);

function toggleTheme(): void {
  setTheme(state.settings.theme === 'dark' ? 'light' : 'dark');
}

function copyConfigDir(): void {
  const dir = backend.value?.configDir;
  if (!dir) return;
  void navigator.clipboard?.writeText(dir).then(
    () => pushNotice('info', `Copied ${dir}`),
    () => pushNotice('warn', dir),
  );
}
</script>

<template>
  <footer class="flex h-7 shrink-0 items-center gap-3 border-t border-[var(--border)] bg-[var(--panel)] px-3 text-[11px] text-[var(--text-dim)]">
    <template v-if="activeTab">
      <span class="truncate-1 max-w-[280px] text-[var(--text)]">{{ activeTab.target }}</span>
      <span class="opacity-40">|</span>
      <span class="truncate-1 max-w-[220px]">{{ activeTab.detail || (activeTab.status === 'open' ? 'connected' : activeTab.status) }}</span>
      <!-- Worth seeing at a glance: a session that looks direct and is not. -->
      <span v-if="activeTab.via" class="truncate-1 max-w-[220px]" :title="'Tunnelled through ' + activeTab.via">via {{ activeTab.via }}</span>
      <template v-if="activeTab.status === 'open'">
        <span v-if="activeTab.serverVersion" class="opacity-40">|</span>
        <span v-if="activeTab.serverVersion" class="truncate-1 max-w-[180px]">{{ activeTab.serverVersion }}</span>
        <span v-if="activeTab.fingerprint" class="opacity-40">|</span>
        <span v-if="activeTab.fingerprint" class="font-mono" :title="'Pinned host key: ' + activeTab.fingerprint">
          {{ shortFingerprint(activeTab.fingerprint) }}
        </span>
        <span class="opacity-40">|</span>
        <span>↓ {{ formatBytes(activeTab.bytesIn) }} ↑ {{ formatBytes(activeTab.bytesOut) }}</span>
      </template>
    </template>
    <span v-else class="opacity-70">{{ runtimeSummary() }}</span>

    <span class="ml-auto flex items-center gap-3">
      <span
        class="flex items-center gap-1"
        :class="pickers === 'none' ? 'text-[var(--warn)]' : ''"
        :title="
          pickers === 'bridge'
            ? 'trz/tsz use native Windows dialogs through Go'
            : pickers === 'browser'
              ? 'trz/tsz use the webview File System Access API'
              : 'No file picker is available, so trz/tsz transfers cannot start.'
        "
      >
        <ShieldAlert v-if="pickers === 'none'" :size="12" />
        <FolderOpen v-else :size="12" />
        {{ pickers === 'none' ? 'no file picker' : pickers === 'bridge' ? 'native picker' : 'browser picker' }}
      </span>
      <span v-if="backend" class="flex items-center gap-1">
        <Shield v-if="backend.secretSecure" :size="12" class="text-[var(--ok)]" />
        <ShieldAlert v-else :size="12" class="text-[var(--warn)]" />
        <button class="focus-ring rounded hover:text-[var(--text)]" :title="'Config dir: ' + backend.configDir" @click="copyConfigDir">
          {{ backend.secretSecure ? backend.secretBackend : 'sealed file' }}
        </button>
      </span>
      <span class="opacity-40">|</span>
      <span>{{ state.tabs.length }} tab{{ state.tabs.length === 1 ? '' : 's' }}</span>
      <span class="opacity-40">|</span>
      <button class="focus-ring flex items-center gap-1 rounded px-1 hover:text-[var(--text)]" title="Ping the Go backend (proves both directions of the bridge)" @click="pingBackend">
        <Plug :size="12" /> ping
      </button>
      <button class="focus-ring flex items-center gap-1 rounded px-1 hover:text-[var(--text)]" title="Run the SSH self-test against a local server" @click="runSelfTest">
        <Activity :size="12" /> self-test
      </button>
      <button class="focus-ring rounded px-1 hover:text-[var(--text)]" title="Settings (Ctrl+,)" @click="emit('show-settings')">
        <Settings :size="12" />
      </button>
      <button class="focus-ring rounded px-1 hover:text-[var(--text)]" title="Toggle light/dark" @click="toggleTheme">
        <Moon v-if="state.settings.theme === 'dark'" :size="12" />
        <Sun v-else :size="12" />
      </button>
      <button v-if="state.notices.length" class="focus-ring flex items-center gap-1 rounded px-1 hover:text-[var(--text)]" title="Clear notices" @click="state.notices.splice(0)">
        <X :size="12" /> {{ state.notices.length }}
      </button>
    </span>
  </footer>
</template>
