# Deferred feature requests

Planned, not started. Each entry records what v3 already has and the
agreed shape of the work so it can be picked up later.

## #120 Datapack management

Request: manage Minecraft datapacks from the panel instead of uploading
zips by hand through the Files tab.

What v3 already has:
- Files tab upload into `<world>/datapacks` works.
- `pkg/minecraft/datapacks.go` has `FindDatapackZips`, `EnableDatapack`,
  `DisableDatapack` and `FindDatapackRefs`, but nothing outside the
  package calls them and there is no datapack RPC.
- CurseForge pack installs already place datapack content (class 6945)
  into `datapacks` (`internal/provisioner/curseforge.go` `cfClassDir`).

Plan:
1. RPCs `ListDatapacks`, `UploadDatapack`, `ToggleDatapack`,
   `DeleteDatapack` (in `mod.proto` or a new service) with RBAC
   annotations, built on `pkg/minecraft/datapacks.go`. Resolve the folder
   from `level-name` in `server.properties`.
2. A Datapacks panel next to Mods on the server page: list zips and
   folders with enable/disable, drag-and-drop upload, delete with confirm.
3. Optional: Modrinth search with `project_type:datapack` through
   `pkg/indexers/modrinth`.
4. Tell the user changes need `/reload` or a restart; offer a Reload
   button when the server is running.

Effort: medium.

## #139 Global memory quota

Request: an admin-set cap on total memory allocated across all servers,
enforced when servers are created or their memory is changed, so users
with create/update rights cannot overcommit the host.

What v3 already has:
- Only a frontend warning: `memory-slider.svelte` computes
  `overCommitted` and shows "Sharing X with other servers".
- No backend check in `CreateServer` / `UpdateServer`, no config key.
  (`internal/memlimit` only limits the panel's own Go heap.)

Plan:
1. Setting `minecraft.max_total_memory_mb` in config plus a DB-backed
   admin setting editable in Settings (0 = no cap).
2. In `CreateServer` and `UpdateServer` (memory changes), sum `Memory`
   over all servers except the one being updated; over the cap returns
   `CodeResourceExhausted` with a clear message. Admins may bypass.
   Apply the same check to every other server-creation path (modpack
   import, server import).
3. Show the cap in `memory-slider.svelte` as a hard limit with the
   remaining headroom.
4. Later: per-role quotas and a max server count as RBAC role
   attributes.

Effort: medium.
