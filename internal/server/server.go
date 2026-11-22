package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"enhancedkimi/internal/bridge"
	"enhancedkimi/internal/chat"
	"enhancedkimi/internal/config"
	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/future"
	"enhancedkimi/internal/gateway"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/kctmigration"
	"enhancedkimi/internal/monitormigration"
	"enhancedkimi/internal/plugins"
	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/registry"
	"enhancedkimi/internal/scheduler"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/updater"
	"enhancedkimi/internal/usage"
	"enhancedkimi/internal/webassets"
	bundledPlugins "enhancedkimi/plugins"
)

var Version = "0.2.3"

type Module struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}
type NativeOptions struct {
	Home                  string
	CWD                   string
	DataDir               string
	Executable            string
	Profile               string
	CoreURL               string
	PairingToken          string
	RestartState          func() updater.RestartState
	RequestRestart        func()
	UpdateAPIBaseURL      string
	UpdateHTTPClient      *http.Client
	VaultKeyStore         credentials.OSKeyStore
	EnableOSKeyStore      bool
	EnableAutoUpdateCheck bool
}

type App struct {
	Handler          http.Handler
	Settings         *config.Store
	modules          []Module
	pluginClose      func() error
	pluginSafe       func()
	pluginManager    *plugins.Manager
	adapter          *desktop.Mock
	native           *desktop.NativeService
	updater          *updater.Manager
	started          time.Time
	gateway          *gateway.Gateway
	planner          *chat.PlannerService
	scheduler        *scheduler.Engine
	kctMigration     *kctmigration.Service
	monitorMigration *monitormigration.Service
	winBridge        *windowsBridge
	gatewayErr       string
	gatewayBase      string
}

// New builds the core without the local gateway. A build that wants /v1 uses
// NewWithGateway; the split keeps existing callers unchanged and makes serving
// /v1 an explicit decision.
func New(db *sql.DB, guard *security.Guard, forceSafe bool) (*App, error) {
	return NewWithGateway(db, guard, forceSafe, "")
}

// NewWithGateway builds the core and, when gatewayToken is non-empty, mounts
// the local OpenAI-compatible gateway. An empty token leaves /v1 unmounted
// rather than serving it open.
func NewWithGateway(db *sql.DB, guard *security.Guard, forceSafe bool, gatewayToken string) (*App, error) {
	return NewWithNative(db, guard, forceSafe, gatewayToken, NativeOptions{})
}

