import { HytaleAuthState, ServerStatus, type Server } from '$lib/proto/discopanel/v1/storage_pb';

// Status text that beats the generic label while a sign-in waits
export function hytaleStatusLabel(server: Server): string | null {
	const hy = server.hytale;
	if (!hy) return null;
	if (hy.signIn) return 'Waiting for sign-in';
	if (hy.auth === HytaleAuthState.WAITING) return 'Waiting for sign-in';
	if (
		hy.auth === HytaleAuthState.SIGNED_OUT &&
		(server.status === ServerStatus.RUNNING || server.status === ServerStatus.STARTING)
	) {
		return 'Not signed in';
	}
	return null;
}
