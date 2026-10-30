<script setup lang="ts">
import { ChevronRight, Download, KeyRound, Lock, Pencil, Plug, Plus, Search, Terminal, Trash2, Upload } from 'lucide-vue-next';
import { computed, ref } from 'vue';

import { backend, errorMessage } from '../lib/backend';
import { connectHost, exportHosts, importHosts, loadHosts, pushNotice, state } from '../lib/store';
import type { Host } from '../lib/protocol';

const emit = defineEmits<{
  (event: 'new-host'): void;
  (event: 'edit-host', host: Host): void;
  (event: 'quick-connect'): void;
  (event: 'show-known-hosts'): void;
}>();

const filter = ref('');
const selectedId = ref<string>('');
const confirmingDelete = ref<string>('');

const visible = computed(() => {
  const needle = filter.value.trim().toLowerCase();
  if (!needle) return state.hosts;
  return state.hosts.filter((host) =>
    [host.name, host.host, host.user, host.group].some((field) => (field || '').toLowerCase().includes(needle)),
  );
});

const groups = computed(() => {
  const map = new Map<string, Host[]>();
  for (const host of visible.value) {
    const key = host.group?.trim() || '';
    const list = map.get(key);
    if (list) list.push(host);
    else map.set(key, [host]);
  }
  return [...map.entries()].sort((a, b) => a[0].localeCompare(b[0]));
});

async function remove(host: Host): Promise<void> {
  try {
    await backend.deleteHost(host.id);
    confirmingDelete.value = '';
    await loadHosts();
    pushNotice('info', `Deleted ${host.name || host.host}`);
  } catch (error) {
    pushNotice('error', errorMessage(error));
  }
}

const accentFor = (host: Host): string => host.colour || 'var(--text-dim)';
</script>

<template>
  <aside class="flex h-full w-[268px] shrink-0 flex-col border-r border-[var(--border)] bg-[var(--panel)]">
    <header class="flex items-center gap-2 px-3 py-2.5">
      <span class="flex h-6 w-6 items-center justify-center rounded bg-[var(--accent)]/18 text-[var(--accent)]">
        <Terminal :size="14" />
      </span>
      <span class="text-[13px] font-semibold tracking-tight">GoTerm</span>
      <span class="text-[10px] text-[var(--text-dim)]">v{{ state.info?.version ?? '0.1.0' }}</span>
    </header>

    <div class="px-3 pb-2">
      <div class="relative">
        <Search :size="13" class="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-[var(--text-dim)]" />
        <input
          v-model="filter"
          class="focus-ring w-full rounded border border-[var(--border)] bg-[var(--bg)] py-1.5 pl-7 pr-2 text-[12px]"
          placeholder="Search hosts"
        />
      </div>
    </div>

    <div class="flex gap-2 px-3 pb-2">
      <button
        class="focus-ring flex flex-1 items-center justify-center gap-1.5 rounded bg-[var(--accent)] px-2 py-1.5 text-[12px] font-medium text-[var(--accent-ink)]"
        @click="emit('new-host')"
      >
        <Plus :size="13" /> New host
      </button>
      <button
        class="focus-ring flex items-center justify-center gap-1.5 rounded border border-[var(--border)] px-2 py-1.5 text-[12px] text-[var(--text-dim)] hover:text-[var(--text)]"
        title="Connect once without saving"
        @click="emit('quick-connect')"
      >
        <Plug :size="13" /> Quick
      </button>
    </div>

    <nav class="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
      <p v-if="!state.hosts.length && !filter" class="mt-6 px-2 text-center text-[12px] leading-relaxed text-[var(--text-dim)]">
        No saved hosts yet.
        <br />
        Add one, or hit Quick to connect once.
      </p>
      <p v-else-if="!visible.length" class="mt-6 px-2 text-center text-[12px] text-[var(--text-dim)]">Nothing matches “{{ filter }}”.</p>

      <section v-for="group in groups" :key="group[0]" class="mb-2">
        <h3 v-if="group[0]" class="px-2 py-1 text-[10px] uppercase tracking-wider text-[var(--text-dim)]">
          {{ group[0] }}
        </h3>
        <div
          v-for="host in group[1]"
          :key="host.id"
          class="group relative flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-[12px]"
          :class="selectedId === host.id ? 'bg-[var(--panel-2)]' : 'hover:bg-[var(--panel-2)]'"
          :title="`Double-click to connect · ${host.user}@${host.host}:${host.port}`"
          @click="selectedId = host.id"
          @dblclick="connectHost(host.id)"
        >
          <span class="h-1.5 w-1.5 shrink-0 rounded-full" :style="{ background: accentFor(host) }" />
          <span class="min-w-0 flex-1">
            <span class="truncate-1 block">{{ host.name || host.host }}</span>
            <span class="truncate-1 block text-[10px] text-[var(--text-dim)]">
              {{ host.user }}@{{ host.host }}:{{ host.port }}
            </span>
          </span>
          <span class="flex shrink-0 items-center gap-0.5 text-[var(--text-dim)]">
            <Lock v-if="host.hasSecret" :size="12" title="secret stored" />
            <KeyRound v-else-if="host.authType === 'key'" :size="12" title="key authentication" />
          </span>

          <span class="absolute right-1 hidden items-center gap-0.5 rounded bg-[var(--panel-2)] group-hover:flex">
            <button class="focus-ring rounded p-1 hover:text-[var(--accent)]" title="Connect" @click.stop="connectHost(host.id)">
              <ChevronRight :size="13" />
            </button>
            <button class="focus-ring rounded p-1 hover:text-[var(--text)]" title="Edit" @click.stop="emit('edit-host', host)">
              <Pencil :size="12" />
            </button>
            <button
              class="focus-ring rounded p-1 hover:text-[var(--err)]"
              :title="confirmingDelete === host.id ? 'Click again to confirm' : 'Delete'"
              @click.stop="confirmingDelete === host.id ? remove(host) : (confirmingDelete = host.id)"
            >
              <Trash2 :size="12" />
            </button>
          </span>
        </div>
      </section>
    </nav>

    <footer class="border-t border-[var(--border)] px-3 py-2 text-[11px] text-[var(--text-dim)]">
      <div class="mb-1 flex gap-1">
        <button
          class="focus-ring flex flex-1 items-center gap-1.5 rounded border border-[var(--border)] px-1.5 py-1 hover:text-[var(--text)]"
          title="Export every host to one encrypted file (passwords included)"
          @click="exportHosts"
        >
          <Upload :size="12" /> Export
        </button>
        <button
          class="focus-ring flex flex-1 items-center gap-1.5 rounded border border-[var(--border)] px-1.5 py-1 hover:text-[var(--text)]"
          title="Import hosts from an exported file"
          @click="importHosts"
        >
          <Download :size="12" /> Import
        </button>
      </div>
      <button class="focus-ring flex w-full items-center gap-1.5 rounded px-1 py-1 text-left hover:text-[var(--text)]" @click="emit('show-known-hosts')">
        <Lock :size="12" />
        Known hosts
        <span class="ml-auto">{{ state.knownHosts.length }}</span>
      </button>
      <p class="px-1 pt-1 leading-snug">
        File transfer needs <code class="text-[var(--text)]">trz</code>/<code class="text-[var(--text)]">tsz</code> on the
        server; drag a file onto a terminal to upload.
      </p>
    </footer>
  </aside>
</template>