func NewWithNative(db *sql.DB, guard *security.Guard, forceSafe bool, gatewayToken string, native NativeOptions) (*App, error) {
	cfg, err := config.New(db)
	if err != nil {
		return nil, err
	}
	if forceSafe && !cfg.Safe() {
		v, e := cfg.Get()
		if e != nil {
			return nil, e
		}
		v.SafeMode = true
		if e = cfg.Put(v); e != nil {
			return nil, e
		}
	}
	settings, err := cfg.Get()
	if err != nil {
		return nil, err
	}
	app := &App{Settings: cfg, started: time.Now(), adapter: desktop.NewMock()}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /enhance/v1/pair", guard.Pair)
	app.adapter.Register(mux)
	var nativeRef atomic.Pointer[desktop.NativeService]
	onUpdateStatus := func(status updater.Status) {
		if ns := nativeRef.Load(); ns != nil {
			ns.Events().Publish("update", status)
		}
	}
	// The health list is read at update time, after every module has been
	// registered, so the updater compares the candidate against the modules this
	// core actually reports as READY.
	app.updater = newUpdater(native, cfg, app.healthyModules, onUpdateStatus)
	registerUpdateRoutes(mux, app.updater)
	add := func(id string, init func() error) {
		if !settings.Modules[id] {
			app.modules = append(app.modules, Module{id, "DISABLED", "重启后应用模块设置"})
			return
		}
		if err := init(); err != nil {
			app.modules = append(app.modules, Module{id, "FAULTED", "初始化失败，其余模块仍可使用"})
			log.Printf("module %s initialization failed", id)
		} else {
			app.modules = append(app.modules, Module{id, "READY", ""})
		}
	}
	// Only the table migration is left. provider, gateway and the planner read
	// registry_providers/registry_models directly, while the HTTP surface that
	// served the separate model page is gone: models are configured on the
	// official provider form.
	add("registry", func() error {
		s, e := registry.New(db)
		if e == nil {
			s.RegisterGateways(mux)
		}
		return e
	})
	var ledger *usage.Service
	add("usage", func() error {
		s, e := usage.New(db)
		if e == nil {
			s.Register(mux)
			ledger = s
		}
		return e
	})
	add("bridge", func() error {
		s, e := bridge.New(db, app.adapter, cfg.Safe)
		if e == nil {
			s.UseDispatchGate(func(fn func() error) error {
				err := cfg.Dispatch(fn)
				if errors.Is(err, config.ErrSafeMode) {
					return desktop.ErrSafeMode
				}
				return err
			})
			s.Register(mux)
		}
		return e
	})
	add("plugins", func() error {
		// The data directory is known before construction; the vault and the native
		// service are not, because they are built further down. They are attached by
		// instance method on this same manager, so an App never reaches a
		// package-level vault or core.
		//
		// The host version is deliberately not passed here: hostVersions refers to
		// the Kimi Code client version (1.3.x), which is discovered when the
		// enhancer attaches, not to this core's own 0.2.0. An unknown host version
		// leaves the version gate open rather than inventing a compatibility claim.
		s, e := plugins.New(db, cfg.Safe, plugins.WithDataDir(native.DataDir), plugins.WithBundled(bundledPlugins.BundledPlugins), plugins.WithOfficial(bundledPlugins.OfficialDigests))
		if e == nil {
			s.Register(mux)
			app.pluginManager = s
			app.pluginClose = s.Close
			app.pluginSafe = s.EnforceSafeMode
			// The handover from the standalone KCT extension gates the adapter's
			// enable, so the gate is attached to the same instance the plugin page
			// talks to. It is a core route because the adapter is disabled at the
			// moment the page needs it.
			if native.DataDir != "" {
				migration := kctmigration.New(native.DataDir, s, kctmigration.System{})
				migration.Register(mux)
				s.UseEnableGate(migration)
				app.kctMigration = migration

				monitorMig := monitormigration.New(native.DataDir, monitormigration.System{})
				monitorMig.Register(mux)
				s.UseEnableGate(monitorMig)
				app.monitorMigration = monitorMig
			}
			if bundled, bundleErr := bundledPlugins.BundledPlugins(); bundleErr == nil {
				if releaseErr := s.ReleaseBundled(context.Background(), bundled); releaseErr != nil {
					log.Printf("bundled plugin release incomplete: %v", releaseErr)
				}
			}
		}
		return e
	})
	var providerClient *provider.Client
	credentialStatus := "UNAVAILABLE"
	vault, vaultErr := credentials.NewVault(db)
	if vaultErr == nil {
		keyStore := native.VaultKeyStore
		if keyStore == nil && native.EnableOSKeyStore && native.DataDir != "" {
			keyStore = credentials.NewOSKeyStore(native.DataDir)
		}
		if keyStore != nil {
			vault.UseKeyStore(keyStore)
			if autoErr := vault.AutoUnlock(context.Background()); autoErr != nil {
				log.Printf("credential vault auto-unlock failed: %v", autoErr)
			}
		}
		vault.Register(mux)
		providerClient = provider.New(db, vault)
		providerClient.Register(mux)
		if vault.Status(context.Background()).OSKeychain {
			credentialStatus = "ENCRYPTED_VAULT_OS_KEYCHAIN"
		} else {
			credentialStatus = "ENCRYPTED_VAULT_MANUAL_UNLOCK"
		}
		if app.pluginManager != nil {
			// The plugin host gets this App's vault, not a second locked one.
			app.pluginManager.UseVault(vault)
		}
		closePlugins := app.pluginClose
		app.pluginClose = func() error {
			vault.Lock()
			if closePlugins != nil {
				return closePlugins()
			}
			return nil
		}
		browser, e := future.NewBrowserStore(db, vault.BrowserStore(), cfg.Dispatch)
		if e == nil {
			browser.Register(mux)
			app.modules = append(app.modules, Module{"browser-store", "READY", "隔离加密会话库，原生浏览器未连接"})
		}
	}
	workspace, workspaceErr := future.NewWorkspace(db, cfg.Safe, cfg.Dispatch)
	if workspaceErr == nil {
		workspace.Register(mux)
		app.modules = append(app.modules, Module{"workspace", "READY", "本地会话引用与策略，原生执行未连接"})
	}
	if providerClient != nil && gatewayToken != "" {
		gw := gateway.New(providerClient, ledger, cfg.Dispatch, gatewayToken)
		gw.Register(mux)
		mux.HandleFunc("GET /enhance/v1/gateway", func(w http.ResponseWriter, r *http.Request) {
			httpx.JSON(w, 200, map[string]any{"enabled": true, "baseUrl": app.gatewayBase, "tokenMasked": gateway.MaskToken(gatewayToken), "tokenAvailable": true, "streaming": true, "reasoningContent": true, "toolCalls": true, "multiAccount": false, "writesUserConfig": false})
		})
		mux.HandleFunc("GET /enhance/v1/gateway/preview", func(w http.ResponseWriter, r *http.Request) {
			httpx.JSON(w, 200, gw.PreviewConfig(app.gatewayBase))
		})
		app.gateway = gw
		app.modules = append(app.modules, Module{"gateway", "READY", "本机 OpenAI 兼容网关，单账号穿透"})
	} else {
		// No token means /v1 is not mounted at all. It is not a faulted module,
		// so it must not appear as a degraded core capability.
		app.gatewayErr = "未提供本地网关令牌"
	}
	chats, err := chat.New(db)
	chatReady := err == nil
	if chatReady {
		if providerClient != nil {
			chats.UseProvider(providerClient, ledger, cfg.Dispatch)
		}
		chats.Register(mux)
		app.modules = append(app.modules, Module{"chat", "READY", ""})
	} else {
		app.modules = append(app.modules, Module{"chat", "FAULTED", "聊天初始化失败，其余模块仍可使用"})
	}
	if native.Home != "" {
		nativeService, nativeErr := newNativeIntegration(db, native, cfg.Safe)
		if nativeErr != nil {
			app.modules = append(app.modules, Module{"native-host", "FAULTED", "原生服务初始化失败，其余模块仍可使用"})
		} else {
			if vault != nil {
				nativeService.UseVault(vault)
			}
			nativeService.UseUpdater(app.updater)
			nativeService.Register(mux)
			app.native = nativeService
			nativeRef.Store(nativeService)
			if app.pluginManager != nil {
				app.pluginManager.UseCore(pluginCore{service: nativeService})
				app.pluginManager.UseOnChange(func() {
					nativeService.Events().Publish("plugins", map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano)})
				})
			}
			if chatReady {
				if generator := newPlannerGenerator(app.gateway, nativeService, cfg.Dispatch); generator != nil {
					planner, plannerErr := chat.NewPlanner(db, generator, native.CWD, nativeService.Events())
					if plannerErr != nil {
						app.modules = append(app.modules, Module{"planner", "FAULTED", "规划持久化初始化失败，其余模块仍可使用"})
					} else {
						nativeService.UsePlanner(planner)
						app.planner = planner
						app.modules = append(app.modules, Module{"planner", "READY", "持久化流式规划、工作区工具与逐次审批"})
					}
				}
			}
			schedulerStore, schedStoreErr := scheduler.NewStore(db)
			if schedStoreErr != nil {
				app.modules = append(app.modules, Module{"scheduler", "FAULTED", "定时任务存储初始化失败，其余模块仍可使用"})
			} else {
				recordActivity := func(ctx context.Context, pluginID, detail string) {
					if app.pluginManager != nil {
						app.pluginManager.RecordActivity(ctx, pluginID, plugins.ActivitySession, detail)
					}
				}
				schedEngine, schedErr := scheduler.NewEngine(
					schedulerStore,
					nativeService.NewSchedulerExecutor(recordActivity),
					scheduler.WithDefaultCWD(native.CWD),
					scheduler.WithPluginEnabled(func(pluginID string) bool {
						if cfg.Safe() || app.pluginManager == nil {
							return false
						}
						p, ok := app.pluginManager.Get(pluginID)
						return ok && p.Enabled
					}),
				)
				if schedErr != nil {
					app.modules = append(app.modules, Module{"scheduler", "FAULTED", "定时任务调度初始化失败，其余模块仍可使用"})
				} else {
					nativeService.UseScheduler(schedEngine)
					app.scheduler = schedEngine
					schedEngine.Start()
					app.modules = append(app.modules, Module{"scheduler", "READY", "本地持久化定时任务与无人值守调度引擎"})
				}
			}
			if app.pluginManager != nil {
				app.pluginManager.UseStopPluginWork(func(ctx context.Context, id string) error {
					var errs []error
					if app.planner != nil {
						if _, stopErr := app.planner.StopPluginThreads(ctx, id); stopErr != nil {
							errs = append(errs, stopErr)
						}
					}
					if app.scheduler != nil {
						if stopErr := app.scheduler.StopPlugin(ctx, id); stopErr != nil {
							errs = append(errs, stopErr)
						}
					}
					return errors.Join(errs...)
				})
				app.pluginManager.UseOnUninstall(func(ctx context.Context, id string, deleteData bool) error {
					if app.scheduler == nil {
						return nil
					}
					if deleteData {
						return app.scheduler.DeletePluginData(ctx, id)
					}
					return app.scheduler.StopPlugin(ctx, id)
				})
			}
			app.modules = append(app.modules, Module{"native-host", "READY", "官方会话适配与版本化 RPC 动作层"})
		}
	}
	compactionStatus := "POLICY_ONLY_NATIVE_UNCONNECTED"
	if app.native != nil {
		compactionStatus = "NATIVE_SCHEDULER_WITH_RECEIPTS"
	}
	mux.HandleFunc("GET /enhance/v1/health", func(w http.ResponseWriter, r *http.Request) {
		currentCredentials := credentialStatus
		if vaultErr == nil && vault != nil {
			if vault.Status(r.Context()).OSKeychain {
				currentCredentials = "ENCRYPTED_VAULT_OS_KEYCHAIN"
			} else {
				currentCredentials = "ENCRYPTED_VAULT_MANUAL_UNLOCK"
			}
		}
		httpx.JSON(w, 200, map[string]any{"status": "OK", "version": Version, "safeMode": cfg.Safe(), "modules": app.modules, "uptimeMs": time.Since(app.started).Milliseconds(), "adapter": app.adapter.Status(r.Context()), "compaction": compactionStatus, "credentials": currentCredentials})
	})
	if native.EnableAutoUpdateCheck && app.updater != nil {
		app.updater.StartAutoCheck()
	}
	mux.HandleFunc("GET /enhance/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		v, e := cfg.Get()
		if e != nil {
			httpx.Fail(w, 500, "STORAGE_ERROR", "无法读取设置")
			return
		}
		httpx.JSON(w, 200, v)
	})
	mux.HandleFunc("PUT /enhance/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		var update config.Update
		if !httpx.Decode(w, r, &update) {
			return
		}
		v, inputErr := update.Value()
		if inputErr != nil {
			httpx.Fail(w, 422, "INVALID_SETTINGS", "所有设置必须明确填写，未知值不能替代开关或版本号")
			return
		}
		if chatReady {
			var n int
			if e := db.QueryRow(`SELECT count(*) FROM chat_sources WHERE id=?`, v.DefaultChatSource).Scan(&n); e != nil || n == 0 {
				httpx.Fail(w, 422, "INVALID_SOURCE", "默认聊天来源不存在")
				return
			}
		} else {
			current, e := cfg.Get()
			if e != nil || current.DefaultChatSource != v.DefaultChatSource {
				httpx.Fail(w, 409, "SOURCE_UNAVAILABLE", "聊天模块不可用，恢复后再更换默认来源")
				return
			}
		}
		if e := cfg.Put(v); e != nil {
			if errors.Is(e, config.ErrConflict) {
				httpx.Fail(w, 409, "CONFIG_CONFLICT", "设置已变化，请刷新后再保存")
			} else {
				httpx.Fail(w, 422, "INVALID_SETTINGS", "设置无效，请检查模块和空闲分钟数")
			}
			return
		}
		if cfg.Safe() && app.pluginSafe != nil {
			app.pluginSafe()
		}
		v, e := cfg.Get()
		if e != nil {
			httpx.Fail(w, 500, "STORAGE_ERROR", "设置已保存，但暂时无法读回")
			return
		}
		httpx.JSON(w, 200, v)
	})
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		mux.HandleFunc(method+" /enhance/", func(w http.ResponseWriter, r *http.Request) {
			httpx.Fail(w, 404, "NOT_FOUND", "接口不存在或模块未启用")
		})
	}
	assets := webassets.FS()
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "." || p == "" {
			p = "index.html"
		}
		if _, e := fs.Stat(assets, p); e != nil {
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			files.ServeHTTP(w, r2)
			return
		}
		files.ServeHTTP(w, r)
	})
	app.registerWindowsBridgeRoutes(mux, guard)
	app.Handler = recoverer(app.wrapWindowsBridge(guard, guard.Wrap(mux)))
	return app, nil
}
func (a *App) Close() error {
	if a.updater != nil {
		a.updater.Close()
	}
	if a.scheduler != nil {
		a.scheduler.Close()
	}
	if a.planner != nil {
		a.planner.Close()
	}
	if a.native != nil {
		a.native.Close()
	}
	if a.pluginClose != nil {
		return a.pluginClose()
	}
	return nil
}

