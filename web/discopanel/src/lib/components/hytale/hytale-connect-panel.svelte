<script lang="ts">
	import { onMount } from 'svelte';
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import type { GetProxyStatusResponse } from '$lib/proto/discopanel/v1/proxy_pb';
	import { AddressSelect } from '$lib/components/app';
	import { Info } from '@lucide/svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { HYTALE, hytaleAddresses } from '$lib/game';

	let { server }: { server: Server } = $props();

	let proxyStatus = $state<GetProxyStatusResponse | null>(null);
	onMount(async () => {
		try {
			proxyStatus = await rpcClient.proxy.getProxyStatus({}, silentCallOptions);
		} catch {
			// Detection failures keep the browser host fallback
		}
	});

	let addresses = $derived(hytaleAddresses(server.port, proxyStatus));
	let radius = $derived(server.hytale?.maxViewRadius ?? 0);
</script>

<div class="flex flex-col overflow-hidden rounded-xl border bg-card">
	<div class="flex items-center justify-between px-5 pt-4">
		<span class="stat-label">Connect</span>
		<span class="text-xs text-muted-foreground">UDP {server.port}</span>
	</div>
	<div class="space-y-3 px-5 py-4">
		<AddressSelect {addresses} />
		<p class="text-xs leading-relaxed text-muted-foreground">
			Hytale connects over QUIC (UDP). Forward UDP {server.port} on your router; TCP isn't needed. Players
			must type the port, since Hytale doesn't read SRV records yet, and the hostname proxy is Minecraft
			only.
		</p>
	</div>
	<div class="mt-auto flex items-start gap-2 border-t px-5 py-4 text-xs text-muted-foreground">
		<Info class="mt-0.5 size-3.5 shrink-0" />
		{#if radius > 0}
			<span>
				View radius {radius} chunks{radius > HYTALE.recommendedViewRadius
					? `, above the recommended ${HYTALE.recommendedViewRadius}. Expect much higher memory use.`
					: '.'}
			</span>
		{:else}
			<span>View radius shows here once the server has written config.json.</span>
		{/if}
	</div>
</div>
