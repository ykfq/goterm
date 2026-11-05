<script setup lang="ts">
/*
 * The SFTP file browser.
 *
 * Docked to the right of the terminal rather than floating: the usual reason to
 * open it is "put this file next to what I am doing", and a panel keeps both in
 * view. Everything stateful lives in the store (`openSftp`/`refreshSftp`/...), so
 * this file only renders `state.sftp` and forwards clicks.
 *
 * Uploads and downloads run entirely inside Go between native dialogs; the only
 * thing that crosses the bridge for a transfer is progress, so the panel never
 * sees a local path it did not ask for.
 */
import {
  ArrowUp,
  Download,
  File as FileIcon,
  Folder,
  FolderPlus,
  Pencil,
  RefreshCw,
  Trash2,
  Upload,
  X,
} from 'lucide-vue-next';
import { computed, ref, watch } from 'vue';

import { formatBytes, toDate } from '../lib/bytes';
import type { SFTPEntry } from '../lib/protocol';
import {
  closeSftp,
  reclaimFocusSoon,
  refreshSftp,
  sftpDownload,
  sftpGoUp,
  sftpMkdir,
  sftpRemove,
  sftpRename,
  sftpUpload,
  state,
} from '../lib/store';
import type { SftpTransfer } from '../lib/store';

const draft = ref('');
const busy = computed(() => state.sftp.loading);
const atRoot = computed(() => state.sftp.path === '/' || state.sftp.path === '');

// The path bar follows navigation but stays editable while the user types:
// a listing reply must not overwrite half-typed text, so only a *change* of
// the listed directory rewrites the draft.
watch(
  () => state.sftp.path,
  (path) => {
    draft.value = path;
  },
  { immediate: true },
);

/** Directories first, then names — how every file manager since 1984 does it. */
const rows = computed(() => {
  const entries = [...state.sftp.entries];
  entries.sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
  return entries;
});

const hasTransfers = computed(() => state.sftp.transfers.length > 0);

function submitPath(): void {
  const next = draft.value.trim();
  if (!next) {
    draft.value = state.sftp.path;
    return;
  }
  if (next === state.sftp.path) return;
  void refreshSftp(next);
}

function enter(entry: SFTPEntry): void {
  if (entry.dir) void refreshSftp(entry.path);
}

