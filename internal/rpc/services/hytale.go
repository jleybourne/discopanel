package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/auth"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/hytaletracker"
	"github.com/discohaus/discopanel/internal/lifecycle"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/hytale"
	"github.com/discohaus/discopanel/pkg/javaversions"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/runtimespec"
	"github.com/discohaus/discopanel/pkg/transfer"
	"github.com/google/uuid"
)

// Compile-time check that HytaleService implements the interface
var _ discopanelv1connect.HytaleServiceHandler = (*HytaleService)(nil)

// Serves Hytale config, sign-in and mods
type HytaleService struct {
	store         *storage.Store
	docker        *docker.Client
	lifecycle     *lifecycle.Manager
	tracker       *hytaletracker.Tracker
	uploadManager *transfer.UploadManager
	enforcer      *rbac.Enforcer
	rec           *metrics.Recorder
	log           *logger.Logger
	// Panel data dir, Hytale's private per-server state lives under it
	panelDataDir string
}

func NewHytaleService(store *storage.Store, docker *docker.Client, lifecycleManager *lifecycle.Manager, tracker *hytaletracker.Tracker, uploadManager *transfer.UploadManager, enforcer *rbac.Enforcer, panelDataDir string, rec *metrics.Recorder, log *logger.Logger) *HytaleService {
	return &HytaleService{
		panelDataDir:  panelDataDir,
		store:         store,
		docker:        docker,
		lifecycle:     lifecycleManager,
		tracker:       tracker,
		uploadManager: uploadManager,
		enforcer:      enforcer,
		rec:           rec,
		log:           log,
	}
}

// Rejected on Minecraft-only RPCs called for a Hytale server
func errHytaleUnsupported(what string) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s is Minecraft only and not available for Hytale servers", what))
}

// Loads a server for a Minecraft-only rpc, refusing Hytale servers
func getMinecraftServer(ctx context.Context, store *storage.Store, id, what string) (*v1.Server, error) {
	server, err := getServer(ctx, store, id)
	if err != nil {
		return nil, err
	}
	if minecraft.IsHytale(server.ModLoader) {
		return nil, errHytaleUnsupported(what)
	}
	return server, nil
}

// Loads a server and refuses anything that is not Hytale
func (s *HytaleService) hytaleServer(ctx context.Context, id string) (*v1.Server, error) {
	server, err := getServer(ctx, s.store, id)
	if err != nil {
		return nil, err
	}
	if !minecraft.IsHytale(server.ModLoader) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this server does not run Hytale"))
	}
	return server, nil
}

// Reports whether the server container runs or is being started
func (s *HytaleService) serverActive(ctx context.Context, server *v1.Server) bool {
	if s.lifecycle != nil && s.lifecycle.Busy(server.Id) {
		return true
	}
	return s.containerActive(ctx, server)
}

// Container state alone, for callers already holding the transition slot
func (s *HytaleService) containerActive(ctx context.Context, server *v1.Server) bool {
	if server.ContainerId == "" {
		return false
	}
	status, err := s.docker.GetContainerStatus(ctx, server.ContainerId)
	if err != nil {
		return false
	}
	switch status {
	case v1.ServerStatus_SERVER_STATUS_STOPPED, v1.ServerStatus_SERVER_STATUS_ERROR:
		return false
	}
	return true
}

// Private dir of one server, outside its data dir
func hytalePrivateDir(panelDataDir string, server *v1.Server) (string, error) {
	dir, err := hytale.PrivateDir(panelDataDir, server.Id)
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, err)
	}
	return dir, nil
}

