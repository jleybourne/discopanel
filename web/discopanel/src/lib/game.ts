import { ModLoader, type Server } from '$lib/proto/discopanel/v1/storage_pb';
import { directAddresses, type AddressSource } from '$lib/hostname';
import { panelHost } from '$lib/utils/host';

export type GameKey = 'minecraft' | 'hytale';

// Game a server hosts, mirrors minecraft.IsHytale on the backend
export function gameOf(server: Pick<Server, 'modLoader'> | null | undefined): GameKey {
	return server?.modLoader === ModLoader.HYTALE ? 'hytale' : 'minecraft';
}

export function isHytale(server: Pick<Server, 'modLoader'> | null | undefined): boolean {
	return gameOf(server) === 'hytale';
}

export const GAME_LABEL: Record<GameKey, string> = {
	minecraft: 'Minecraft: Java Edition',
	hytale: 'Hytale'
};

// Hytale defaults and limits, kept in step with pkg/hytale
export const HYTALE = {
	defaultPort: 5520,
	minMemoryMb: 4096,
	javaMajor: 25,
	defaultMaxPlayers: 100,
	recommendedViewRadius: 12,
	maxViewRadius: 64,
	maxPlayersLimit: 10000
} as const;

export const HYTALE_PATCHLINES = [
	{ value: 'release', label: 'Release', hint: 'recommended' },
	{ value: 'pre-release', label: 'Pre-release', hint: 'early builds, may break worlds' }
] as const;

export function patchlineLabel(value: string | undefined): string {
	return HYTALE_PATCHLINES.find((p) => p.value === value)?.label ?? 'Release';
}

// Hytale reads no SRV records, every address carries the port
export function hytaleAddresses(port: number, src: AddressSource | null): string[] {
	const withPort = (host: string) => `${host}:${port}`;
	if (src) {
		const hosts = directAddresses(0, src.lanIp, src.publicIp, [src.effectiveBaseUrl]);
		if (hosts.length > 0) return hosts.map(withPort);
	}
	return [withPort(panelHost())];
}
