<script lang="ts">
  interface Props {
    checked: boolean;
    /** The switch's spoken name; the visible label beside it says the same. */
    label: string;
    disabled?: boolean;
    /** Why it is disabled. */
    title?: string;
    onChange?: (on: boolean) => void;
  }
  let { checked, label, disabled = false, title, onChange }: Props = $props();
</script>

<!-- The drawn toggle: 28 by 16px, accent when on. -->
<button
  type="button"
  role="switch"
  aria-checked={checked}
  aria-label={label}
  {disabled}
  {title}
  onclick={() => onChange?.(!checked)}
  class="relative h-4 w-7 shrink-0 rounded-full transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50 {checked
    ? 'bg-accent'
    : 'bg-line'}"
>
  <span aria-hidden="true" class="absolute top-0.5 size-3 rounded-full bg-surface transition-[left] {checked ? 'left-3.5' : 'left-0.5'}"></span>
</button>
