<script lang="ts">
	import { untrack } from 'svelte';
	import { guardUnsavedChanges } from '$lib/utils/unsaved-guard.svelte';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Switch } from '$lib/components/ui/switch';
	import { Badge } from '$lib/components/ui/badge';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { ConfirmDialog, EmptyState, SectionCard } from '$lib/components/app';
	import MemorySlider from '$lib/components/memory-slider.svelte';
	import HytalePortField from './hytale-port-field.svelte';
	import {
		AlertTriangle,
		Eye,
		EyeOff,
		KeyRound,
		Loader2,
		Plus,
		RotateCcw,
		Save,
		X
	} from '@lucide/svelte';
	import { ConnectError, Code } from '@connectrpc/connect';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import {
		HytaleAuthState,
		HytaleAuthStateSchema,
		ServerStatus,
		type Server
	} from '$lib/proto/discopanel/v1/storage_pb';
	import { HytaleWhitelistFormat, type HytaleConfig } from '$lib/proto/discopanel/v1/hytale_pb';
	import { enumLabel } from '$lib/proto-meta';
	import { HYTALE, HYTALE_PATCHLINES, patchlineLabel } from '$lib/game';
	import { isUp } from '$lib/server-status';

	let { server, onUpdate }: { server: Server; onUpdate?: () => void | Promise<void> } = $props();

	// Editable fields, config.json plus the panel's own server row
	interface Form {
		serverName: string;
		motd: string;
		password: string;
		maxPlayers: number;
		maxViewRadius: number;
		whitelistEnabled: boolean;
		whitelist: string[];
		patchline: string;
		reinstall: boolean;
		name: string;
		memory: number;
		memoryMin: number;
		memoryMax: number;
		port: number;
		autoStart: boolean;
	}

	let config = $state<HytaleConfig | null>(null);
	let loading = $state(true);
	let loadError = $state('');
	// No server properties read: config.json and whitelist stay hidden
	let configDenied = $state(false);
	let saving = $state(false);
	let showPassword = $state(false);
	let newEntry = $state('');
	let portError = $state('');
	let signInOpen = $state(false);
	let hostTotalMb = $state(0);
	let occupiedMb = $state(0);

	function formFrom(cfg: HytaleConfig | null): Form {
		return {
			serverName: cfg?.serverName ?? '',
			motd: cfg?.motd ?? '',
			password: cfg?.password ?? '',
			maxPlayers: cfg?.maxPlayers || server.maxPlayers,
			maxViewRadius: cfg?.maxViewRadius ?? 0,
			whitelistEnabled: cfg?.whitelistEnabled ?? false,
			whitelist: [...(cfg?.whitelist ?? [])],
			patchline: cfg?.patchline || 'release',
			reinstall: false,
			name: server.name,
			memory: server.memory,
			memoryMin: server.memoryMin,
			memoryMax: server.memoryMax,
			port: server.port,
			autoStart: server.autoStart
		};
	}

	let form = $state<Form>(formFrom(null));
	let baseline = $state.raw<Form>(formFrom(null));

	const CONFIG_KEYS = ['serverName', 'motd', 'password', 'maxPlayers', 'maxViewRadius'] as const;
	const ACCESS_KEYS = ['whitelistEnabled', 'whitelist'] as const;
	const PANEL_KEYS = ['name', 'memory', 'memoryMin', 'memoryMax', 'port', 'autoStart'] as const;
	const MEMORY_KEYS = ['memory', 'memoryMin', 'memoryMax'] as const;
	const ALL_KEYS = [
		...CONFIG_KEYS,
		...ACCESS_KEYS,
		'patchline',
		'reinstall',
		...PANEL_KEYS
	] as const;
	type Key = (typeof ALL_KEYS)[number];

	function dirtyKey(k: Key): boolean {
		return JSON.stringify($state.snapshot(form[k])) !== JSON.stringify(baseline[k]);
	}
	let dirtyKeys = $derived(ALL_KEYS.filter((k) => dirtyKey(k)));
	let dirty = $derived(dirtyKeys.length > 0);
	// The heap trio moves together, count it as one change
	let dirtyCount = $derived(
		dirtyKeys.filter((k) => k !== 'memoryMin' && k !== 'memoryMax').length +
			(dirtyKeys.some((k) => k === 'memoryMin' || k === 'memoryMax') &&
			!dirtyKeys.includes('memory')
				? 1
				: 0)
	);
	guardUnsavedChanges(() => dirty && !saving);

	let running = $derived(isUp(server.status) || server.status === ServerStatus.STARTING);
	let whitelistEditable = $derived(config?.whitelistFormat === HytaleWhitelistFormat.RECOGNIZED);
	let viewRadiusHigh = $derived(form.maxViewRadius > HYTALE.recommendedViewRadius);
	let errors = $derived.by(() => {
		const out: string[] = [];
		if (form.maxPlayers < 1 || form.maxPlayers > HYTALE.maxPlayersLimit)
			out.push(`Max players must be between 1 and ${HYTALE.maxPlayersLimit}`);
		if (
			dirtyKey('maxViewRadius') &&
			(form.maxViewRadius < 1 || form.maxViewRadius > HYTALE.maxViewRadius)
		)
			out.push(`Max view radius must be between 1 and ${HYTALE.maxViewRadius}`);
		if (form.memory < HYTALE.minMemoryMb) out.push('Hytale needs at least 4 GB of memory');
		if (!form.name.trim()) out.push('The server needs a name');
		if (dirtyKey('port') && portError) out.push(portError);
		return out;
	});

	let loadedFor = '';
	$effect(() => {
		void server;
		untrack(() => {
			if (loadedFor !== server.id) {
				loadedFor = server.id;
				void load();
				return;
			}
			if (!loading) rebaseOnServer();
		});
	});

	// Pull outside changes to the panel fields into untouched ones, keep edits
	function rebaseOnServer() {
		const fresh = formFrom(config);
		const next = { ...baseline };
		for (const k of PANEL_KEYS) {
			if (!dirtyKey(k)) (form as Record<Key, unknown>)[k] = fresh[k];
			(next as Record<Key, unknown>)[k] = fresh[k];
		}
		baseline = next;
	}

	async function load() {
		loading = true;
		loadError = '';
		configDenied = false;
		try {
			const [res, memory] = await Promise.all([
				rpcClient.hytale
					.getHytaleConfig({ serverId: server.id }, silentCallOptions)
					.catch((error) => {
						// Expected for roles without server properties read
						if (error instanceof ConnectError && error.code === Code.PermissionDenied) {
							configDenied = true;
							return null;
						}
						throw error;
					}),
				rpcClient.server.getHostMemory({}).catch(() => null)
			]);
			config = res?.config ?? null;
			if (memory) {
				hostTotalMb = Number(memory.totalMb);
				occupiedMb = memory.allocations
					.filter((a) => a.serverId !== server.id)
					.reduce((sum, a) => sum + a.memory, 0);
			}
			form = formFrom(config);
			baseline = formFrom(config);
		} catch (error) {
			loadError = rpcErrorMessage(error, 'Failed to load the Hytale settings');
		} finally {
			loading = false;
		}
	}

	function reset() {
		form = formFrom(config);
	}

	function addEntry() {
		const value = newEntry.trim();
		if (!value) return;
		if (form.whitelist.some((e) => e.toLowerCase() === value.toLowerCase())) {
			notify.info(`${value} is already listed`);
			return;
		}
		form.whitelist = [...form.whitelist, value];
		newEntry = '';
	}

	// Memory and port are container settings, saving them rebuilds a
	// running container right away instead of waiting for a restart
	const RESTART_KEYS: Key[] = ['memory', 'memoryMin', 'memoryMax', 'port'];
	let restartsNow = $derived(running && RESTART_KEYS.some((k) => dirtyKey(k)));
	let restartOpen = $state(false);
	let playersLabel = $derived(
		server.hytale?.playersKnown
			? `${server.playersOnline} ${server.playersOnline === 1 ? 'player' : 'players'}`
			: 'everyone who is connected'
	);

	function requestSave() {
		if (errors.length > 0) {
			notify.error(errors[0]);
			return;
		}
		if (restartsNow) {
			restartOpen = true;
			return;
		}
		void save();
	}

	async function save() {
		if (errors.length > 0) {
			notify.error(errors[0]);
			return;
		}
		const restarting = restartsNow;
		saving = true;
		try {
			const configDirty = [...CONFIG_KEYS, ...ACCESS_KEYS, 'patchline', 'reinstall'].some((k) =>
				dirtyKey(k as Key)
			);
			if (configDirty) {
				const res = await rpcClient.hytale.updateHytaleConfig({
					serverId: server.id,
					serverName: dirtyKey('serverName') ? form.serverName : undefined,
					motd: dirtyKey('motd') ? form.motd : undefined,
					password: dirtyKey('password') ? form.password : undefined,
					maxPlayers: dirtyKey('maxPlayers') ? form.maxPlayers : undefined,
					maxViewRadius: dirtyKey('maxViewRadius') ? form.maxViewRadius : undefined,
					patchline: dirtyKey('patchline') ? form.patchline : undefined,
					whitelistEnabled: dirtyKey('whitelistEnabled') ? form.whitelistEnabled : undefined,
					updateWhitelist: dirtyKey('whitelist'),
					whitelist: dirtyKey('whitelist') ? form.whitelist : [],
					reinstall: form.reinstall
				});
				config = res.config ?? config;
			}
			if (PANEL_KEYS.some((k) => dirtyKey(k))) {
				// Only changed fields, so stale values never overwrite another
				// admin's edit; the heap trio is sent whole or not at all
				const memoryDirty = MEMORY_KEYS.some((k) => dirtyKey(k));
				await rpcClient.server.updateServer({
					id: server.id,
					name: dirtyKey('name') ? form.name.trim() : '',
					memory: memoryDirty ? form.memory : 0,
					memoryMin: memoryDirty ? form.memoryMin : 0,
					memoryMax: memoryDirty ? form.memoryMax : 0,
					port: dirtyKey('port') ? form.port : undefined,
					autoStart: dirtyKey('autoStart') ? form.autoStart : undefined
				});
			}
			notify.success('Settings saved', {
				description: restarting
					? 'The server is restarting now to apply them'
					: running
						? 'They apply on the next restart'
						: 'They apply on the next start'
			});
			await onUpdate?.();
			form = formFrom(config);
			baseline = formFrom(config);
		} catch (error) {
			notify.error(rpcErrorMessage(error, 'Failed to save the settings'));
		} finally {
			saving = false;
		}
	}

	async function signInAgain() {
		try {
			const res = await rpcClient.hytale.startHytaleSignIn({ serverId: server.id });
			notify.success(res.message);
			await onUpdate?.();
		} catch (error) {
			notify.error(rpcErrorMessage(error, 'Could not request a sign-in'));
		}
	}

	let auth = $derived(server.hytale?.auth ?? HytaleAuthState.UNSPECIFIED);
