<!--
  A text field for a host folder that suggests the folders under what is typed,
  from the file browser's listing. Suggestions are only hints: any path can be
  typed. Picking one descends into it, so the next level shows.
-->
<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import type { HTMLInputAttributes } from "svelte/elements";
  import { api, call, type Schema } from "#lib/api/client.ts";
  import { folderSuggestions, pathParts, trimSlash } from "#lib/storage.ts";

  interface Props extends Omit<HTMLInputAttributes, "value" | "class"> {
    value: string;
    /** Classes for the wrapper, which takes the field's place in a layout. */
    class?: string;
    /** A suggestion was picked; the path has no trailing slash. */
    onpick?: (path: string) => void;
    /** Suggest the folders under / while the field is empty; off for a field that takes more than paths. */
    listRoot?: boolean;
  }
  let { value = $bindable(""), class: cls = "", onpick, listRoot = true, onfocus, onblur, onkeydown, oninput, ...rest }: Props = $props();

  const id = $props.id();
  const listId = `${id}-folders`;

  let focused = $state(false);
  /** Esc closed the list; typing opens it again. */
  let dismissed = $state(false);
  /** The highlighted suggestion, or -1 for none. */
  let active = $state(-1);

  const parts = $derived(pathParts(value));
  /** Only a path gets suggestions: one being typed, or an empty field that lists /. */
  const asPath = $derived(value.startsWith("/") || (listRoot && value === ""));
  const listing = createQuery(() => ({
    queryKey: ["daemon", "storage", "browse", parts.parent] as const,
    queryFn: () => call<Schema<"Listing">>(api.GET("/storage/browse", { params: { query: { path: parts.parent } } })),
    enabled: focused && asPath,
    staleTime: 30_000,
    retry: false,
  }));
  const suggestions = $derived(listing.data && asPath ? folderSuggestions(listing.data.entries, parts.fragment) : []);
  const open = $derived(focused && !dismissed && suggestions.length > 0);

  function pick(name: string) {
    value = `${parts.parent}${name}/`;
    active = -1;
    dismissed = false;
    onpick?.(trimSlash(value));
  }

  function move(by: number) {
    dismissed = false;
    const n = suggestions.length;
    active = active < 0 ? (by > 0 ? 0 : n - 1) : (active + by + n) % n;
  }

  function keydown(e: KeyboardEvent & { currentTarget: HTMLInputElement }) {
    onkeydown?.(e);
    if (e.defaultPrevented || !suggestions.length) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      move(e.key === "ArrowDown" ? 1 : -1);
    } else if (!open) {
      return;
    } else if (e.key === "Enter" && active >= 0) {
      e.preventDefault();
      pick(suggestions[active]!);
    } else if (e.key === "Tab" && !e.shiftKey && (active >= 0 || parts.fragment || suggestions.length === 1)) {
      // Like a shell: Tab completes what was typed, or the one folder there is. Otherwise it moves on.
      e.preventDefault();
      pick(suggestions[Math.max(active, 0)]!);
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      dismissed = true;
      active = -1;
    }
  }
</script>

<div class="relative min-w-0 {cls}">
  <input
    {...rest}
    class="input"
    role="combobox"
    aria-autocomplete="list"
    aria-expanded={open}
    aria-controls={listId}
    aria-activedescendant={open && active >= 0 ? `${listId}-${active}` : undefined}
    autocomplete="off"
    spellcheck="false"
    bind:value
    oninput={(e) => {
      dismissed = false;
      active = -1;
      oninput?.(e);
    }}
    onfocus={(e) => {
      focused = true;
      onfocus?.(e);
    }}
    onblur={(e) => {
      focused = false;
      active = -1;
      value = trimSlash(value);
      onblur?.(e);
    }}
    onkeydown={keydown}
  />
  <ul
    id={listId}
    role="listbox"
    aria-label="Folders"
    class="absolute inset-x-0 top-full z-50 mt-1 max-h-64 overflow-auto rounded-md border border-line bg-surface p-1 shadow-lg"
    hidden={!open}
  >
    {#each suggestions as name, i (name)}
      <!-- The field keeps focus: the list is driven from its keys, and a press here must not blur it. -->
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <li
        id="{listId}-{i}"
        role="option"
        aria-selected={i === active}
        class="cursor-default truncate rounded px-2.5 py-1.5 {i === active ? 'bg-sunken' : 'hover:bg-sunken'}"
        onmousedown={(e) => e.preventDefault()}
        onclick={() => pick(name)}
      >
        <span class="text-muted">{parts.parent}</span>{name}
      </li>
    {/each}
  </ul>
</div>
