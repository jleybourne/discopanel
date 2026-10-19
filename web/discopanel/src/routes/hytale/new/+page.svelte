<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { Badge } from '$lib/components/ui/badge';
	import { PageHeader, SectionCard, ServerAvatar } from '$lib/components/app';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import { serversStore } from '$lib/stores/servers';
	import { Loader2, Rocket } from '@lucide/svelte';
	import { ModLoader } from '$lib/proto/discopanel/v1/storage_pb';
	import type { GetProxyStatusResponse } from '$lib/proto/discopanel/v1/proxy_pb';
	import HytaleCreateFields from '$lib/components/hytale/hytale-create-fields.svelte';
	import { HYTALE, hytaleAddresses, patchlineLabel } from '$lib/game';

	let loading = $state(false);
	let status = $state<GetProxyStatusResponse | null>(null);
	let hostTotalMb = $state(0);
	let occupiedMb = $state(0);

	let form = $state({
		name: '',
		description: '',
		patchline: 'release',
		maxPlayers: HYTALE.defaultMaxPlayers as number,
		viewRadius: HYTALE.recommendedViewRadius as number,
		port: HYTALE.defaultPort as number,
		portError: '',
		memory: 6144,
		memoryMin: 3072,
		memoryMax: 4608,
		// Both sign-ins happen on the first start
		startImmediately: true,
		detached: false,
		autoStart: false
	});

	onMount(async () => {
		// Both only refine hints, the form works without them
		const [proxyStatus, hostMemory] = await Promise.allSettled([
			rpcClient.proxy.getProxyStatus({}, silentCallOptions),
			rpcClient.server.getHostMemory({}, silentCallOptions)
		]);
		if (proxyStatus.status === 'fulfilled') status = proxyStatus.value;
		if (hostMemory.status === 'fulfilled') {
			hostTotalMb = Number(hostMemory.value.totalMb);
			occupiedMb = hostMemory.value.allocations.reduce((sum, a) => sum + a.memory, 0);
		}
	});

	let address = $derived(hytaleAddresses(form.port, status)[0]);
	let invalid = $derived(
		!!form.portError ||
			form.memory < HYTALE.minMemoryMb ||
			form.maxPlayers < 1 ||
			form.maxPlayers > HYTALE.maxPlayersLimit ||
			form.viewRadius < 1 ||
			form.viewRadius > HYTALE.maxViewRadius
	);
	let canSubmit = $derived(!loading && form.name.trim().length > 0 && !invalid);

	async function handleSubmit(e: Event) {
		e.preventDefault();
		if (!form.name.trim()) {
			notify.error('Server name is required');
			return;
		}
		if (invalid) {
			notify.error(form.portError || 'Check the highlighted Hytale settings');
			return;
		}
		loading = true;
		try {
			const response = await rpcClient.server.createServer({
				name: form.name.trim(),
				description: form.description,
				modLoader: ModLoader.HYTALE,
				port: form.port,
				maxPlayers: form.maxPlayers,
				memory: form.memory,
				memoryMin: form.memoryMin,
				memoryMax: form.memoryMax,
				autoStart: form.autoStart,
				detached: form.detached,
				startImmediately: form.startImmediately,
				hytale: { patchline: form.patchline, maxViewRadius: form.viewRadius }
			});
			const created = response.server;
			if (!created) throw new Error('The panel returned no server');
			serversStore.addServer(created);
			notify.success(`Server "${created.name}" created!`);
			goto(resolve(`/hytale/${created.id}`));
		} catch (error) {
			notify.error(`Failed to create server: ${rpcErrorMessage(error, 'Unknown error')}`);
		} finally {
			loading = false;
		}
	}
</script>

<svelte:head>
	<title>New Hytale server · DiscoPanel</title>
</svelte:head>

