package desktop

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pageRead is one page a probe moved the view to and what the two reading
// actions answered there.
type pageRead struct {
	PageURL string `json:"pageURL"`
	Extract struct {
		OK      bool   `json:"ok"`
		Value   string `json:"value"`
		Refused string `json:"refused"`
		Message string `json:"message"`
	} `json:"extract"`
	Adapter struct {
		OK    bool `json:"ok"`
		Value struct {
			PageURL string `json:"pageUrl"`
		} `json:"value"`
		Refused string `json:"refused"`
		Message string `json:"message"`
	} `json:"adapter"`
}

// probeRun is the whole answer the probe prints, in the shape it prints it.
type probeRun struct {
	Installed struct {
		Transport string `json:"transport"`
	} `json:"installed"`
	RecordedURL string         `json:"recordedURL"`
	Opened      map[string]any `json:"opened"`
	Redirected  pageRead       `json:"redirected"`
	PushState   pageRead       `json:"pushState"`
	FileScheme  pageRead       `json:"fileScheme"`
	DataScheme  pageRead       `json:"dataScheme"`
	Fatal       string         `json:"fatal"`
}

// Reading a plugin's page must follow the page the view is actually on. A
// sign-in redirect and an in-page route change both move a plugin's own view
// away from the address core recorded when it opened, and the plugin owns that
// navigation: browser:view already lets it reach any http(s) page. Requiring the
// two strings to match refused both, which broke every real page that redirects
// or pushes a route.
//
// The probe runs the real bootstrap.js against a fake Electron and a fake core,
// then drives browser.view.extract and browser.view.adapter over the bridge the
// plugin runtime uses, so this is the behaviour of the shipped file rather than
// a restatement of its source.
func TestPluginPageReadsFollowTheAPageThatMoved(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the executable bootstrap probe needs it")
	}
	probe, err := filepath.Abs("testdata/bootstrap_url_probe.cjs")
	if err != nil {
		t.Fatal(err)
	}
	source, err := filepath.Abs("assets/bootstrap.js")
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, probe, source).Output()
	if err != nil {
		t.Fatalf("the bootstrap probe failed: %v", err)
	}
	var run probeRun
	if err := json.Unmarshal(output, &run); err != nil {
		t.Fatalf("the bootstrap probe answered unreadable JSON: %v\n%s", err, output)
	}
	if run.Fatal != "" {
		t.Fatalf("the probe could not install the host: %s", run.Fatal)
	}
	if run.Installed.Transport != "main-loopback-rpc" {
		t.Fatalf("the probe did not install the real host: %#v", run.Installed)
	}
	if run.Opened["ok"] != true {
		t.Fatalf("the probe could not open a view: %#v", run.Opened)
	}

	allowed := map[string]pageRead{"重定向后": run.Redirected, "改路由后": run.PushState}
	for label, read := range allowed {
		if read.PageURL == run.RecordedURL {
			t.Fatalf("%s the probe left the page on the recorded address, so it proves nothing", label)
		}
		if !read.Extract.OK {
			t.Fatalf("%s提取失败：%s", label, read.Extract.Message)
		}
		if !strings.Contains(read.Extract.Value, read.PageURL) {
			t.Fatalf("%s提取的不是当前页面：%q", label, read.Extract.Value)
		}
		if !read.Adapter.OK {
			t.Fatalf("%s适配器失败：%s", label, read.Adapter.Message)
		}
		if read.Adapter.Value.PageURL != read.PageURL {
			t.Fatalf("%s适配器跑在了别的页面：%#v", label, read.Adapter.Value)
		}
	}

	for label, read := range map[string]pageRead{"file 页面": run.FileScheme, "data 页面": run.DataScheme} {
		if read.Extract.OK || read.Adapter.OK {
			t.Fatalf("%s被读取了：%#v", label, read)
		}
		if !strings.Contains(read.Extract.Message, "http") || !strings.Contains(read.Adapter.Message, "http") {
			t.Fatalf("%s的拒绝没有说明原因：%#v", label, read)
		}
	}
}