// Builds the settings view, pending edits shown over the files
func readHytaleConfig(server *v1.Server, privateDir string) (*v1.HytaleConfig, error) {
	dataDir := server.DataPath
	settings, err := hytale.ReadSettings(privateDir)
	if err != nil {
		return nil, err
	}
	obj, err := hytale.ReadObjectFile(filepath.Join(dataDir, hytale.ConfigFile))
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	view := hytale.NewObject()
	if obj != nil {
		view.Merge(obj)
	}
	pending, err := hytale.ReadPending(privateDir)
	if err != nil {
		return nil, err
	}
	if pending != nil {
		view.Merge(pending)
	}
	cfg := hytale.ConfigFromObject(view)
	if cfg.ServerName == "" && obj == nil {
		cfg.ServerName = server.Name
	}
	if cfg.MaxPlayers == 0 {
		cfg.MaxPlayers = int(server.MaxPlayers)
	}

	wl, err := hytale.ReadWhitelist(dataDir)
	if err != nil {
		return nil, err
	}
	if delta, err := hytale.ReadPendingWhitelist(privateDir); err == nil {
		wl.Overlay(delta)
	}
	return &v1.HytaleConfig{
		ServerName:       cfg.ServerName,
		Motd:             cfg.MOTD,
		Password:         cfg.Password,
		MaxPlayers:       int32(cfg.MaxPlayers),
		MaxViewRadius:    int32(cfg.MaxViewRadius),
		Patchline:        settings.Patchline,
		WhitelistFormat:  wl.Format,
		WhitelistEnabled: wl.Enabled,
		Whitelist:        wl.Entries,
		ConfigExists:     obj != nil,
		PendingRestart:   hytale.HasPending(privateDir),
		PasswordSet:      cfg.Password != "",
	}, nil
}

// The join password is file content, it needs Files read like config.json
func (s *HytaleService) redactPassword(ctx context.Context, serverID string, cfg *v1.HytaleConfig) {
	if s.allowed(ctx, optionsv1.ResourceType_RESOURCE_TYPE_FILES, optionsv1.ActionType_ACTION_TYPE_READ, serverID) {
		return
	}
	cfg.Password = ""
	cfg.PasswordHidden = true
}

// Reports whether the caller's roles grant an action, false on any doubt
func (s *HytaleService) allowed(ctx context.Context, res optionsv1.ResourceType, act optionsv1.ActionType, serverID string) bool {
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return false
	}
	if s.enforcer == nil {
		return true
	}
	ok, err := s.enforcer.Enforce(user.Roles, res, act, serverID)
	return err == nil && ok
}

func (s *HytaleService) GetHytaleConfig(ctx context.Context, req *connect.Request[v1.GetHytaleConfigRequest]) (*connect.Response[v1.GetHytaleConfigResponse], error) {
	server, err := s.hytaleServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	privateDir, err := hytalePrivateDir(s.panelDataDir, server)
	if err != nil {
		return nil, err
	}
	cfg, err := readHytaleConfig(server, privateDir)
	if err != nil {
		return nil, err
	}
	s.redactPassword(ctx, server.Id, cfg)
	return connect.NewResponse(&v1.GetHytaleConfigResponse{Config: cfg}), nil
}

// Writing config.json is a file write, the caller needs Files update too
func (s *HytaleService) requireFilesUpdate(ctx context.Context, serverID string) error {
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}
	if s.enforcer == nil {
		return nil
	}
	ok, err := s.enforcer.Enforce(user.Roles, optionsv1.ResourceType_RESOURCE_TYPE_FILES, optionsv1.ActionType_ACTION_TYPE_UPDATE, serverID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return connect.NewError(connect.CodePermissionDenied, errors.New("editing Hytale config files also needs the Files update permission"))
	}
	return nil
}