</script>

{#snippet dot(k: Key)}
	{#if dirtyKey(k)}
		<span class="size-1.5 rounded-full bg-status-busy" title="Unsaved change"></span>
	{/if}
{/snippet}

{#snippet fieldLabel(k: Key, forId: string, text: string)}
	<div class="mb-1.5 flex items-center gap-1.5">
		<Label for={forId} class="text-xs font-medium text-muted-foreground">{text}</Label>
		{@render dot(k)}
	</div>
{/snippet}

{#if loading && !config}
	<div class="flex h-64 items-center justify-center">
		<Loader2 class="size-8 animate-spin text-muted-foreground" />
	</div>
{:else if loadError}
	<EmptyState icon={AlertTriangle} title="Could not load the settings" description={loadError}>
		<Button size="sm" variant="outline" onclick={load}>Try again</Button>
	</EmptyState>
{:else}
	<div class="flex min-h-0 flex-1 flex-col gap-3">
		{#if !configDenied && (config?.pendingRestart || running)}
			<p
				class="flex shrink-0 items-center gap-2 rounded-lg border border-status-busy/25 bg-status-busy/5 px-3 py-2 text-xs text-muted-foreground"
			>
				<AlertTriangle class="size-3.5 shrink-0 text-status-busy" />
				{#if !config?.configExists}
					The server writes config.json on its first start. Your settings are saved and applied on
					the start after that.
				{:else if running}
					The server rewrites its files while it runs, so changes are saved and applied on the next
					restart.
				{:else}
					Saved changes are applied on the next start.
				{/if}
			</p>
		{/if}

		<div class="min-h-0 flex-1 overflow-y-auto">
			<div class="grid items-start gap-4 px-1 py-1 lg:grid-cols-[minmax(0,1fr)_20rem]">
				<div class="min-w-0 space-y-4">
					{#if configDenied}
						<SectionCard title="Server and access" description="config.json, whitelist.json">
							<p class="text-sm text-muted-foreground">
								Viewing these needs the server properties permission. Ask an admin to change them.
							</p>
						</SectionCard>
					{:else}
						<SectionCard
							title="Server"
							description="config.json"
							contentClass="grid gap-4 sm:grid-cols-2"
						>
							<div class="sm:col-span-2">
								{@render fieldLabel('serverName', 'h-name', 'Server name')}
								<Input id="h-name" bind:value={form.serverName} maxlength={128} class="h-9" />
							</div>
							<div>
								{@render fieldLabel('maxPlayers', 'h-max', 'Max players')}
								<Input
									id="h-max"
									type="number"
									min="1"
									max={HYTALE.maxPlayersLimit}
									bind:value={form.maxPlayers}
									class="h-9"
								/>
							</div>
							<div>
								{@render fieldLabel('maxViewRadius', 'h-view', 'Max view radius (chunks)')}
								<Input
									id="h-view"
									type="number"
									min="1"
									max={HYTALE.maxViewRadius}
									bind:value={form.maxViewRadius}
									placeholder="Server default"
									class="h-9"
								/>
								<p
									class="mt-1.5 text-[11px] {viewRadiusHigh
										? 'text-status-busy'
										: 'text-muted-foreground'}"
								>
									{viewRadiusHigh
										? `Above the recommended ${HYTALE.recommendedViewRadius}. Expect much higher memory use.`
										: `The biggest driver of memory use, ${HYTALE.recommendedViewRadius} or less recommended.`}
								</p>
							</div>
							<div class="sm:col-span-2">
								{@render fieldLabel('motd', 'h-motd', 'Message of the day')}
								<Input id="h-motd" bind:value={form.motd} maxlength={512} class="h-9" />
							</div>
							<div class="sm:col-span-2">
								{@render fieldLabel('password', 'h-pass', 'Join password')}
								<div class="flex gap-2">
									<Input
										id="h-pass"
										type={showPassword ? 'text' : 'password'}
										autocomplete="new-password"
										bind:value={form.password}
										placeholder={config?.passwordHidden && config.passwordSet
											? 'Hidden, type a new one to replace it'
											: ''}
										maxlength={128}
										class="h-9"
									/>
									<Button
										variant="outline"
										size="icon"
										class="size-9 shrink-0"
										aria-label={showPassword ? 'Hide password' : 'Show password'}
										onclick={() => (showPassword = !showPassword)}
									>
										{#if showPassword}<EyeOff class="size-4" />{:else}<Eye class="size-4" />{/if}
									</Button>
								</div>
								<p class="mt-1.5 text-[11px] text-muted-foreground">
									{#if config?.passwordHidden}
										{config.passwordSet ? 'A password is set.' : 'No password is set.'} Seeing it needs
										the Files read permission.
									{:else}
										Leave empty for an open server.
									{/if}
								</p>
							</div>
						</SectionCard>

						<SectionCard title="Access" description="whitelist.json" contentClass="space-y-4">
							{#if !whitelistEditable}
								<p class="text-sm text-muted-foreground">
									{config?.whitelistFormat === HytaleWhitelistFormat.MISSING
										? 'whitelist.json appears after the first start. Edit it here once the server has created it.'
										: 'whitelist.json is in a format the panel does not edit. Change it in the Files tab.'}
								</p>
							{:else}
								<label class="flex items-center justify-between gap-4">
									<span class="min-w-0">
										<span class="flex items-center gap-1.5 text-sm font-medium">
											Whitelist {@render dot('whitelistEnabled')}
										</span>
										<span class="text-xs text-muted-foreground">
											Only listed players can join
										</span>
									</span>
									<Switch bind:checked={form.whitelistEnabled} />
								</label>
								<div class="space-y-2">
									<div class="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
										Players {@render dot('whitelist')}
									</div>
									{#if form.whitelist.length === 0}
										<p class="text-xs text-muted-foreground">Nobody is listed yet.</p>
									{:else}
										<ul class="flex flex-wrap gap-1.5">
											{#each form.whitelist as entry (entry)}
												<li
													class="inline-flex items-center gap-1 rounded-md border bg-muted/40 py-0.5 pr-1 pl-2 font-mono text-xs"
												>
													{entry}
													<button
														type="button"
														class="rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground"
														aria-label="Remove {entry}"
														onclick={() =>
															(form.whitelist = form.whitelist.filter((e) => e !== entry))}
													>
														<X class="size-3" />
													</button>
												</li>
											{/each}
										</ul>
									{/if}
									<form
										class="flex gap-2"
										onsubmit={(e) => {
											e.preventDefault();
											addEntry();
										}}
									>
										<Input
											bind:value={newEntry}
											placeholder="Entry as the server stores it"
											aria-label="Whitelist entry"
											maxlength={64}
											class="h-8"
										/>
										<Button type="submit" size="sm" variant="outline" disabled={!newEntry.trim()}>
											<Plus class="size-4" />
											Add player
										</Button>
									</form>
									<p class="text-[11px] text-muted-foreground">
										Entries are written exactly as typed and must match the format already in
										whitelist.json (names or UUIDs, as the server wrote them). To add players by
										name, use the server's own whitelist command in the console.
									</p>
								</div>
							{/if}
							<p class="border-t pt-3 text-xs text-muted-foreground">
								Permissions and bans are edited the same way in the Files tab (permissions.json,
								bans.json).
							</p>
						</SectionCard>
					{/if}

					<SectionCard
						title="Panel"
						description="How DiscoPanel runs this server"
						contentClass="space-y-4"
					>
						{#if running}
							<p class="text-xs text-status-busy">
								Changing the memory or the UDP port restarts the server as soon as you save.
							</p>
						{/if}
						<div class="grid gap-4 sm:grid-cols-2">
							<div>
								{@render fieldLabel('name', 'h-panel-name', 'Name in DiscoPanel')}
								<Input id="h-panel-name" bind:value={form.name} class="h-9" />
							</div>
							<div>
								<HytalePortField
									bind:port={form.port}
									bind:error={portError}
									serverId={server.id}
									id="h-port"
								/>
							</div>
						</div>
						<MemorySlider
							bind:memory={form.memory}
							bind:memoryMin={form.memoryMin}
							bind:memoryMax={form.memoryMax}
							totalMb={hostTotalMb}
							{occupiedMb}
							minMemory={HYTALE.minMemoryMb}
							disabled={saving}
							dirty={dirtyKey('memory') || dirtyKey('memoryMin') || dirtyKey('memoryMax')}
						/>
						<label class="flex items-center justify-between gap-4 rounded-lg border px-3 py-2.5">
							<span>
								<span class="flex items-center gap-1.5 text-sm font-medium">
									Auto start {@render dot('autoStart')}
								</span>
								<span class="text-xs text-muted-foreground">
									Start this server when DiscoPanel starts
								</span>
							</span>
							<Switch bind:checked={form.autoStart} />
						</label>
					</SectionCard>
				</div>

				<aside class="space-y-4">
					<SectionCard title="Hytale account" contentClass="space-y-3">
						<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
							<dt class="text-muted-foreground">Server</dt>
							<dd class="text-right">
								{#if auth === HytaleAuthState.SIGNED_IN}
									<Badge variant="secondary">Signed in</Badge>
								{:else if auth === HytaleAuthState.UNSPECIFIED}
									<span class="text-muted-foreground">Not confirmed yet</span>
								{:else}
									{enumLabel(HytaleAuthStateSchema, auth)}
								{/if}
							</dd>
							<dt class="text-muted-foreground">Persistence</dt>
							<dd class="text-right">
								{server.hytale?.persistenceEncrypted ? 'Encrypted' : 'Not saved yet'}
							</dd>
							<dt class="text-muted-foreground">Downloader</dt>
							<dd class="text-right">
								{server.hytale?.downloaderSignedIn ? 'Credentials saved' : 'Not signed in'}
							</dd>
						</dl>
						<p class="text-xs text-muted-foreground">
							Stays signed in while the server runs. If it's offline for more than 30 days you'll be
							asked to sign in again.
						</p>
						<Button
							size="sm"
							variant="outline"
							class="w-full"
							disabled={!running}
							title={running ? undefined : 'Start the server first'}
							onclick={() => (signInOpen = true)}
						>
							<KeyRound class="size-4" />
							Sign in again
						</Button>
					</SectionCard>

					<SectionCard title="Updates" contentClass="space-y-3">
						{#if configDenied}
							<p class="text-sm text-muted-foreground">
								The release channel needs the server properties permission.
							</p>
						{:else}
							<div>
								{@render fieldLabel('patchline', 'h-chan', 'Release channel')}
								<Select
									type="single"
									value={form.patchline}
									onValueChange={(v: string | undefined) => (form.patchline = v || 'release')}
								>
									<SelectTrigger id="h-chan" class="h-9 w-full">
										<span>{patchlineLabel(form.patchline)}</span>
									</SelectTrigger>
									<SelectContent>
										{#each HYTALE_PATCHLINES as p (p.value)}
											<SelectItem value={p.value}>{p.label}</SelectItem>
										{/each}
									</SelectContent>
								</Select>
								<p class="mt-1.5 text-[11px] text-muted-foreground">
									New server versions are downloaded on the next restart. Back up before switching
									to pre-release, worlds opened on newer builds may not open on older ones.
								</p>
							</div>
							<label class="flex items-center justify-between gap-3">
								<span class="text-sm">
									<span class="flex items-center gap-1.5">
										Reinstall on next start {@render dot('reinstall')}
									</span>
									<span class="block text-[11px] text-muted-foreground">
										Downloads the server files again, signing in if the downloader asks
									</span>
								</span>
								<Switch bind:checked={form.reinstall} />
							</label>
						{/if}
					</SectionCard>
				</aside>
			</div>
		</div>

		{#if dirty}
			<div
				class="flex shrink-0 flex-wrap items-center justify-between gap-3 rounded-xl border bg-card px-4 py-3"
			>
				<span class="text-sm font-medium">
					{dirtyCount} unsaved {dirtyCount === 1 ? 'change' : 'changes'}
					<span class="font-normal text-muted-foreground">
						· applied on {running ? 'restart' : 'next start'}
					</span>
				</span>
				<div class="flex items-center gap-2">
					<Button variant="outline" size="sm" onclick={reset} disabled={saving}>
						<RotateCcw class="size-4" />
						Discard
					</Button>
					<Button
						size="sm"
						onclick={requestSave}
						disabled={saving || errors.length > 0}
						class="min-w-28"
					>
						{#if saving}
							<Loader2 class="size-4 animate-spin" />
						{:else}
							<Save class="size-4" />
						{/if}
						Save
					</Button>
				</div>
			</div>
			{#if errors.length > 0}
				<p class="shrink-0 text-xs text-status-danger">{errors[0]}</p>
			{/if}
		{/if}
	</div>
{/if}

<ConfirmDialog
	bind:open={restartOpen}
	title="Restart {server.name} now?"
	description="Changing the memory or the port rebuilds the container, so saving restarts the server now and disconnects {playersLabel}. Other changes are applied during that restart."
	confirmLabel="Save and restart"
	destructive
	onConfirm={save}
/>

<ConfirmDialog
	bind:open={signInOpen}
	title="Sign the server in again?"
	description="The server prints a new device code. The current login keeps working until the new sign-in is approved."
	confirmLabel="Sign in again"
	onConfirm={signInAgain}
/>
