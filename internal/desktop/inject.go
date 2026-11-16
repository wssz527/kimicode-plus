package desktop

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"enhancedkimi/internal/webassets"
)

//go:embed assets/bootstrap.js
var bootstrapSource string

//go:embed assets/renderer.js
var rendererSource string

type AttachOptions struct {
	Profile      string
	CoreURL      string
	PairingToken string
}

type AttachResult struct {
	Status      string `json:"status"`
	Transport   string `json:"transport"`
	Protocol    int    `json:"protocol"`
	AppVersion  string `json:"appVersion"`
	Electron    string `json:"electron"`
	HostPID     int    `json:"hostPid"`
	DOMRevision int    `json:"domRevision"`
}

func missingNativeCapability(capabilities map[string]bool) string {
	for _, capability := range []string{"ptShell", "ptBody", "sideColumn", "sideFooter"} {
		if !capabilities[capability] {
			return capability
		}
	}
	return ""
}

func AttachNative(ctx context.Context, options AttachOptions) (*AttachResult, error) {
	if options.CoreURL == "" || len(options.PairingToken) < 32 {
		return nil, errors.New("缺少增强核心地址或配对凭据")
	}
	pid, err := FindMainPID(ctx, options.Profile)
	if err != nil {
		return nil, err
	}
	attachCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	cdp, err := Attach(attachCtx, pid)
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if !succeeded {
			_, _ = cdp.Evaluate(closeCtx, `(async()=>{if(globalThis.__ekdNative?.cleanup){await globalThis.__ekdNative.cleanup();return true;}return false;})()`)
		}
		_ = cdp.Close(closeCtx)
	}()
	raw, err := cdp.Evaluate(attachCtx, `(()=>{const req=typeof require==='function'?require:process.mainModule.require.bind(process.mainModule);return{pid:process.pid,type:process.type,appVersion:req('electron').app.getVersion(),electron:process.versions.electron}})()`)
	if err != nil {
		return nil, err
	}
	var identity HostIdentity
	if err = json.Unmarshal(raw, &identity); err != nil {
		return nil, err
	}
	if identity.PID != pid {
		return nil, errors.New("宿主进程身份在附着时发生变化")
	}
	contract, err := identity.Contract()
	if err != nil {
		return nil, err
	}
	var capabilities map[string]bool
	domDeadline := time.Now().Add(15 * time.Second)
	for {
		domRaw, domErr := cdp.Evaluate(attachCtx, `(async()=>{const req=typeof require==='function'?require:process.mainModule.require.bind(process.mainModule);const{webContents}=req('electron');const wc=webContents.getAllWebContents().find(item=>item.getURL().startsWith('app://renderer/')&&!/screenshot|browser-overlay/.test(item.getURL()));if(!wc)throw new Error('隔离主窗口未找到');return wc.executeJavaScript("(()=>({ptShell:!!document.querySelector('.pt-shell'),ptBody:!!document.querySelector('.pt-body'),sideColumn:!!document.querySelector('.side .col'),sideFooter:!!document.querySelector('.side-footer'),chat:!!document.querySelector('.chat'),composer:!!document.querySelector('.composer'),sideChat:[...document.querySelectorAll('button.sit-link')].some(button=>button.textContent.trim()==='侧边聊天')}))()")})()`)
		missing := ""
		if domErr == nil {
			if json.Unmarshal(domRaw, &capabilities) == nil {
				missing = missingNativeCapability(capabilities)
				if missing == "" {
					break
				}
			}
		}
		if time.Now().After(domDeadline) {
			if domErr != nil {
				return nil, domErr
			}
			return nil, fmt.Errorf("宿主 DOM 契约缺少 %s，增强保持停用", missing)
		}
		select {
		case <-attachCtx.Done():
			return nil, attachCtx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	assets := webassets.FS()
	moduleSource, err := fs.ReadFile(assets, "host/enhancement.js")
	if err != nil {
		return nil, err
	}
	cssSource, err := fs.ReadFile(assets, "host/enhancement.css")
	if err != nil {
		return nil, err
	}
	config := map[string]any{
		"coreURL": options.CoreURL, "pairingToken": options.PairingToken,
		"expectedApp": contract.AppVersion, "expectedElectron": contract.Electron,
		"moduleSource": string(moduleSource), "cssSource": string(cssSource), "rendererSource": rendererSource,
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	raw, err = cdp.Evaluate(attachCtx, "("+bootstrapSource+")("+string(encoded)+")")
	if err != nil {
		return nil, err
	}
	var result AttachResult
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.Status != "installed" || result.Transport != "main-loopback-rpc" || result.Protocol != 1 {
		return nil, errors.New("宿主没有确认正式 RPC 已挂载")
	}
	result.HostPID = pid
	result.DOMRevision = contract.DOMRevision
	succeeded = true
	return &result, nil
}