func (s *HytaleService) UpdateHytaleConfig(ctx context.Context, req *connect.Request[v1.UpdateHytaleConfigRequest]) (*connect.Response[v1.UpdateHytaleConfigResponse], error) {
	msg := req.Msg
	server, err := s.hytaleServer(ctx, msg.ServerId)
	if err != nil {
		return nil, err
	}
	if err := s.requireFilesUpdate(ctx, server.Id); err != nil {
		return nil, err
	}
	privateDir, err := hytalePrivateDir(s.panelDataDir, server)
	if err != nil {
		return nil, err
	}
	current, err := readHytaleConfig(server, privateDir)
	if err != nil {
		return nil, err
	}

	// Validate the merged result, not just the changed fields
	next := hytale.ServerConfig{
		ServerName:    current.ServerName,
		MOTD:          current.Motd,
		Password:      current.Password,
		MaxPlayers:    int(current.MaxPlayers),
		MaxViewRadius: int(current.MaxViewRadius),
	}
	patch := hytale.NewObject()
	if msg.ServerName != nil {
		next.ServerName = strings.TrimSpace(*msg.ServerName)
		_ = patch.Set(hytale.KeyServerName, next.ServerName)
	}
	if msg.Motd != nil {
		next.MOTD = *msg.Motd
		_ = patch.Set(hytale.KeyMOTD, next.MOTD)
	}
	if msg.Password != nil {
		next.Password = *msg.Password
		_ = patch.Set(hytale.KeyPassword, next.Password)
	}
	if msg.MaxPlayers != nil {
		next.MaxPlayers = int(*msg.MaxPlayers)
		_ = patch.Set(hytale.KeyMaxPlayers, next.MaxPlayers)
	}
	if msg.MaxViewRadius != nil {
		next.MaxViewRadius = int(*msg.MaxViewRadius)
		_ = patch.Set(hytale.KeyMaxViewRadius, next.MaxViewRadius)
	}
	if patch.Len() > 0 {
		// Unknown values from the file are not the user's to fix
		check := next
		if msg.MaxViewRadius == nil && check.MaxViewRadius == 0 {
			check.MaxViewRadius = 1
		}
		if err := hytale.ValidateConfig(check); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}

	// Whitelist edits are stored as additions and removals against what the
	// caller saw, so entries the server added meanwhile survive
	wlChanged := msg.WhitelistEnabled != nil || msg.UpdateWhitelist
	var wlAdd, wlRemove []string
	if wlChanged {
		if current.WhitelistFormat != v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_RECOGNIZED {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("whitelist.json does not exist yet or is in a format the panel does not edit, start the server once or change it in Files"))
		}
		if msg.UpdateWhitelist {
			entries, err := hytale.NormalizeWhitelist(msg.Whitelist)
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, err)
			}
			wlAdd, wlRemove = hytale.DiffWhitelist(current.Whitelist, entries)
		}
	}

	var patchline string
	if msg.Patchline != nil {
		if patchline, err = hytale.NormalizePatchline(*msg.Patchline); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}

	// Files written straight away only when nothing can race the write
	write := func(direct bool) error {
		if patch.Len() > 0 {
			if err := hytale.SavePending(privateDir, patch); err != nil {
				return err
			}
		}
		if wlChanged {
			if err := hytale.SavePendingWhitelist(privateDir, msg.WhitelistEnabled, wlAdd, wlRemove); err != nil {
				return err
			}
		}
		if direct {
			if _, err := hytale.ApplyPending(server.DataPath, privateDir); err != nil {
				s.log.Warn("Hytale config for %s stays pending: %v", server.Name, err)
			}
		}
		if patchline != "" {
			settings, err := hytale.ReadSettings(privateDir)
			if err != nil {
				return err
			}
			settings.Patchline = patchline
			if err := hytale.WriteSettings(privateDir, settings); err != nil {
				return err
			}
		}
		return nil
	}
	if s.serverActive(ctx, server) {
		err = write(false)
	} else {
		err = s.lifecycle.Exclusive(ctx, server.Id, "hytale-config", false, func() error {
			fresh, ferr := getServer(ctx, s.store, server.Id)
			if ferr != nil {
				return ferr
			}
			return write(!s.containerActive(ctx, fresh))
		})
		var busy *lifecycle.BusyError
		if errors.As(err, &busy) {
			err = write(false)
		}
	}
	if err != nil {
		s.log.Error("Failed to save Hytale config for %s: %v", server.Name, err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save the Hytale settings: %w", err))
	}

	fields := map[string]any{}
	if msg.MaxPlayers != nil && int32(next.MaxPlayers) != server.MaxPlayers {
		fields["max_players"] = int32(next.MaxPlayers)
	}
	if len(fields) > 0 {
		if err := s.store.UpdateServerFields(ctx, server.Id, fields); err != nil {
			s.log.Error("Failed to mirror max players for %s: %v", server.Name, err)
		}
	}
	if msg.Reinstall {
		cfg, err := s.store.GetServerProperties(ctx, server.Id)
		if err == nil {
			on := true
			cfg.ForceProvision = &on
			err = s.store.UpdateServerProperties(ctx, cfg)
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to schedule the reinstall: %w", err))
		}
	}

	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_PROPERTIES_UPDATE, metrics.Attrs{"file": hytale.ConfigFile}, "updated Hytale settings")
	fresh, _ := getServer(ctx, s.store, server.Id)
	if fresh == nil {
		fresh = server
	}
	cfg, err := readHytaleConfig(fresh, privateDir)
	if err != nil {
		return nil, err
	}
	s.redactPassword(ctx, server.Id, cfg)
	return connect.NewResponse(&v1.UpdateHytaleConfigResponse{Config: cfg}), nil
}

