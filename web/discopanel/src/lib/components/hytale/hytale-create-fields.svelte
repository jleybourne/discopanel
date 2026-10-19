<script lang="ts">
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { SectionCard } from '$lib/components/app';
	import MemorySlider from '$lib/components/memory-slider.svelte';
	import HytalePortField from './hytale-port-field.svelte';
	import { HYTALE, HYTALE_PATCHLINES } from '$lib/game';

	let {
		name = $bindable(),
		description = $bindable(),
		patchline = $bindable(),
		maxPlayers = $bindable(),
		viewRadius = $bindable(),
		port = $bindable(),
		portError = $bindable(''),
		memory = $bindable(),
		memoryMin = $bindable(),
		memoryMax = $bindable(),
		hostTotalMb = 0,
		occupiedMb = 0,
		address = '',
		disabled = false
	}: {
		name: string;
		description: string;
		patchline: string;
		maxPlayers: number;
		viewRadius: number;
		port: number;
		portError?: string;
		memory: number;
		memoryMin: number;
		memoryMax: number;
		hostTotalMb?: number;
		occupiedMb?: number;
		address?: string;
		disabled?: boolean;
	} = $props();

	let channel = $derived(
		HYTALE_PATCHLINES.find((p) => p.value === patchline) ?? HYTALE_PATCHLINES[0]
	);
	let radiusHigh = $derived(viewRadius > HYTALE.recommendedViewRadius);
</script>

<SectionCard
	title="Basics"
	description="Name, release channel and capacity"
	contentClass="grid gap-4 sm:grid-cols-2"
>
	<div class="space-y-2 sm:col-span-2">
		<Label for="hy-name">Server name <span class="text-destructive">*</span></Label>
		<Input id="hy-name" placeholder="Orbis Survival" bind:value={name} required {disabled} />
	</div>
	<div class="space-y-2">
		<Label for="hy-channel">Release channel</Label>
		<Select
			type="single"
			value={patchline}
			onValueChange={(v: string | undefined) => (patchline = v || 'release')}
			{disabled}
		>
			<SelectTrigger id="hy-channel" class="w-full">
				<span>{channel.label} ({channel.hint})</span>
			</SelectTrigger>
			<SelectContent>
				{#each HYTALE_PATCHLINES as p (p.value)}
					<SelectItem value={p.value}>{p.label} ({p.hint})</SelectItem>
				{/each}
			</SelectContent>
		</Select>
	</div>
	<div class="space-y-2">
		<Label for="hy-players">Max players</Label>
		<Input
			id="hy-players"
			type="number"
			min="1"
			max={HYTALE.maxPlayersLimit}
			bind:value={maxPlayers}
			{disabled}
		/>
	</div>
	<div class="space-y-2">
		<Label for="hy-view">Max view radius (chunks)</Label>
		<Input
			id="hy-view"
			type="number"
			min="1"
			max={HYTALE.maxViewRadius}
			bind:value={viewRadius}
			{disabled}
		/>
		<p class="text-xs {radiusHigh ? 'text-status-busy' : 'text-muted-foreground'}">
			{radiusHigh
				? `Above the recommended ${HYTALE.recommendedViewRadius}. Expect much higher memory use.`
				: `The biggest driver of memory use, ${HYTALE.recommendedViewRadius} or less recommended.`}
		</p>
	</div>
	<div class="space-y-2">
		<Label for="hy-desc">
			Description <span class="text-xs text-muted-foreground">(optional)</span>
		</Label>
		<Textarea id="hy-desc" bind:value={description} {disabled} class="min-h-9 resize-none" />
	</div>
	<p class="text-xs text-muted-foreground sm:col-span-2">
		The in-game name, max players and view radius are written to config.json once the server has
		created it on its first start, so they take effect from the next restart.
	</p>
</SectionCard>

<SectionCard
	title="Connectivity"
	description="How players will reach the server"
	contentClass="grid gap-4 sm:grid-cols-[12rem_minmax(0,1fr)]"
>
	<HytalePortField bind:port bind:error={portError} {disabled} id="hy-port" />
	<p class="text-xs leading-relaxed text-muted-foreground sm:pt-7">
		Hytale connects over QUIC (UDP), so each server gets its own port and players join with
		<span class="font-mono">{address || `your-ip:${port}`}</span>. The hostname proxy is Minecraft
		only, and Hytale doesn't support DNS SRV records yet.
	</p>
</SectionCard>

<SectionCard
	title="Memory"
	description="How much of the host's memory this server gets"
	contentClass="space-y-3"
>
	<MemorySlider
		bind:memory
		bind:memoryMin
		bind:memoryMax
		totalMb={hostTotalMb}
		{occupiedMb}
		minMemory={HYTALE.minMemoryMb}
		{disabled}
	/>
	<p class="text-xs {memory < HYTALE.minMemoryMb ? 'text-status-danger' : 'text-muted-foreground'}">
		Hytale needs at least 4 GB.
	</p>
</SectionCard>
