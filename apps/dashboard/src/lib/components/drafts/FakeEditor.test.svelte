<script lang="ts">
  // CodeEditor's stand-in in tests: a textarea with the same props.
  let { value, label, onChange }: { value: string; label: string; file?: string; bare?: boolean; onChange: (v: string) => void } = $props();
</script>

<textarea aria-label={label} {value} oninput={(e) => onChange(e.currentTarget.value)}></textarea>