func (s *HytaleService) StartHytaleSignIn(ctx context.Context, req *connect.Request[v1.StartHytaleSignInRequest]) (*connect.Response[v1.StartHytaleSignInResponse], error) {
	server, err := s.hytaleServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	if server.ContainerId == "" || !s.serverActive(ctx, server) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("start the server first, the sign-in runs inside it"))
	}
	s.tracker.Track(server.Id, server.DataPath)
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.tracker.RequestSignIn(runCtx, server.Id); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("could not reach the server console: %w", err))
	}
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_COMMAND_RUN, metrics.Attrs{"command": hytaletracker.LoginCommand}, "requested a Hytale server sign-in")
	return connect.NewResponse(&v1.StartHytaleSignInResponse{Message: "Sign-in requested, the code appears on the server page in a few seconds"}), nil
}

// Maps mod file errors onto rpc codes
func hytaleModError(err error) error {
	switch {
	case errors.Is(err, hytale.ErrInvalidModName):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, hytale.ErrModExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, hytale.ErrModNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, hytale.ErrUnsafeModPath):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (s *HytaleService) ListHytaleMods(ctx context.Context, req *connect.Request[v1.ListHytaleModsRequest]) (*connect.Response[v1.ListHytaleModsResponse], error) {
	server, err := s.hytaleServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	mods, err := hytale.ListMods(server.DataPath)
	if err != nil {
		return nil, hytaleModError(err)
	}
	return connect.NewResponse(&v1.ListHytaleModsResponse{Mods: mods}), nil
}

func (s *HytaleService) ImportHytaleMod(ctx context.Context, req *connect.Request[v1.ImportHytaleModRequest]) (*connect.Response[v1.ImportHytaleModResponse], error) {
	server, err := s.hytaleServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	if req.Msg.UploadSessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload_session_id is required"))
	}
	tempPath, name, err := s.uploadManager.GetTempPath(req.Msg.UploadSessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session not found or not completed"))
	}
	mod, err := hytale.PlaceMod(server.DataPath, tempPath, name)
	if err != nil {
		return nil, hytaleModError(err)
	}
	s.uploadManager.CleanupSession(req.Msg.UploadSessionId)
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_MOD_INSTALL, metrics.Attrs{"file": mod.FileName}, "uploaded %s", mod.FileName)
	return connect.NewResponse(&v1.ImportHytaleModResponse{Mod: mod}), nil
}

