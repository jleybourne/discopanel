<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import {
		DropdownMenu,
		DropdownMenuContent,
		DropdownMenuItem,
		DropdownMenuSeparator,
		DropdownMenuTrigger
	} from '$lib/components/ui/dropdown-menu';
	import {
		PageHeader,
		StatusBadge,
		ServerAvatar,
		EmptyState,
		ConfirmDialog
	} from '$lib/components/app';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
	import { serversStore, sortServersByActivity, claimFullStats } from '$lib/stores/servers';
	import { canStart, canStop, canRestart, isUp, TONE_BADGE } from '$lib/server-status';
	import { formatUptime } from '$lib/utils/time';
	import { runServerAction, type ServerOp } from '$lib/server-actions';
	import { notify } from '$lib/stores/activity.svelte';
	import { isHytale, patchlineLabel } from '$lib/game';
	import { hytaleStatusLabel } from '$lib/components/hytale/hytale-status';
	import {
		Plus,
		Play,
		Square,
		RotateCw,
		RefreshCcw,
		MoreHorizontal,
		Trash2,
		Gamepad2,
		Users,
		MemoryStick,
		AlertTriangle
	} from '@lucide/svelte';
	import { type Server, ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';

	const DELETE_WARNING =
		'This permanently removes the container, all server files, the world, every mod and the server sign-in.\nThis cannot be undone.';

	let servers = $derived(sortServersByActivity($serversStore.filter((s) => isHytale(s))));
	let initialLoading = $state(true);
	let loadError = $state('');
	let actioningId = $state('');
	let deleteTarget = $state<Server | null>(null);
	let deleteOpen = $state(false);
	let now = $state(new Date());

	async function load() {
		try {
			await serversStore.fetchServers($serversStore.length > 0, true);
			loadError = '';
		} catch (error) {
			loadError = rpcErrorMessage(error, 'Could not load servers');
		} finally {
			initialLoading = false;
		}
	}

	onMount(() => {
		const release = claimFullStats();
		void load();
		const clock = setInterval(() => (now = new Date()), 30000);
		return () => {
			release();
			clearInterval(clock);
		};
	});

	async function handleAction(action: ServerOp, server: Server) {
		actioningId = server.id;
		await runServerAction(action, server);
		actioningId = '';
	}

	function requestDelete(server: Server) {
		deleteTarget = server;
		deleteOpen = true;
	}

	async function confirmDelete() {
		if (!deleteTarget) return;
		try {
			await rpcClient.server.deleteServer({ id: deleteTarget.id }, silentCallOptions);
			serversStore.removeServer(deleteTarget.id);
			notify.success(`Deleted ${deleteTarget.name}`);
		} catch (error) {
			notify.error(error instanceof Error ? error.message : 'Failed to delete server');
		} finally {
			deleteTarget = null;
		}
	}
</script>

<svelte:head>
	<title>Hytale · DiscoPanel</title>
</svelte:head>

<div class="border-b bg-card/40">
	<div class="mx-auto w-full max-w-6xl px-4 pt-5 sm:px-6 2xl:max-w-7xl">
		<PageHeader
			title="Hytale"
			description="Official Hytale dedicated servers, early access"
			class="pb-4"
		>
			<Button href={resolve('/hytale/new')} size="sm" class="glow-primary h-8">
				<Plus class="size-4" />
				<span class="hidden sm:inline">New Hytale server</span>
			</Button>
		</PageHeader>
	</div>
</div>

<div class="mx-auto w-full max-w-6xl space-y-5 p-4 sm:p-6 2xl:max-w-7xl">
	{#if initialLoading && servers.length === 0}
		<div class="space-y-3">
			<Skeleton class="h-16 rounded-lg" />
			<Skeleton class="h-16 rounded-lg" />
		</div>
	{:else if loadError && servers.length === 0}
		<div class="rounded-xl border bg-card">
			<EmptyState icon={AlertTriangle} title="Could not load servers" description={loadError}>
				<Button variant="outline" onclick={load}>Try again</Button>
			</EmptyState>
		</div>
	{:else if servers.length === 0}
		<div class="rounded-xl border bg-card">
			<EmptyState
				icon={Gamepad2}
				title="No Hytale servers yet"
				description="The panel downloads the official server on first start. You'll sign in with your Hytale account twice: once for the download and once to authenticate the server."
			>
				<Button href={resolve('/hytale/new')} class="glow-primary">
					<Plus class="size-4" />
					Create Hytale server
				</Button>
			</EmptyState>
		</div>
	{:else}
		<div class="overflow-hidden rounded-xl border bg-card">
			<div class="divide-y">
				{#each servers as server (server.id)}
					{@const busy = actioningId === server.id}
					{@const signInLabel = hytaleStatusLabel(server)}
					<div
						class="group relative flex items-center gap-3 px-3 py-3 transition-colors hover:bg-accent/40 sm:px-4"
					>
						<ServerAvatar name={server.name} favicon={server.favicon} size="md" />
						<div class="min-w-0 flex-1">
							<div class="flex flex-wrap items-center gap-2">
								<a
									href={resolve(`/hytale/${server.id}`)}
									class="truncate text-sm font-medium after:absolute after:inset-0"
								>
									{server.name}
								</a>
								<StatusBadge status={server.status} />
								{#if signInLabel}
									<span
										class="inline-flex shrink-0 items-center rounded-full border px-2 py-0.5 text-xs font-medium {TONE_BADGE.busy}"
									>
										{signInLabel}
									</span>
								{/if}
							</div>
							<div class="mt-0.5 flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
								<span>{server.mcVersion || 'not installed yet'}</span>
								<span>·</span>
								<span>{patchlineLabel(server.hytale?.patchline).toLowerCase()}</span>
								<span>·</span>
								<span class="font-mono">:{server.port}/udp</span>
								{#if server.status === ServerStatus.RUNNING && server.lastStarted}
									<span>·</span>
									<span>up {formatUptime(server.lastStarted, now)}</span>
								{/if}
							</div>
						</div>

						<div class="tabular hidden shrink-0 items-center gap-4 text-xs md:flex">
							{#if isUp(server.status)}
								<span
									class="flex w-16 items-center gap-1.5 text-muted-foreground"
									title="Players online"
								>
									<Users class="size-3.5 shrink-0" />
									{server.hytale?.playersKnown ? server.playersOnline : '?'}/{server.maxPlayers}
								</span>
								<span
									class="hidden w-14 items-center gap-1.5 text-muted-foreground lg:flex"
									title="Memory in use"
								>
									<MemoryStick class="size-3.5 shrink-0" />
									{Number(server.memoryUsage) > 0
										? `${(Number(server.memoryUsage) / 1024).toFixed(1)}G`
										: '--'}
								</span>
							{:else}
								<span class="w-16"></span>
								<span class="hidden w-14 lg:block"></span>
							{/if}
						</div>

						<div
							class="relative z-10 flex shrink-0 items-center gap-1 opacity-80 transition-opacity group-hover:opacity-100"
						>
							{#if canStart(server.status)}
								<Button
									variant="ghost"
									size="icon"
									class="size-8 text-status-ok hover:bg-status-ok/10 hover:text-status-ok"
									disabled={busy}
									title="Start"
									onclick={() => handleAction('start', server)}
								>
									<Play class="size-4" />
								</Button>
							{:else if canStop(server.status)}
								<Button
									variant="ghost"
									size="icon"
									class="size-8 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
									disabled={busy}
									title={server.status === ServerStatus.PROVISIONING ? 'Cancel start' : 'Stop'}
									onclick={() => handleAction('stop', server)}
								>
									<Square class="size-4" />
								</Button>
							{/if}
							{#if canRestart(server.status)}
								<Button
									variant="ghost"
									size="icon"
									class="hidden size-8 sm:inline-flex"
									disabled={busy}
									title="Restart"
									onclick={() => handleAction('restart', server)}
								>
									<RotateCw class="size-4" />
								</Button>
							{/if}
							<DropdownMenu>
								<DropdownMenuTrigger>
									{#snippet child({ props })}
										<Button
											{...props}
											variant="ghost"
											size="icon"
											class="size-8"
											disabled={busy}
											title="More"
										>
											<MoreHorizontal class="size-4" />
										</Button>
									{/snippet}
								</DropdownMenuTrigger>
								<DropdownMenuContent align="end">
									<DropdownMenuItem
										onclick={() => handleAction('recreate', server)}
										disabled={busy}
									>
										<RefreshCcw class="mr-2 size-4" />
										Recreate container
									</DropdownMenuItem>
									<DropdownMenuSeparator />
									<DropdownMenuItem
										variant="destructive"
										onclick={() => requestDelete(server)}
										disabled={busy}
									>
										<Trash2 class="mr-2 size-4" />
										Delete server
									</DropdownMenuItem>
								</DropdownMenuContent>
							</DropdownMenu>
						</div>
					</div>
				{/each}
			</div>
		</div>
		<p class="text-xs text-muted-foreground">
			{servers.length}
			{servers.length === 1 ? 'Hytale server' : 'Hytale servers'}
		</p>
	{/if}
</div>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete {deleteTarget?.name ?? 'server'}?"
	description={DELETE_WARNING}
	confirmLabel="Delete server"
	destructive
	onConfirm={confirmDelete}
/>