// healthyModules reports the modules this core has running right now.
func (a *App) healthyModules() []string {
	ids := make([]string, 0, len(a.modules))
	for _, module := range a.modules {
		if module.State == "READY" {
			ids = append(ids, module.ID)
		}
	}
	return ids
}

func newUpdater(native NativeOptions, cfg *config.Store, healthyModules func() []string, onStatusChange func(updater.Status)) *updater.Manager {
	options := updater.Options{
		CurrentVersion: Version, DataDir: native.DataDir, Safe: cfg.Safe,
		Executable: native.Executable, RestartState: native.RestartState, RequestRestart: native.RequestRestart,
		TargetExecutable: func() string { return desktop.ManagedUpdateExecutable(native.DataDir) },
		HealthyModules:   healthyModules,
		OnStatusChange:   onStatusChange,
		Launchd: updater.LaunchdOptions{
			Label:   "com.user.enhanced-kimi.core-managed",
			Enabled: func(target string) bool { return target == desktop.ManagedUpdateExecutable(native.DataDir) },
			// The login item, not the supervisor, owns the resident process.
			Managed: func() bool { return desktop.LaunchdManagedCore(native.DataDir) },
		},
		APIBaseURL: native.UpdateAPIBaseURL, HTTPClient: native.UpdateHTTPClient,
	}
	options.Dispatch = func(fn func() error) error {
		err := cfg.Dispatch(fn)
		if errors.Is(err, config.ErrSafeMode) {
			return updater.ErrSafeMode
		}
		return err
	}
	return updater.New(options)
}

