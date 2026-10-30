<script setup lang="ts">
import { ShieldCheck, Trash2, X } from 'lucide-vue-next';
import { computed, ref } from 'vue';

import { backend, errorMessage } from '../lib/backend';
import { toDate } from '../lib/bytes';
import { loadKnownHosts, pushNotice, state } from '../lib/store';

const emit = defineEmits<{ (event: 'close'): void }>();
const filter = ref('');

const rows = computed(() => {
  const needle = filter.value.trim().toLowerCase();
  return state.knownHosts.filter(
    (record) => !needle || record.address.toLowerCase().includes(needle) || record.fingerprint.toLowerCase().includes(needle),
  );
});

function when(value: unknown): string {
  const date = toDate(value);
  return date ? date.toLocaleString() : '—';
}

async function forget(address: string): Promise<void> {
  try {
    await backend.forgetHostKey(address);
    await loadKnownHosts();
    pushNotice('info', `Forgot the key for ${address}. The next connection will re-trust it.`);
  } catch (error) {
    pushNotice('error', errorMessage(error));
  }
}
</script>

<template>
  <div class="fixed inset-0 z-40 flex items-center justify-center bg-black/45 p-6" @click.self="emit('close')">
    <div class="flex max-h-[80vh] w-[720px] flex-col overflow-hidden rounded-xl border border-[var(--border)] bg-[var(--panel)] shadow-2xl">
      <header class="flex items-center gap-2 border-b border-[var(--border)] px-4 py-3">
        <ShieldCheck :size="15" class="text-[var(--ok)]" />
        <h2 class="text-[13px] font-semibold">Known hosts</h2>
        <p class="ml-2 text-[11px] text-[var(--text-dim)]">
          Fingerprints are recorded on first use. A change is a hard error, never a warning you can click past.
        </p>
        <button class="focus-ring ml-auto rounded p-1 text-[var(--text-dim)] hover:text-[var(--text)]" @click="emit('close')">
          <X :size="14" />
        </button>
      </header>
      <div class="border-b border-[var(--border)] px-4 py-2">
        <input v-model="filter" class="focus-ring w-full rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1 text-[12px]" placeholder="Filter by address or fingerprint" />
      </div>
      <div class="min-h-0 flex-1 overflow-auto">
        <table class="w-full border-collapse text-[12px]">
          <thead class="sticky top-0 bg-[var(--panel-2)] text-left text-[11px] text-[var(--text-dim)]">
            <tr>
              <th class="px-4 py-2 font-medium">Address</th>
              <th class="px-2 py-2 font-medium">Type</th>
              <th class="px-2 py-2 font-medium">Fingerprint</th>
              <th class="px-2 py-2 font-medium">Added</th>
              <th class="px-2 py-2" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="record in rows" :key="record.address" class="border-t border-[var(--border-soft)]">
              <td class="px-4 py-2">{{ record.address }}</td>
              <td class="px-2 py-2 text-[var(--text-dim)]">{{ record.type }}</td>
              <td class="px-2 py-2 font-mono text-[11px]">{{ record.fingerprint }}</td>
              <td class="px-2 py-2 text-[var(--text-dim)]">{{ when(record.addedAt) }}</td>
              <td class="px-2 py-2 text-right">
                <button class="focus-ring rounded p-1 text-[var(--text-dim)] hover:text-[var(--err)]" title="Forget this key" @click="forget(record.address)">
                  <Trash2 :size="13" />
                </button>
              </td>
            </tr>
            <tr v-if="!rows.length">
              <td colspan="5" class="px-4 py-6 text-center text-[var(--text-dim)]">Nothing pinned yet.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
