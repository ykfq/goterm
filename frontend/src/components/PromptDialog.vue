<script setup lang="ts">
/*
 * A one-field modal: a two-factor code, an export passphrase, an import
 * passphrase.
 *
 * It exists as its own component rather than as more props on PassphraseDialog
 * because the key-passphrase prompt is a different thing — it has a "remember
 * this in the key ring" box and it can retry on its own. This one just asks one
 * question and returns the answer.
 */
import { X } from 'lucide-vue-next';
import { nextTick, onMounted, ref } from 'vue';

const props = defineProps<{
  title: string;
  hint?: string;
  label?: string;
  placeholder?: string;
  confirm?: string;
  /** Mask what is typed (passphrases) instead of showing it (codes). */
  secret?: boolean;
  mono?: boolean;
  maxLength?: number;
  error?: string;
  /** A textarea instead of an input: for reviewing text that already exists. */
  multiline?: boolean;
  /** What the field starts with, when the question is about existing text. */
  initial?: string;
}>();

const emit = defineEmits<{
  (event: 'submit', value: string): void;
  (event: 'cancel'): void;
}>();

const value = ref(props.initial ?? '');
const input = ref<HTMLInputElement | null>(null);
const area = ref<HTMLTextAreaElement | null>(null);

function submit(): void {
  if (!value.value.trim()) {
    (area.value ?? input.value)?.focus();
    return;
  }
  // The raw value goes out: a passphrase may legitimately start or end with a
  // space, so only the emptiness check trims.
  emit('submit', value.value);
}

// A multi-line field is still a confirmation first: Enter sends, Shift+Enter
// adds a line break. Ctrl/Cmd+Enter does the same as Enter, because that is the
// habit the dialog taught until now and two ways to say "yes" cost nothing.
function onAreaKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Enter') return;
  if (event.shiftKey) return; // a newline belongs in the box
  event.preventDefault();
  submit();
}

onMounted(() => {
  void nextTick(() => (area.value ?? input.value)?.focus());
});
</script>

<template>
  <div
    class="fixed inset-0 z-[60] flex items-start justify-center bg-black/55 p-6 pt-24"
    @click.self="emit('cancel')"
    @keydown.esc.stop="emit('cancel')"
  >
    <form
      class="overflow-hidden rounded-xl border border-[var(--border)] bg-[var(--panel)] shadow-2xl"
      :class="multiline ? 'w-[620px] max-w-full' : 'w-[430px]'"
      @submit.prevent="submit"
    >
      <header class="flex items-center gap-2 border-b border-[var(--border)] px-4 py-3">
        <h2 class="text-[13px] font-semibold">{{ title }}</h2>
        <button
          type="button"
          class="focus-ring ml-auto rounded p-1 text-[var(--text-dim)] hover:text-[var(--text)]"
          @click="emit('cancel')"
        >
          <X :size="14" />
        </button>
      </header>

      <div class="flex flex-col gap-3 px-4 py-4">
        <p v-if="hint" class="text-[12px] leading-snug text-[var(--text-dim)]">{{ hint }}</p>

        <label class="flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
          {{ label || 'Value' }}
          <textarea
            v-if="multiline"
            ref="area"
            v-model="value"
            data-test="prompt-textarea"
            rows="12"
            spellcheck="false"
            autocomplete="off"
            autocapitalize="off"
            class="focus-ring w-full resize-none whitespace-pre rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 font-mono text-[12px] leading-snug text-[var(--text)]"
            @keydown="onAreaKeydown"
          />
          <input
            v-else
            ref="input"
            v-model="value"
            :type="secret ? 'password' : 'text'"
            :placeholder="placeholder"
            :maxlength="maxLength && maxLength > 0 ? maxLength : undefined"
            :class="mono ? 'font-mono tracking-wide' : ''"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            class="focus-ring w-full rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 text-[13px] text-[var(--text)]"
          />
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
          <button
            type="submit"
            class="focus-ring rounded bg-[var(--accent)] px-3 py-1.5 text-[12px] font-medium text-[var(--accent-ink)]"
          >
            {{ confirm || 'OK' }}
          </button>
        </div>
      </div>
    </form>
  </div>
</template>
