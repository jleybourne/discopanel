<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Switch } from '$lib/components/ui/switch';
	import { Progress } from '$lib/components/ui/progress';
	import { EmptyState, ConfirmDialog } from '$lib/components/app';
	import { Loader2, Upload, Trash2, Package, X, AlertTriangle, RefreshCw } from '@lucide/svelte';
	import { rpcClient, rpcErrorMessage } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import { HytaleModKind, type HytaleMod } from '$lib/proto/discopanel/v1/hytale_pb';
	import { formatBytes } from '$lib/utils';
	import { uploadFile, cancelUpload, type UploadProgress } from '$lib/utils/chunked-upload';

	let { server, active = false }: { server: Server; active?: boolean } = $props();

	type Filter = 'all' | 'plugins' | 'packs';

	let mods = $state<HytaleMod[]>([]);
	let loading = $state(true);
	let loadError = $state('');
	let filter = $state<Filter>('all');
	let busy = $state<string | null>(null);
	let uploading = $state(false);
	let uploadProgress = $state<UploadProgress | null>(null);
	let currentUpload = $state('');
	let abort: AbortController | null = null;
	let fileInput = $state<HTMLInputElement | null>(null);
	let dragActive = $state(false);

	let deleteTarget = $state<HytaleMod | null>(null);
	let deleteOpen = $state(false);
	let disableTarget = $state<HytaleMod | null>(null);
	let disableOpen = $state(false);

	let loadedFor = '';
	$effect(() => {
		if (active && loadedFor !== server.id) {
			loadedFor = server.id;
			mods = [];
			loading = true;
			void loadMods();
		}
	});

	async function loadMods() {
		loading = true;
		loadError = '';
		try {
			const res = await rpcClient.hytale.listHytaleMods({ serverId: server.id });
			mods = res.mods;
		} catch (error) {
			loadError = rpcErrorMessage(error, 'Failed to load mods');
		} finally {
			loading = false;
		}
	}

	const isPlugin = (m: HytaleMod) => m.kind === HytaleModKind.PLUGIN;
	let plugins = $derived(mods.filter(isPlugin).length);
	let packs = $derived(mods.length - plugins);
	let visible = $derived(
		filter === 'all' ? mods : mods.filter((m) => (filter === 'plugins') === isPlugin(m))
	);
	const key = (m: HytaleMod) => `${m.enabled ? 'on' : 'off'}:${m.fileName}`;

	async function uploadFiles(list: FileList | File[]) {
		const files = Array.from(list).filter((f) => /\.(jar|zip)$/i.test(f.name));
		if (files.length === 0) {
			notify.error('Only .jar plugins and .zip asset packs are supported');
			return;
		}
		uploading = true;
		abort = new AbortController();
		let placed = 0;
		try {
			for (const file of files) {
				currentUpload = file.name;
				uploadProgress = null;
				const result = await uploadFile(file, {
					onProgress: (p) => (uploadProgress = p),
					signal: abort.signal
				});
				try {
					await rpcClient.hytale.importHytaleMod({
						serverId: server.id,
						uploadSessionId: result.sessionId
					});
					placed++;
				} catch (error) {
					notify.error(`${file.name}: ${rpcErrorMessage(error, 'upload failed')}`);
				}
			}
			if (placed > 0) {
				notify.success(`Uploaded ${placed} ${placed === 1 ? 'file' : 'files'}`, {
					description: 'Changes apply on the next restart'
				});
			}
		} catch (error) {
			if (error instanceof Error && error.message === 'Upload cancelled') {
				notify.info('Upload cancelled');
			} else {
				notify.error(rpcErrorMessage(error, 'Upload failed'));
			}
		} finally {
			uploading = false;
			uploadProgress = null;
			currentUpload = '';
			abort = null;
			await loadMods();
		}
	}

	function cancelCurrentUpload() {
		abort?.abort();
		if (uploadProgress?.sessionId) cancelUpload(uploadProgress.sessionId).catch(() => {});
	}

	async function setEnabled(mod: HytaleMod, enabled: boolean) {
		busy = key(mod);
		try {
			await rpcClient.hytale.setHytaleModEnabled({
				serverId: server.id,
				fileName: mod.fileName,
				enabled
			});
			notify.success(`${mod.fileName} ${enabled ? 'enabled' : 'disabled'}`, {
				description: 'Applies on the next restart'
			});
		} catch (error) {
			notify.error(rpcErrorMessage(error, 'Failed to change the mod'));
		} finally {
			busy = null;
			await loadMods();
		}
	}

	function toggle(mod: HytaleMod) {
		// A world that needs a missing plugin will not load, ask first
		if (mod.enabled && isPlugin(mod)) {
			disableTarget = mod;
			disableOpen = true;
			return;
		}
		void setEnabled(mod, !mod.enabled);
	}

	async function confirmDelete() {
		if (!deleteTarget) return;
		try {
			// The flag picks the copy when both folders hold the name
			await rpcClient.hytale.deleteHytaleMod({
				serverId: server.id,
				fileName: deleteTarget.fileName,
				enabled: deleteTarget.enabled
			});
			notify.success(`${deleteTarget.fileName} deleted`);
		} catch (error) {
			notify.error(rpcErrorMessage(error, 'Failed to delete the mod'));
		} finally {
			await loadMods();
		}
	}

	function handleDrop(e: DragEvent) {
		e.preventDefault();
		dragActive = false;
		if (!uploading && e.dataTransfer?.files?.length) void uploadFiles(e.dataTransfer.files);
	}
