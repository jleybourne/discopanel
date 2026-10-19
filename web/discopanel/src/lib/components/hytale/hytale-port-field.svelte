<script lang="ts">
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { CheckCircle2, AlertCircle, Loader2 } from '@lucide/svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { ModuleProtocol } from '$lib/proto/discopanel/v1/storage_pb';

	let {
		port = $bindable(),
		error = $bindable(''),
		serverId = '',
		id = 'hytale-port',
		disabled = false
	}: {
		port: number;
		error?: string;
		serverId?: string;
		id?: string;
		disabled?: boolean;
	} = $props();

	let checking = $state(false);
	let free = $state(false);
	let seq = 0;

	// Debounced registry check, a number no other server uses on TCP or UDP
	$effect(() => {
		const value = Number(port);
		const mine = ++seq;
		free = false;
		if (!Number.isInteger(value) || value < 1 || value > 65535) {
			error = 'Port must be between 1 and 65535';
			checking = false;
			return;
		}
		error = '';
		checking = true;
		const timer = setTimeout(async () => {
			try {
				const res = await rpcClient.server.checkPortAvailable(
					{ port: value, protocol: ModuleProtocol.UNSPECIFIED, serverId },
					silentCallOptions
				);
				if (mine !== seq) return;
				free = res.available;
				error = res.available ? '' : res.reason || 'This port is in use';
			} catch {
				// No verdict without the registry, the server checks again on save
				if (mine === seq) error = '';
			} finally {
				if (mine === seq) checking = false;
			}
		}, 350);
		return () => clearTimeout(timer);
	});
</script>

<div class="space-y-2">
	<Label for={id}>UDP port</Label>
	<Input
		{id}
		type="number"
		min="1"
		max="65535"
		bind:value={port}
		{disabled}
		aria-invalid={error ? 'true' : undefined}
		aria-describedby="{id}-status"
	/>
	<p id="{id}-status" class="flex items-center gap-1.5 text-xs" aria-live="polite">
		{#if checking}
			<Loader2 class="size-3 animate-spin text-muted-foreground" />
			<span class="text-muted-foreground">Checking...</span>
		{:else if error}
			<AlertCircle class="size-3 text-status-danger" />
			<span class="text-status-danger">{error}</span>
		{:else if free}
			<CheckCircle2 class="size-3 text-status-ok" />
			<span class="text-status-ok">Free on this host</span>
		{/if}
	</p>
</div>