func (s *HytaleService) SetHytaleModEnabled(ctx context.Context, req *connect.Request[v1.SetHytaleModEnabledRequest]) (*connect.Response[v1.SetHytaleModEnabledResponse], error) {
	server, err := s.hytaleServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	mod, err := hytale.SetModEnabled(server.DataPath, req.Msg.FileName, req.Msg.Enabled)
	if err != nil {
		return nil, hytaleModError(err)
	}
	kind, verb := v1.ServerActionKind_SERVER_ACTION_KIND_MOD_DISABLE, "disabled"
	if mod.Enabled {
		kind, verb = v1.ServerActionKind_SERVER_ACTION_KIND_MOD_ENABLE, "enabled"
	}
	s.rec.Record(ctx, server.Id, kind, metrics.Attrs{"file": mod.FileName}, "%s %s", verb, mod.FileName)
	return connect.NewResponse(&v1.SetHytaleModEnabledResponse{Mod: mod}), nil
}

func (s *HytaleService) DeleteHytaleMod(ctx context.Context, req *connect.Request[v1.DeleteHytaleModRequest]) (*connect.Response[v1.DeleteHytaleModResponse], error) {
	server, err := s.hytaleServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	if err := hytale.DeleteMod(server.DataPath, req.Msg.FileName, req.Msg.Enabled); err != nil {
		return nil, hytaleModError(err)
	}
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_MOD_DELETE, metrics.Attrs{"file": req.Msg.FileName}, "deleted %s", req.Msg.FileName)
	return connect.NewResponse(&v1.DeleteHytaleModResponse{}), nil
}

// Runtime tags that ship Java 25
func validHytaleImage(tag string) bool {
	return tag == "" || (docker.IsValidRuntimeTag(tag) && javaversions.TagMajor(tag) == hytale.JavaMajor)
}

// Reservations for a Hytale server, the number is held on TCP and UDP
func hytalePortRequests(port int, additional []*v1.NetworkPort, proxyOn bool) []proxy.NetRequest {
	return proxy.HytaleNetRequests(port, additional, proxyOn)
}

// Extra ports must not take over the game binding or ride the proxy
func validateHytaleExtraPorts(ports []*v1.NetworkPort) error {
	for _, p := range ports {
		if p.GetProxyEnabled() {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("proxied extra ports are Minecraft only"))
		}
		if p.GetContainerPort() == hytale.ContainerPort && p.GetProtocol() != v1.ModuleProtocol_MODULE_PROTOCOL_TCP {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("container port %d/udp is the Hytale game port, change the server's UDP port instead", hytale.ContainerPort))
		}
	}
	return nil
}