function modified(entry: SFTPEntry): string {
  const date = toDate(entry.modTime);
  if (!date) return '';
  return date.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

function percent(row: SftpTransfer): number {
  if (row.total <= 0) return row.done > 0 ? 100 : 0;
  return Math.min(100, Math.round((row.done / row.total) * 100));
}

function dismiss(): void {
  closeSftp();
  // The panel took the keyboard; closing it must hand the terminal back or
  // Windows paints the IME as disabled (see reclaimFocusSoon).
  reclaimFocusSoon();
}
</script>

<template>
  <aside
    class="flex w-[400px] shrink-0 flex-col overflow-hidden border-l border-[var(--border)] bg-[var(--panel-2)] text-[12px]"
    data-test="sftp-panel"
  >
    <header class="flex h-9 shrink-0 items-center gap-2 border-b border-[var(--border)] px-2">
      <span class="truncate-1 flex-1 font-medium text-[var(--text-dim)]" data-test="sftp-title">
        Files · {{ state.sftp.path || '…' }}
      </span>
      <button
        class="focus-ring rounded p-1 text-[var(--text-dim)] hover:bg-[var(--panel)] hover:text-[var(--text)]"
        title="Close file browser"
        data-test="sftp-close"
        @click="dismiss"
      >
        <X :size="13" />
      </button>
    </header>

    <div class="flex shrink-0 items-center gap-1 border-b border-[var(--border)] px-2 py-1.5">
      <button
        class="focus-ring rounded p-1 text-[var(--text-dim)] hover:bg-[var(--panel)] hover:text-[var(--text)] disabled:opacity-40"
        title="Parent folder"
        :disabled="atRoot || busy"
        data-test="sftp-up"
        @click="sftpGoUp()"
      >
        <ArrowUp :size="14" />
      </button>
      <button
        class="focus-ring rounded p-1 text-[var(--text-dim)] hover:bg-[var(--panel)] hover:text-[var(--text)] disabled:opacity-40"
        title="Refresh"
        :disabled="busy"
        data-test="sftp-refresh"
        @click="refreshSftp()"
      >
        <RefreshCw :size="14" :class="{ 'animate-spin': busy }" />
      </button>
      <input
        v-model="draft"
        class="focus-ring min-w-0 flex-1 rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1 font-mono text-[11px] text-[var(--text)] outline-none"
        spellcheck="false"
        title="Remote path — press Enter to go"
        data-test="sftp-path"
        @keydown.enter.prevent="submitPath"
        @keydown.esc.prevent="draft = state.sftp.path"
      />
      <button
        class="focus-ring rounded p-1 text-[var(--text-dim)] hover:bg-[var(--panel)] hover:text-[var(--text)]"
        title="New folder here"
        data-test="sftp-mkdir"
        @click="sftpMkdir()"
      >
        <FolderPlus :size="14" />
      </button>
      <button
        class="focus-ring rounded p-1 text-[var(--text-dim)] hover:bg-[var(--panel)] hover:text-[var(--text)]"
        title="Upload files here"
        data-test="sftp-upload"
        @click="sftpUpload()"
      >
        <Upload :size="14" />
      </button>
    </div>

    <div
      v-if="state.sftp.error"
      class="shrink-0 border-b border-[var(--err)]/40 bg-[var(--err)]/10 px-2 py-1.5 text-[11px] break-words text-[var(--err)]"
      data-test="sftp-error"
      role="alert"
    >
      {{ state.sftp.error }}
    </div>

    <div v-if="hasTransfers" class="shrink-0 space-y-1 border-b border-[var(--border)] px-2 py-1.5" data-test="sftp-transfers">
      <div v-for="row in state.sftp.transfers" :key="row.key" class="space-y-0.5">
        <div class="flex items-center gap-2 text-[11px] text-[var(--text-dim)]">
          <Download v-if="row.direction === 'download'" :size="11" class="shrink-0" />
          <Upload v-else :size="11" class="shrink-0" />
          <span class="truncate-1 flex-1">{{ row.name }}</span>
          <span class="shrink-0 tabular-nums">{{ percent(row) }}%</span>
        </div>
        <div class="h-1 overflow-hidden rounded bg-[var(--border)]">
          <div class="h-full bg-[var(--accent)] transition-[width]" :style="{ width: `${percent(row)}%` }" />
        </div>
      </div>
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto">
      <p v-if="busy && !rows.length" class="px-3 py-4 text-center text-[var(--text-dim)]">Listing…</p>
      <p v-else-if="!rows.length" class="px-3 py-4 text-center text-[var(--text-dim)]" data-test="sftp-empty">
        This folder is empty.
      </p>
      <ul v-else class="divide-y divide-[var(--border)]/60">
        <li
          v-for="entry in rows"
          :key="entry.path"
          class="group flex items-center gap-2 px-2 py-1 hover:bg-[var(--panel)]"
          :class="{ 'cursor-pointer': entry.dir }"
          :title="entry.path"
          data-test="sftp-entry"
          @dblclick="enter(entry)"
          @keydown.enter="enter(entry)"
        >
          <span class="shrink-0 text-[var(--text-dim)]">
            <Folder v-if="entry.dir" :size="14" class="text-[var(--accent)]" />
            <FileIcon v-else :size="14" />
          </span>
          <button
            class="truncate-1 min-w-0 flex-1 text-left"
            :class="entry.dir ? 'text-[var(--text)] hover:underline' : 'text-[var(--text)]'"
            :data-test="entry.dir ? 'sftp-open-dir' : undefined"
            @click="enter(entry)"
          >
            {{ entry.name }}
          </button>
          <span class="shrink-0 tabular-nums text-[10px] text-[var(--text-dim)]" :title="entry.mode">
            {{ entry.dir ? '' : formatBytes(entry.size) }}
          </span>
          <span class="shrink-0 tabular-nums text-[10px] text-[var(--text-dim)]">
            {{ modified(entry) }}
          </span>
          <span class="flex shrink-0 items-center gap-0.5 opacity-0 group-hover:opacity-100 focus-within:opacity-100">
            <button
              v-if="!entry.dir"
              class="focus-ring rounded p-0.5 text-[var(--text-dim)] hover:bg-[var(--border)] hover:text-[var(--text)]"
              title="Download…"
              data-test="sftp-download"
              @click.stop="sftpDownload(entry)"
            >
              <Download :size="12" />
            </button>
            <button
              class="focus-ring rounded p-0.5 text-[var(--text-dim)] hover:bg-[var(--border)] hover:text-[var(--text)]"
              title="Rename…"
              data-test="sftp-rename"
              @click.stop="sftpRename(entry)"
            >
              <Pencil :size="12" />
            </button>
            <button
              class="focus-ring rounded p-0.5 text-[var(--text-dim)] hover:bg-[var(--border)] hover:text-[var(--err)]"
              title="Delete"
              data-test="sftp-remove"
              @click.stop="sftpRemove(entry)"
            >
              <Trash2 :size="12" />
            </button>
          </span>
        </li>
      </ul>
    </div>
  </aside>
</template>
