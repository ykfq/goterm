<script setup lang="ts">
import { ChevronRight, Download, Folder, KeyRound, Lock, Pencil, Plug, Plus, Search, Terminal, Trash2, Upload } from 'lucide-vue-next';
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';

import { backend, errorMessage } from '../lib/backend';
import {
  SIDEBAR_WIDTH_DEFAULT,
  connectHost,
  exportHosts,
  importHosts,
  loadHosts,
  maxSidebarWidth,
  openSftp,
  previewSidebarWidth,
  pushNotice,
  setSidebarWidth,
  state,
} from '../lib/store';
import type { Host } from '../lib/protocol';
import type { Tab } from '../lib/store';

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

// --- files ---------------------------------------------------------------
//
// A live session is the only thing SFTP can be opened on, so the entry point
// lists exactly those. Hiding it behind the tab menu was the wrong place: the
// sidebar is where you already look to see what is connected.
const liveSessions = computed(() => state.tabs.filter((tab) => tab.status === 'open' && Boolean(tab.sessionId)));

function isBrowsing(tab: Tab): boolean {
  return state.sftp.open && state.sftp.tabId === tab.id;
}

// --- resizable width -------------------------------------------------------
//
// The divider is a thin strip on the right edge. Width follows the pointer
// while dragging (the terminal's ResizeObserver refits on every frame) but is
// only written to localStorage on release; double-click restores the default,
// and dragging it to the left edge folds the sidebar away entirely.
const resizing = ref(false);
let dragStartX = 0;
let dragStartWidth = 0;
/** The strip being dragged, so a move can end the drag itself. */
let dragHandle: HTMLElement | null = null;
let dragPointer = 0;

/**
 * How close to the left edge a drag has to get before the panel folds away
 * instead of staying as an unusable sliver. The width the user had is kept, so
 * reopening (Ctrl+Shift+B, or the sidebar button) comes back the way it was.
 */
const COLLAPSE_AT_PX = 40;

function releasePointer(): void {
  const handle = dragHandle;
  dragHandle = null;
  if (!handle) return;
  if (typeof handle.releasePointerCapture === 'function' && handle.hasPointerCapture(dragPointer)) {
    handle.releasePointerCapture(dragPointer);
  }
}

function onResizeStart(event: PointerEvent): void {
  if (event.button !== 0) return;
  resizing.value = true;
  dragStartX = event.clientX;
  dragStartWidth = state.settings.sidebarWidth;
  dragHandle = event.currentTarget as HTMLElement;
  dragPointer = event.pointerId;
  // Capture so the drag keeps working when the pointer outruns the strip.
  if (typeof dragHandle.setPointerCapture === 'function') dragHandle.setPointerCapture(event.pointerId);
  event.preventDefault();
}

function onResizeMove(event: PointerEvent): void {
  if (!resizing.value) return;
  const next = dragStartWidth + (event.clientX - dragStartX);
  if (next <= COLLAPSE_AT_PX) {
    // There is nothing left worth dragging at that width, so fold the panel
    // away and end the drag here — the divider disappears with it, which is
    // also the release. The remembered width is put back, not the sliver.
    releasePointer();
    resizing.value = false;
    setSidebarWidth(dragStartWidth);
    state.sidebarOpen = false;
    return;
  }
  previewSidebarWidth(next);
}

function onResizeEnd(): void {
  if (!resizing.value) return;
  releasePointer();
  resizing.value = false;
  setSidebarWidth(state.settings.sidebarWidth);
}

function resetWidth(): void {
  setSidebarWidth(SIDEBAR_WIDTH_DEFAULT);
}

// The ceiling is half the window, so it moves when the window does: a panel
// dragged wide on a maximized window must not stay that wide after a restore.
function enforceWidthCap(): void {
  const cap = maxSidebarWidth();
  if (state.settings.sidebarWidth > cap) setSidebarWidth(cap);
}

onMounted(() => window.addEventListener('resize', enforceWidthCap));
onBeforeUnmount(() => window.removeEventListener('resize', enforceWidthCap));
</script>

<template>
  <aside
    class="relative flex h-full shrink-0 flex-col overflow-hidden border-r border-[var(--border)] bg-[var(--panel)]"
    :class="resizing ? 'select-none' : ''"
    :style="{ width: `${state.settings.sidebarWidth}px` }"
    data-test="sidebar"
  >
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

    <section class="shrink-0 border-t border-[var(--border)] px-2 py-2" data-test="sidebar-files">
      <h3 class="flex items-center gap-1.5 px-2 pb-1 text-[10px] uppercase tracking-wider text-[var(--text-dim)]">
        <Folder :size="11" /> Files
        <span v-if="liveSessions.length" class="ml-auto normal-case tracking-normal">{{ liveSessions.length }}</span>
      </h3>
      <p v-if="!liveSessions.length" class="px-2 pb-0.5 text-[11px] leading-snug text-[var(--text-dim)]">
        Connect a host to browse its files over SFTP.
      </p>
      <button
        v-for="tab in liveSessions"
        :key="tab.id"
        class="focus-ring flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-[12px]"
        :class="
          isBrowsing(tab)
            ? 'bg-[var(--panel-2)] text-[var(--text)]'
            : 'text-[var(--text-dim)] hover:bg-[var(--panel-2)] hover:text-[var(--text)]'
        "
        :title="`Browse ${tab.target} over SFTP`"
        data-test="sidebar-sftp-entry"
        @click="openSftp(tab.id)"
      >
        <Folder :size="13" class="shrink-0" :class="isBrowsing(tab) ? 'text-[var(--accent)]' : ''" />
        <span class="truncate-1 min-w-0 flex-1">{{ tab.title }}</span>
      </button>
    </section>

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
        Files move over <code class="text-[var(--text)]">SFTP</code> — nothing to install on the server. Drag onto a
        terminal for <code class="text-[var(--text)]">trz</code>/<code class="text-[var(--text)]">tsz</code>.
      </p>
    </footer>

    <div
      class="absolute inset-y-0 right-0 z-10 w-[4px] cursor-col-resize hover:bg-[var(--accent)]/40"
      :class="resizing ? 'bg-[var(--accent)]/50' : ''"
      title="Drag to resize · to the edge to collapse · double-click to reset"
      data-test="sidebar-resizer"
      @pointerdown="onResizeStart"
      @pointermove="onResizeMove"
      @pointerup="onResizeEnd"
      @pointercancel="onResizeEnd"
      @dblclick="resetWidth"
    ></div>
  </aside>
</template>
