<script lang="ts">
  // DiffView's stand-in in jsdom tests: renders proposed text under its accessible name.
  let { file, original, proposed, label }: { file: string; original: string; proposed: string; label: string } = $props();
</script>

<div role="region" aria-label={label} data-file={file} data-original-len={original.length}>{proposed}</div>