// Creates a Hytale server row, its data dir and its first settings
func (s *ServerService) createHytaleServer(ctx context.Context, msg *v1.CreateServerRequest) (*connect.Response[v1.CreateServerResponse], error) {
	name := strings.TrimSpace(msg.Name)
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	if len(msg.ProxyHostnames) > 0 || msg.ProxyListenerId != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the hostname proxy is Minecraft only, Hytale players join on ip:port"))
	}
	if msg.ModpackId != "" || msg.WorldUploadSessionId != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("modpacks and world imports are Minecraft only"))
	}
	patchline, err := hytale.NormalizePatchline(msg.GetHytale().GetPatchline())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	viewRadius := int(msg.GetHytale().GetMaxViewRadius())
	maxPlayers := int(msg.MaxPlayers)
	if maxPlayers == 0 {
		maxPlayers = 100
	}
	check := hytale.ServerConfig{ServerName: name, MaxPlayers: maxPlayers, MaxViewRadius: max(viewRadius, 1)}
	if err := hytale.ValidateConfig(check); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	port := int(msg.Port)
	if port == 0 {
		port = hytale.DefaultPort
	}
	if port < 1 || port > 65535 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid port %d", port))
	}
	memory := msg.Memory
	if memory == 0 {
		memory = hytale.MinMemoryMB
	}
	if memory < hytale.MinMemoryMB {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Hytale needs at least %d MB of memory", hytale.MinMemoryMB))
	}
	image := msg.DockerImage
	if !validHytaleImage(image) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Hytale needs a Java %d runtime image", hytale.JavaMajor))
	}
	if image == "" {
		image = javaversions.Tag(hytale.JavaMajor)
	}

	if err := s.allocateAdditionalPorts(ctx, msg.AdditionalPorts); err != nil {
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	}
	additionalPorts, err := normalizeAdditionalPorts(msg.AdditionalPorts, nil, s.proxy.Enabled())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateHytaleExtraPorts(additionalPorts); err != nil {
		return nil, err
	}
	if err := validateDockerOverrides(ctx, s.authManager, s.config.Storage.DataDir, msg.DockerOverrides, nil); err != nil {
		return nil, err
	}

	serverUUID := uuid.New().String()
	owner := proxy.NetOwner{Kind: proxy.OwnerServer, ID: serverUUID}
	netClaim, err := checkoutNetwork(ctx, s.proxy, s.log, owner, hytalePortRequests(port, additionalPorts, s.proxy.Enabled()))
	if err != nil {
		return nil, err
	}
	defer netClaim.Release()

	serverDataDir := fmt.Sprintf("%s_%s", files.SanitizePathName(name), serverUUID)
	server := &v1.Server{
		Id:              serverUUID,
		Name:            name,
		Description:     msg.Description,
		ModLoader:       v1.ModLoader_MOD_LOADER_HYTALE,
		Status:          v1.ServerStatus_SERVER_STATUS_CREATING,
		Port:            int32(port),
		MaxPlayers:      int32(maxPlayers),
		Memory:          memory,
		MemoryMin:       msg.MemoryMin,
		MemoryMax:       msg.MemoryMax,
		DataPath:        filepath.Join(s.config.Storage.DataDir, "servers", serverDataDir),
		JavaVersion:     hytale.JavaMajor,
		DockerImage:     image,
		AutoStart:       msg.AutoStart,
		Detached:        msg.Detached,
		AdditionalPorts: additionalPorts,
		DockerOverrides: msg.DockerOverrides,
	}
	if err := runtimespec.NormalizeServerMemory(server); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if err := os.MkdirAll(server.DataPath, 0755); err != nil {
		s.log.Error("Failed to create data directory: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create server directory"))
	}
	// Settings land in config.json once the server wrote its own copy
	pending := hytale.NewObject()
	_ = pending.Set(hytale.KeyServerName, name)
	_ = pending.Set(hytale.KeyMaxPlayers, maxPlayers)
	if viewRadius > 0 {
		_ = pending.Set(hytale.KeyMaxViewRadius, viewRadius)
	}
	privateDir, err := hytale.PrivateDir(s.config.Storage.DataDir, server.Id)
	if err == nil {
		err = hytale.WriteSettings(privateDir, &v1.HytaleSettings{Patchline: patchline})
	}
	if err == nil {
		err = hytale.SavePending(privateDir, pending)
	}
	if err != nil {
		os.RemoveAll(server.DataPath)
		if privateDir != "" {
			os.RemoveAll(privateDir)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to write Hytale settings: %w", err))
	}

	if err := s.store.CreateServer(ctx, server); err != nil {
		s.log.Error("Failed to create server: %v", err)
		os.RemoveAll(server.DataPath)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create server"))
	}
	netClaim.Confirm()
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_CREATE, metrics.Attrs{"game": "hytale"}, "created the Hytale server")
	syncRoutes(ctx, s.proxy, s.log, "after server create")
	if s.hytale != nil {
		s.hytale.Track(server.Id, server.DataPath)
	}

	// Hytale has no RCON, console commands go through the runtime agent.
	// The crash doctor repairs Minecraft mod sets and would misread plugins.
	serverConfig := s.serverPropertiesOrDefault(ctx, server.Id)
	rconOff, doctorOff := false, false
	serverConfig.EnableRcon = &rconOff
	serverConfig.DoctorEnabled = &doctorOff
	runtimespec.SyncPropertiesMemory(serverConfig, server)
	if err := s.store.UpdateServerProperties(ctx, serverConfig); err != nil {
		s.log.Error("Failed to save Hytale server properties: %v", err)
	}

	if msg.StartImmediately {
		server.Status = v1.ServerStatus_SERVER_STATUS_PROVISIONING
		if err := s.store.UpdateServer(ctx, server); err != nil {
			s.log.Error("Failed to update server status: %v", err)
		}
		if err := s.runLifecycleAsync(ctx, server, 2*time.Hour, "start"); err != nil {
			s.log.Error("Failed to start newly created server %s: %v", server.Name, err)
		}
	} else {
		server.Status = v1.ServerStatus_SERVER_STATUS_STOPPED
		if err := s.store.UpdateServer(ctx, server); err != nil {
			s.log.Error("Failed to update server status: %v", err)
		}
	}
	return connect.NewResponse(&v1.CreateServerResponse{Server: server.Redact()}), nil
}