func registerUpdateRoutes(mux *http.ServeMux, manager *updater.Manager) {
	mux.HandleFunc("GET /enhance/v1/update/status", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, manager.Status())
	})
	mux.HandleFunc("POST /enhance/v1/update/check", func(w http.ResponseWriter, r *http.Request) {
		status, err := manager.Check(r.Context())
		if err == nil {
			httpx.JSON(w, http.StatusOK, status)
		} else if errors.Is(err, updater.ErrBusy) {
			httpx.Fail(w, http.StatusConflict, "UPDATE_BUSY", err.Error())
		} else {
			httpx.Fail(w, http.StatusBadGateway, "UPDATE_CHECK_FAILED", err.Error())
		}
	})
	mux.HandleFunc("POST /enhance/v1/update/apply", func(w http.ResponseWriter, r *http.Request) {
		status, err := manager.StartApply(r.Context())
		if err != nil {
			httpx.Fail(w, http.StatusConflict, "UPDATE_REJECTED", err.Error())
			return
		}
		httpx.JSON(w, http.StatusAccepted, status)
	})
	mux.HandleFunc("POST /enhance/v1/update/auto-check", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if !httpx.Decode(w, r, &body) || body.Enabled == nil {
			httpx.Fail(w, http.StatusBadRequest, "INVALID_INPUT", "缺少 enabled 开关参数")
			return
		}
		status, err := manager.SetAutoCheck(*body.Enabled)
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "UPDATE_PREFS_FAILED", err.Error())
			return
		}
		httpx.JSON(w, http.StatusOK, status)
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				log.Printf("request isolated id=%s", security.Random()[:12])
				httpx.Fail(w, 500, "INTERNAL_ERROR", "操作失败，核心服务仍在运行")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func (a *App) String() string { return fmt.Sprintf("Enhanced Kimi Desktop %s", Version) }
