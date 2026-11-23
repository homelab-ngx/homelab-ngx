<script lang="ts">
  import { page } from "$app/state";
  import { DEMO } from "#lib/api/client.ts";
  import { fetchStack } from "#lib/api/queries.ts";
  import { copyText, isCopyKey, MONO, refusal, REFUSED, ShellSession, terminalTheme } from "#lib/shell.ts";

  // The shell window: a popup the stack page opens, with nothing in it but
  // the terminal. Closing the window hangs the shell up.
  const name = $derived(page.params.name ?? "");
  let host = $state<HTMLDivElement>();

  $effect(() => {
    if (DEMO || !host) return;
    let stop: (() => void) | undefined;
    let gone = false;
    void attach(name, host).then((s) => {
      if (gone) s();
      else stop = s;
    });
    return () => {
      gone = true;
      stop?.();
    };
  });

  async function attach(stack: string, el: HTMLElement): Promise<() => void> {
    const { init, Terminal, FitAddon } = await import("ghostty-web");
    await init();
    // The canvas measures its cells once; the font has to be there first.
    await document.fonts.load(`13px ${MONO}`).catch(() => undefined);
    const css = getComputedStyle(document.documentElement);
    const term = new Terminal({
      fontSize: 13,
      fontFamily: MONO,
      cursorBlink: true,
      scrollback: 5000,
      theme: terminalTheme((t) => css.getPropertyValue(`--${t}`).trim()),
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    fit.fit();
    fit.observeResize();

    const mac = /Mac|iPhone|iPad/.test(navigator.platform);
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown" || !isCopyKey(e, mac)) return false;
      if (term.hasSelection()) void copyText(term.getSelection());
      return true;
    });
    // Every paste goes in as one: bracketed when the shell asks for it, so
    // a pasted script is not run line by line.
    const onPaste = (e: ClipboardEvent) => {
      e.preventDefault();
      e.stopImmediatePropagation();
      const text = e.clipboardData?.getData("text/plain");
      if (text) term.paste(text);
    };
    window.addEventListener("paste", onPaste, { capture: true });

    let session: ShellSession | undefined;
    const connect = () => {
      session = new ShellSession(stack, {
        output: (bytes) => term.write(bytes),
        opened: () => {
          session?.resize(term.cols, term.rows);
          term.focus();
        },
        closed: (reason) => void explain(stack, reason).then((why) => term.write(`\r\n\x1b[2m${why} Enter reconnects.\x1b[0m\r\n`)),
      });
    };
    term.onData((data) => {
      if (session?.status !== "closed") session?.send(data);
      else if (data === "\r") connect();
    });
    term.onResize(({ cols, rows }) => session?.resize(cols, rows));
    connect();
    term.focus();

    return () => {
      window.removeEventListener("paste", onPaste, { capture: true });
      session?.close();
      term.dispose();
    };
  }

  async function explain(stack: string, reason: string): Promise<string> {
    if (reason !== REFUSED) return reason;
    return refusal(await fetchStack(stack).catch(() => null)) ?? reason;
  }
</script>

<svelte:head><title>{name} shell</title></svelte:head>

{#if DEMO}
  <p class="p-4 text-sm text-muted">Not available in the demo.</p>
{:else}
  <div bind:this={host} class="h-dvh w-full overflow-hidden p-2"></div>
{/if}