// Rejects edits a Hytale server cannot take, and Minecraft to Hytale switches
func validateHytaleUpdate(server *v1.Server, msg *v1.UpdateServerRequest) error {
	wasHytale := minecraft.IsHytale(server.ModLoader)
	if msg.ModLoader != v1.ModLoader_MOD_LOADER_UNSPECIFIED && minecraft.IsHytale(msg.ModLoader) != wasHytale {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("a server cannot switch between Minecraft and Hytale, create a new server instead"))
	}
	if !wasHytale {
		return nil
	}
	if msg.McVersion != "" && msg.McVersion != server.McVersion {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the Hytale version follows the release channel, change it under Updates"))
	}
	if msg.ModpackId != "" {
		return errHytaleUnsupported("modpacks")
	}
	if !validHytaleImage(msg.DockerImage) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Hytale needs a Java %d runtime image", hytale.JavaMajor))
	}
	if msg.Memory > 0 && msg.Memory < hytale.MinMemoryMB {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Hytale needs at least %d MB of memory", hytale.MinMemoryMB))
	}
	return validateHytaleExtraPorts(msg.AdditionalPorts)
}

// Hytale facts for the server page, cheap file reads plus tracker state
func (s *ServerService) applyHytale(ctx context.Context, server *v1.Server) {
	if !minecraft.IsHytale(server.ModLoader) {
		return
	}
	state := &v1.HytaleState{InstalledVersion: server.McVersion}
	if privateDir, err := hytale.PrivateDir(s.config.Storage.DataDir, server.Id); err == nil {
		state.DownloaderSignedIn = hytale.DownloaderSignedIn(privateDir)
		if settings, err := hytale.ReadSettings(privateDir); err == nil {
			state.Patchline = settings.Patchline
		}
	}
	if obj, err := hytale.ReadObjectFile(filepath.Join(server.DataPath, hytale.ConfigFile)); err == nil && obj != nil {
		state.MaxViewRadius = int32(obj.Int(hytale.KeyMaxViewRadius))
	}
	state.PersistenceEncrypted = hytale.PersistenceEncrypted(server.DataPath)
	if s.hytale != nil {
		s.hytale.Track(server.Id, server.DataPath)
		st := s.hytale.Status(server.Id)
		state.Auth = st.Auth
		state.SignIn = st.SignIn
		state.PlayersKnown = st.PlayersKnown
		state.PersistenceEncrypted = state.PersistenceEncrypted || st.PersistenceSeen
		if st.PlayersKnown {
			server.PlayersOnline = int32(len(st.Players))
			server.PlayerSample = st.Players
		}
	}
	// A live device code signs the server into the operator's account,
	// only callers who could start the sign-in themselves see it
	if state.SignIn != nil && !s.canRunCommands(ctx, server.Id) {
		state.SignIn.Url, state.SignIn.Code, state.SignIn.Withheld = "", "", true
	}
	server.Hytale = state
}

// Reports whether the caller may run console commands on a server
func (s *ServerService) canRunCommands(ctx context.Context, serverID string) bool {
	user := auth.GetUserFromContext(ctx)
	if user == nil || s.enforcer == nil {
		return false
	}
	ok, err := s.enforcer.Enforce(user.Roles, optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, optionsv1.ActionType_ACTION_TYPE_COMMAND, serverID)
	return err == nil && ok
}
