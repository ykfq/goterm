<script setup lang="ts">
/*
 * The passphrase prompt.
 *
 * It exists because a private key is allowed to be encrypted, and an encrypted
 * key cannot be used from a form field that the user filled in five minutes
 * ago and may have left blank. Go raises "passphrase required", the store
 * opens this dialog, and the answer is retried immediately — see
 * `connectAskingForPassphrase` in lib/store.ts.
 */
import { Eye, EyeOff, KeyRound, X } from 'lucide-vue-next';
import { nextTick, onMounted, ref } from 'vue';

const props = defineProps<{
  title: string;
  hint: string;
  error?: string;
  /** Only a saved host can store the answer; Quick connect has nowhere to put it. */
  rememberable?: boolean;
}>();

const emit = defineEmits<{
  (event: 'submit', value: { secret: string; remember: boolean }): void;
  (event: 'cancel'): void;
}>();

const secret = ref('');
const remember = ref(false);
const show = ref(false);
const input = ref<HTMLInputElement | null>(null);

function submit(): void {
  if (!secret.value) {
    input.value?.focus();
    return;
  }
  emit('submit', { secret: secret.value, remember: remember.value && Boolean(props.rememberable) });
}

onMounted(() => {
  void nextTick(() => input.value?.focus());
});
</script>

<template>
  <div class="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-6 pt-24" @click.self="emit('cancel')" @keydown.esc.stop="emit('cancel')">
    <form
      class="w-[420px] overflow-hidden rounded-xl border border-[var(--border)] bg-[var(--panel)] shadow-2xl"
      @submit.prevent="submit"
    >
      <header class="flex items-center gap-2 border-b border-[var(--border)] px-4 py-3">
        <KeyRound :size="15" class="text-[var(--accent)]" />
        <h2 class="text-[13px] font-semibold">{{ title }}</h2>
        <button type="button" class="focus-ring ml-auto rounded p-1 text-[var(--text-dim)] hover:text-[var(--text)]" @click="emit('cancel')">
          <X :size="14" />
        </button>
      </header>

      <div class="flex flex-col gap-3 px-4 py-4">
        <p class="text-[12px] text-[var(--text-dim)]">{{ hint }}</p>

        <label class="flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
          Passphrase
          <div class="flex gap-2">
            <input
              ref="input"
              v-model="secret"
              :type="show ? 'text' : 'password'"
              autocomplete="off"
              spellcheck="false"
              placeholder="the key's passphrase"
              class="focus-ring min-w-0 flex-1 rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 font-mono text-[13px] text-[var(--text)]"
            />
            <button
              type="button"
              class="focus-ring rounded border border-[var(--border)] px-2 text-[var(--text-dim)] hover:text-[var(--text)]"
              :title="show ? 'Hide' : 'Show'"
              @click="show = !show"
            >
              <Eye v-if="!show" :size="13" />
              <EyeOff v-else :size="13" />
            </button>
          </div>
        </label>

        <label v-if="rememberable" class="flex items-center gap-2 text-[12px] text-[var(--text-dim)]">
          <input v-model="remember" type="checkbox" class="focus-ring accent-[var(--accent-ink)]" />
          Store this passphrase in the OS key ring
        </label>

        <div v-if="error" class="rounded border border-[var(--err)]/50 bg-[var(--err)]/10 px-2 py-1.5 text-[12px] text-[var(--err)]">
          {{ error }}
        </div>

        <div class="mt-1 flex justify-end gap-2">
          <button
            type="button"
            class="focus-ring rounded border border-[var(--border)] px-3 py-1.5 text-[12px] text-[var(--text-dim)] hover:text-[var(--text)]"
            @click="emit('cancel')"
          >
            Cancel
          </button>
          <button type="submit" class="focus-ring rounded bg-[var(--accent)] px-3 py-1.5 text-[12px] font-medium text-[var(--accent-ink)]">
            Unlock
          </button>
        </div>
      </div>
    </form>
  </div>
</template>
