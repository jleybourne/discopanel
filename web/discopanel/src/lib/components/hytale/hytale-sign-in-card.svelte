<script lang="ts">
	import { onMount } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { CopyButton } from '$lib/components/app';
	import { Check, ExternalLink, KeyRound, Loader2 } from '@lucide/svelte';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import type { HytaleSignIn } from '$lib/proto/discopanel/v1/storage_pb';

	let { signIn }: { signIn: HytaleSignIn } = $props();

	let now = $state(Date.now());
	onMount(() => {
		const timer = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(timer);
	});

	// Links come from console output, only Hytale https pages open
	let safeUrl = $derived.by(() => {
		try {
			const url = new URL(signIn.url);
			return url.protocol === 'https:' &&
				(url.hostname === 'hytale.com' || url.hostname.endsWith('.hytale.com'))
				? url.href
				: '';
		} catch {
			return '';
		}
	});

	let expiresAt = $derived(signIn.expiresAt ? timestampDate(signIn.expiresAt).getTime() : 0);
	let left = $derived(Math.max(0, Math.floor((expiresAt - now) / 1000)));
	let countdown = $derived(`${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`);

	let downloader = $derived(signIn.step === 1);
</script>

<section
	class="overflow-hidden rounded-xl border border-status-busy/30 bg-card"
	aria-labelledby="hytale-signin-title"
>
	<div class="grid gap-5 p-5 md:grid-cols-[minmax(0,1fr)_16rem]">
		<div class="min-w-0 space-y-3">
			<p class="text-xs font-medium tracking-wide text-status-busy uppercase">
				Step {signIn.step} of 2 · {downloader ? 'Download server files' : 'Authenticate the server'}
			</p>
			<h2 id="hytale-signin-title" class="text-lg font-semibold">Sign in to Hytale</h2>
			<p class="text-sm text-muted-foreground">
				{#if signIn.withheld}
					A sign-in is waiting for approval. The code signs the server into the operator's Hytale
					account, so it is only shown to people who can run commands on this server.
				{:else if downloader}
					The Hytale downloader needs your account to fetch the server files. Open the sign-in page
					and enter this code. The panel continues on its own once you approve.
				{:else}
					The server needs a Hytale account before players can join. Open the sign-in page and enter
					this code. The panel then turns on encrypted login persistence so restarts stay signed in.
				{/if}
			</p>
			<div class="flex flex-wrap gap-2">
				{#if signIn.withheld}
					<!-- Nothing to open without the code -->
				{:else if safeUrl}
					<Button href={safeUrl} target="_blank" rel="noopener noreferrer" size="sm">
						<ExternalLink class="size-4" />
						Open sign-in page
					</Button>
					<CopyButton text={safeUrl} label="Copy link" size="sm" variant="outline" />
				{:else}
					<p class="text-sm text-muted-foreground">
						Open the sign-in link printed in the console and enter the code.
					</p>
				{/if}
			</div>
		</div>

		<div class="flex flex-col justify-center gap-2 rounded-lg bg-muted/40 p-4 text-center">
			<span class="stat-label">{signIn.withheld ? 'Code' : 'Your code'}</span>
			{#if signIn.code}
				<div class="flex items-center justify-center gap-1">
					<span class="font-mono text-2xl font-semibold tracking-widest break-all select-all">
						{signIn.code}
					</span>
					<CopyButton text={signIn.code} label="Copy code" />
				</div>
			{:else}
				<span class="text-sm text-muted-foreground">
					{signIn.withheld ? 'Hidden for your role' : 'Shown on the sign-in page'}
				</span>
			{/if}
			<p class="flex items-center justify-center gap-1.5 text-xs text-muted-foreground">
				{#if left > 0}
					<Loader2 class="size-3 animate-spin" />
					Expires in <span class="tabular">{countdown}</span> · waiting for approval
				{:else}
					This code expired. Cancel the start and start again for a new one.
				{/if}
			</p>
		</div>
	</div>

	<ol class="grid gap-px border-t bg-border sm:grid-cols-2" aria-label="Sign-in steps">
		<li class="flex gap-3 bg-card px-5 py-3">
			<span
				class="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold {downloader
					? 'bg-status-busy/15 text-status-busy'
					: 'bg-status-ok/15 text-status-ok'}"
			>
				{#if downloader}1{:else}<Check class="size-3" />{/if}
			</span>
			<div class="min-w-0">
				<p class="text-sm font-medium">Downloader sign-in</p>
				<p class="text-xs text-muted-foreground">
					Saved with the server for future updates, so this only happens once.
				</p>
			</div>
		</li>
		<li class="flex gap-3 bg-card px-5 py-3">
			<span
				class="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold {downloader
					? 'bg-muted text-muted-foreground'
					: 'bg-status-busy/15 text-status-busy'}"
			>
				<KeyRound class="size-3" />
			</span>
			<div class="min-w-0">
				<p class="text-sm font-medium">Server sign-in</p>
				<p class="text-xs text-muted-foreground">
					After the download. The panel then turns on encrypted login persistence so restarts stay
					signed in.
				</p>
			</div>
		</li>
	</ol>
</section>