{#snippet createButton(fullWidth: boolean)}
	<Button
		type="submit"
		form="create-hytale-form"
		disabled={!canSubmit}
		class="glow-primary {fullWidth ? 'w-full' : 'min-w-36'}"
	>
		{#if loading}
			<Loader2 class="size-4 animate-spin" />
			Creating...
		{:else}
			<Rocket class="size-4" />
			{form.startImmediately ? 'Create and start' : 'Create server'}
		{/if}
	</Button>
{/snippet}

<div class="border-b bg-card/40">
	<div class="mx-auto w-full max-w-6xl px-4 pt-5 sm:px-6 2xl:max-w-7xl">
		<PageHeader
			title="Create a Hytale server"
			description="Set up a new official Hytale dedicated server"
			class="pb-4"
		>
			<Badge variant="secondary" class="text-[10px]">Early access</Badge>
		</PageHeader>
	</div>
</div>

<div class="mx-auto w-full max-w-6xl space-y-5 p-4 sm:p-6 2xl:max-w-7xl">
	<div class="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_19rem]">
		<form id="create-hytale-form" onsubmit={handleSubmit} class="min-w-0 space-y-4">
			<HytaleCreateFields
				bind:name={form.name}
				bind:description={form.description}
				bind:patchline={form.patchline}
				bind:maxPlayers={form.maxPlayers}
				bind:viewRadius={form.viewRadius}
				bind:port={form.port}
				bind:portError={form.portError}
				bind:memory={form.memory}
				bind:memoryMin={form.memoryMin}
				bind:memoryMax={form.memoryMax}
				{hostTotalMb}
				{occupiedMb}
				{address}
				disabled={loading}
			/>

			<SectionCard
				title="Lifecycle"
				description="When the server starts and stops"
				contentClass="grid gap-3 sm:grid-cols-3"
			>
				<label
					class="flex cursor-pointer flex-col gap-1.5 rounded-lg border p-3 text-sm transition-colors hover:bg-accent/30"
				>
					<span class="flex items-center justify-between gap-2">
						<span class="font-medium">Start immediately</span>
						<Switch bind:checked={form.startImmediately} disabled={loading} />
					</span>
					<span class="text-xs font-normal text-muted-foreground">
						Boot right away so you can sign in
					</span>
				</label>

				<label
					class="flex cursor-pointer flex-col gap-1.5 rounded-lg border p-3 text-sm transition-colors hover:bg-accent/30"
				>
					<span class="flex items-center justify-between gap-2">
						<span class="font-medium">Detached mode</span>
						<Switch
							bind:checked={form.detached}
							disabled={loading}
							onCheckedChange={(checked) => {
								form.detached = checked;
								if (checked) form.autoStart = false;
							}}
						/>
					</span>
					<span class="text-xs font-normal text-muted-foreground">
						Keeps running when DiscoPanel stops
					</span>
				</label>

				<label
					class="flex cursor-pointer flex-col gap-1.5 rounded-lg border p-3 text-sm transition-colors hover:bg-accent/30"
				>
					<span class="flex items-center justify-between gap-2">
						<span class="font-medium">Auto start</span>
						<Switch
							bind:checked={form.autoStart}
							disabled={loading || form.detached}
							onCheckedChange={(checked) => {
								if (form.detached) {
									notify.error('Cannot enable auto-start for detached servers');
									form.autoStart = false;
									return;
								}
								form.autoStart = checked;
							}}
						/>
					</span>
					<span class="text-xs font-normal text-muted-foreground">
						Starts with DiscoPanel{form.detached ? '. Disabled for detached servers.' : '.'}
					</span>
				</label>
			</SectionCard>
		</form>

		<aside class="sticky top-4 hidden space-y-3 lg:block">
			<div class="overflow-hidden rounded-xl border bg-card">
				<div class="border-b bg-muted/30 px-4 py-2.5">
					<span class="stat-label">Summary</span>
				</div>
				<div class="space-y-3 p-4">
					<div class="flex items-center gap-3">
						<ServerAvatar name={form.name.trim() || '?'} size="lg" />
						<div class="min-w-0">
							<p class="truncate text-sm font-semibold">
								{form.name.trim() || 'Unnamed server'}
							</p>
							<p class="truncate text-xs text-muted-foreground">
								Hytale ({patchlineLabel(form.patchline).toLowerCase()})
							</p>
						</div>
					</div>

					<dl class="space-y-2 border-t pt-3 text-xs">
						<div class="flex items-center justify-between gap-2">
							<dt class="text-muted-foreground">Runtime</dt>
							<dd class="font-medium">Java {HYTALE.javaMajor}</dd>
						</div>
						<div class="flex items-center justify-between gap-2">
							<dt class="text-muted-foreground">Address</dt>
							<dd class="max-w-36 truncate font-mono font-medium" title={address}>
								{address}
							</dd>
						</div>
						<div class="flex items-center justify-between gap-2">
							<dt class="text-muted-foreground">Memory</dt>
							<dd class="tabular font-medium">{(form.memory / 1024).toFixed(1)} GB</dd>
						</div>
						<div class="flex items-center justify-between gap-2">
							<dt class="text-muted-foreground">After creation</dt>
							<dd class="font-medium">
								{form.startImmediately ? 'Starts right away' : 'Stays stopped'}
							</dd>
						</div>
					</dl>
					<p class="rounded-md bg-muted/50 px-2.5 py-1.5 text-[11px] text-muted-foreground">
						On first start you'll sign in with your Hytale account twice: once so the panel can
						download the server files, and once to authenticate the server.
					</p>

					{#if !form.name.trim()}
						<p class="rounded-md bg-muted/50 px-2.5 py-1.5 text-[11px] text-muted-foreground">
							Give the server a name to create it
						</p>
					{:else if form.portError}
						<p class="rounded-md bg-status-danger/10 px-2.5 py-1.5 text-[11px] text-status-danger">
							{form.portError}
						</p>
					{/if}

					{@render createButton(true)}
					<Button variant="ghost" href={resolve('/hytale')} disabled={loading} class="w-full">
						Cancel
					</Button>
				</div>
			</div>
		</aside>
	</div>

	<div
		class="sticky bottom-4 z-10 flex items-center justify-between gap-3 rounded-xl border bg-card/95 px-4 py-3 shadow-lg backdrop-blur-sm lg:hidden"
	>
		<div class="min-w-0">
			<p class="truncate text-sm font-medium">{form.name.trim() || 'Unnamed server'}</p>
			<p class="truncate font-mono text-xs text-muted-foreground">{address}</p>
		</div>
		<div class="flex shrink-0 gap-2">
			<Button variant="outline" href={resolve('/hytale')} disabled={loading}>Cancel</Button>
			{@render createButton(false)}
		</div>
	</div>
</div>