</script>

<div
	role="region"
	aria-label="Mods upload area"
	ondragover={(e) => {
		e.preventDefault();
		if (!uploading) dragActive = true;
	}}
	ondragleave={(e) => {
		if (e.currentTarget === e.target) dragActive = false;
	}}
	ondrop={handleDrop}
	class="relative flex min-h-0 flex-1 flex-col gap-3"
>
	{#if dragActive}
		<div
			class="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-xl border-2 border-dashed border-primary bg-primary/5 backdrop-blur-[1px]"
		>
			<div class="flex items-center gap-2 text-sm font-medium text-primary">
				<Upload class="size-4" />
				Drop .jar or .zip files to upload
			</div>
		</div>
	{/if}

	<p
		class="flex shrink-0 items-start gap-2 rounded-lg border border-status-busy/25 bg-status-busy/5 px-3 py-2 text-xs text-muted-foreground"
	>
		<AlertTriangle class="mt-0.5 size-3.5 shrink-0 text-status-busy" />
		Hytale won't load a world when a plugin it needs is missing. Disabling a plugin asks you to confirm
		first.
	</p>

	<section class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border bg-card">
		<header
			class="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3"
		>
			<div class="flex flex-wrap gap-1" role="group" aria-label="Filter mods">
				{#each [{ k: 'all', l: 'All', n: mods.length }, { k: 'plugins', l: 'Plugins', n: plugins }, { k: 'packs', l: 'Asset packs', n: packs }] as f (f.k)}
					<Button
						size="sm"
						variant={filter === f.k ? 'secondary' : 'ghost'}
						aria-pressed={filter === f.k}
						onclick={() => (filter = f.k as Filter)}
					>
						{f.l} <span class="tabular text-muted-foreground">({f.n})</span>
					</Button>
				{/each}
			</div>
			<div class="flex items-center gap-2">
				<Button
					size="icon"
					variant="ghost"
					class="size-8"
					title="Refresh"
					aria-label="Refresh"
					disabled={loading}
					onclick={loadMods}
				>
					<RefreshCw class="size-4 {loading ? 'animate-spin' : ''}" />
				</Button>
				<Button size="sm" disabled={uploading} onclick={() => fileInput?.click()}>
					{#if uploading}
						<Loader2 class="size-4 animate-spin" />
					{:else}
						<Upload class="size-4" />
					{/if}
					Upload .jar or .zip
				</Button>
				<input
					bind:this={fileInput}
					type="file"
					multiple
					accept=".jar,.zip"
					class="hidden"
					onchange={async (e) => {
						const input = e.currentTarget as HTMLInputElement;
						if (input.files?.length) await uploadFiles(input.files);
						input.value = '';
					}}
				/>
			</div>
		</header>

		{#if uploading && uploadProgress}
			<div class="shrink-0 border-b px-4 py-3">
				<div class="mb-2 flex items-center justify-between gap-2">
					<span class="truncate text-sm text-muted-foreground">Uploading {currentUpload}</span>
					<div class="flex shrink-0 items-center gap-2">
						<span class="tabular text-sm text-muted-foreground">
							{uploadProgress.percentComplete.toFixed(0)}%
						</span>
						<Button
							size="icon"
							variant="ghost"
							class="size-6"
							onclick={cancelCurrentUpload}
							title="Cancel upload"
							aria-label="Cancel upload"
						>
							<X class="size-4" />
						</Button>
					</div>
				</div>
				<Progress value={uploadProgress.percentComplete} class="h-1.5" />
			</div>
		{/if}

		{#if loading && mods.length === 0}
			<div class="flex min-h-40 flex-1 items-center justify-center">
				<Loader2 class="size-8 animate-spin text-muted-foreground" />
			</div>
		{:else if loadError}
			<div class="flex min-h-40 flex-1 flex-col justify-center">
				<EmptyState icon={AlertTriangle} title="Could not list mods" description={loadError}>
					<Button size="sm" variant="outline" onclick={loadMods}>Try again</Button>
				</EmptyState>
			</div>
		{:else if visible.length === 0}
			<div class="flex min-h-40 flex-1 flex-col justify-center">
				<EmptyState
					icon={Package}
					title={mods.length === 0 ? 'No plugins or asset packs' : 'Nothing in this filter'}
					description="Plugins are .jar files, asset packs are .zip files. Upload them or drop them here."
				>
					<Button size="sm" disabled={uploading} onclick={() => fileInput?.click()}>
						<Upload class="size-4" />
						Upload
					</Button>
				</EmptyState>
			</div>
		{:else}
			<div class="min-h-0 flex-1 overflow-y-auto">
				<div
					class="hidden grid-cols-[minmax(0,1fr)_7rem_5rem_4.5rem_3rem] gap-3 border-b px-4 py-2 text-xs font-medium text-muted-foreground sm:grid"
				>
					<span>Name</span>
					<span>Type</span>
					<span class="text-right">Size</span>
					<span>Enabled</span>
					<span class="sr-only">Actions</span>
				</div>
				<ul class="divide-y">
					{#each visible as mod (key(mod))}
						<li
							class="grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-x-3 gap-y-1 px-4 py-2.5 sm:grid-cols-[minmax(0,1fr)_7rem_5rem_4.5rem_3rem] {mod.enabled
								? ''
								: 'opacity-60'}"
						>
							<span class="min-w-0 truncate font-mono text-sm" title={mod.fileName}>
								{mod.fileName}
							</span>
							<span class="hidden sm:block">
								<Badge variant="secondary" class="text-xs">
									{isPlugin(mod) ? 'Plugin' : 'Asset pack'}
								</Badge>
							</span>
							<span class="tabular hidden text-right text-xs text-muted-foreground sm:block">
								{formatBytes(Number(mod.size))}
							</span>
							<!-- Follows mod.enabled, so a cancelled or failed toggle springs back -->
							<Switch
								bind:checked={() => mod.enabled, () => toggle(mod)}
								disabled={busy === key(mod)}
								aria-label="{mod.enabled ? 'Disable' : 'Enable'} {mod.fileName}"
							/>
							<Button
								size="icon"
								variant="ghost"
								class="size-8 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
								title="Delete"
								aria-label="Delete {mod.fileName}"
								onclick={() => {
									deleteTarget = mod;
									deleteOpen = true;
								}}
							>
								<Trash2 class="size-4" />
							</Button>
							<span class="col-span-3 text-xs text-muted-foreground sm:hidden">
								{isPlugin(mod) ? 'Plugin' : 'Asset pack'} · {formatBytes(Number(mod.size))}
							</span>
						</li>
					{/each}
				</ul>
			</div>
		{/if}
		<p class="shrink-0 border-t px-4 py-2.5 text-xs text-muted-foreground">
			Files live in mods/, disabled ones move to mods_disabled/. Changes apply on the next restart.
			Backups include universe/ and mods/.
		</p>
	</section>
</div>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete {deleteTarget?.fileName ?? 'mod'}?"
	description="The file is removed from the server. A world that uses this plugin may stop loading. This cannot be undone."
	confirmLabel="Delete"
	destructive
	onConfirm={confirmDelete}
/>

<ConfirmDialog
	bind:open={disableOpen}
	title="Disable {disableTarget?.fileName ?? 'plugin'}?"
	description="If a world uses this plugin, Hytale won't load that world until you enable it again. The change applies on the next restart."
	confirmLabel="Disable"
	destructive
	onConfirm={async () => {
		if (disableTarget) await setEnabled(disableTarget, false);
	}}
/>
