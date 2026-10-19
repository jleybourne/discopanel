<script lang="ts">
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import { HytaleAuthState, ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import { isUp, statusMeta, TONE_TEXT } from '$lib/server-status';
	import { formatUptime } from '$lib/utils/time';
	import { Equalizer } from '$lib/components/app';
	import { Button } from '$lib/components/ui/button';
	import { KeyRound, Loader2, ShieldCheck, ShieldAlert, Users } from '@lucide/svelte';
	import { rpcClient, rpcErrorMessage } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import { hytaleStatusLabel } from './hytale-status';

	let {
		server,
		now = new Date(),
		onUpdate
	}: { server: Server; now?: Date; onUpdate?: () => void | Promise<void> } = $props();

	let meta = $derived(statusMeta(server.status));
	let label = $derived(hytaleStatusLabel(server) ?? meta.label);
	let animated = $derived(server.status !== ServerStatus.STOPPED && meta.tone !== 'danger');
	let up = $derived(isUp(server.status));
	let hy = $derived(server.hytale);
	let maxPlayers = $derived(server.maxPlayersSlp || server.maxPlayers);
	let requesting = $state(false);

	async function signIn() {
		requesting = true;
		try {
			const res = await rpcClient.hytale.startHytaleSignIn({ serverId: server.id });
			notify.success(res.message);
			await onUpdate?.();
		} catch (error) {
			notify.error(rpcErrorMessage(error, 'Could not request a sign-in'));
		} finally {
			requesting = false;
		}
	}
</script>

<div class="flex flex-col overflow-hidden rounded-xl border bg-card">
	<div class="flex items-center justify-between px-5 pt-4">
		<span class="stat-label">Status</span>
		{#if server.status === ServerStatus.RUNNING && server.lastStarted}
			<span class="tabular text-xs text-muted-foreground">
				up {formatUptime(server.lastStarted, now)}
			</span>
		{/if}
	</div>
	<div class="flex flex-1 flex-col items-center justify-center gap-3 px-5 py-6">
		<Equalizer tone={meta.tone} size="lg" bars={5} {animated} />
		<div class="text-center">
			<div class="text-xl font-semibold {TONE_TEXT[meta.tone]}">{label}</div>
			<p class="mt-1 text-xs text-muted-foreground">{meta.desc}</p>
		</div>
	</div>
	<div class="space-y-2 border-t px-5 py-4 text-xs">
		<div class="flex items-center justify-between gap-2">
			<span class="flex items-center gap-1.5 text-muted-foreground">
				<Users class="size-3.5" />
				Players
			</span>
			{#if !up}
				<span class="font-mono text-muted-foreground">--</span>
			{:else if hy?.playersKnown}
				<span class="tabular font-semibold">{server.playersOnline} / {maxPlayers}</span>
			{:else}
				<span class="text-muted-foreground" title="The panel has not recognised a join line yet">
					Unknown / {maxPlayers}
				</span>
			{/if}
		</div>
		<div class="flex items-start justify-between gap-2">
			<span class="flex items-center gap-1.5 text-muted-foreground">
				{#if hy?.auth === HytaleAuthState.SIGNED_IN}
					<ShieldCheck class="size-3.5 text-status-ok" />
				{:else}
					<ShieldAlert class="size-3.5 text-status-busy" />
				{/if}
				Hytale account
			</span>
			<span class="text-right">
				{#if hy?.auth === HytaleAuthState.SIGNED_IN}
					Signed in{hy.persistenceEncrypted ? ', persistence encrypted' : ', not persisted yet'}
				{:else if hy?.auth === HytaleAuthState.WAITING}
					Waiting for sign-in
				{:else if hy?.auth === HytaleAuthState.SIGNED_OUT}
					<span class="text-status-busy">Not signed in, players cannot join</span>
				{:else if up}
					<span
						class="text-muted-foreground"
						title="The panel has not seen the server report its sign-in state since it started or since the panel restarted. Settings has Sign in again if players cannot join."
					>
						Not confirmed yet
					</span>
				{:else}
					<span class="text-muted-foreground">
						{hy?.persistenceEncrypted ? 'Login saved, encrypted' : 'Signs in on first start'}
					</span>
				{/if}
			</span>
		</div>
		{#if up && hy?.auth === HytaleAuthState.SIGNED_OUT}
			<Button size="sm" variant="outline" class="w-full" disabled={requesting} onclick={signIn}>
				{#if requesting}
					<Loader2 class="size-4 animate-spin" />
				{:else}
					<KeyRound class="size-4" />
				{/if}
				Sign in the server
			</Button>
		{/if}
	</div>
</div>
