package desktop

import (
	"os"
	"strings"
	"testing"
)

// The host runs a site adapter as an expression, in the plugin's own page, and
// returns JSON. These are the properties that make that safe: the body is
// evaluated as one expression (so a non-function throws), the arguments come
// from the authorized answer, and the result is serialized rather than handed
// back as a live object.
func TestSiteAdapterRunsAsOneBoundedExpression(t *testing.T) {
	source, err := os.ReadFile("assets/bootstrap.js")
	if err != nil {
		t.Fatal(err)
	}
	bootstrapSource := strings.ReplaceAll(string(source), "\r\n", "\n")
	start := strings.Index(bootstrapSource, "async function runSiteAdapter(contents, adapter) {")
	if start < 0 {
		t.Fatal("bootstrap.js has no runSiteAdapter")
	}
	end := strings.Index(bootstrapSource[start:], "\n  }\n")
	if end < 0 {
		t.Fatal("runSiteAdapter is not terminated")
	}
	block := bootstrapSource[start : start+end]
	for _, wanted := range []string{
		// The body is wrapped in a call expression: a statement would run on load
		// and answer nothing, so a broken adapter must fail instead.
		"'const run = (' + source + ');'",
		"if (typeof run !== \"function\")",
		"run(' + args + ')",
		"Promise.resolve(",
		"JSON.stringify(",
		"JSON.parse(text)",
	} {
		if !strings.Contains(block, wanted) {
			t.Fatalf("runSiteAdapter lost %s", wanted)
		}
	}
}

// The adapter is authorized by the core exactly like every other view action,
// and the answer — not the request — supplies the body that runs.
func TestSiteAdapterIsAuthorizedBeforeItRuns(t *testing.T) {
	source, err := os.ReadFile("assets/bootstrap.js")
	if err != nil {
		t.Fatal(err)
	}
	bootstrapSource := strings.ReplaceAll(string(source), "\r\n", "\n")
	start := strings.Index(bootstrapSource, "case 'browser.view.adapter': {")
	if start < 0 {
		t.Fatal("bootstrap.js has no browser.view.adapter")
	}
	end := strings.Index(bootstrapSource[start:], "case 'browser.view.close': {")
	if end < 0 {
		t.Fatal("browser.view.adapter is not followed by browser.view.close")
	}
	block := bootstrapSource[start : start+end]
	coreCall := strings.Index(block, "coreBrowserAction('browser.adapter', payload.token")
	runCall := strings.Index(block, "runSiteAdapter(view.webContents, answer.adapter)")
	if coreCall < 0 || runCall < 0 {
		t.Fatalf("adapter does not authorize then run: %q", block)
	}
	if coreCall > runCall {
		t.Fatal("the adapter ran before the core authorized it")
	}
	if strings.Contains(block, "payload.adapter,") {
		t.Fatal("the adapter executed the request body instead of the authorized answer")
	}
	// A page-reading action runs on the page the view is actually on. A sign-in
	// redirect or an in-page route change moves it away from the address the core
	// recorded when the view opened, and the plugin already owns that navigation
	// through browser:view. What must still hold is the scheme: a view that
	// reached file:, data: or javascript: is refused.
	for _, action := range []string{"case 'browser.view.extract': {", "case 'browser.view.adapter': {"} {
		start := strings.Index(bootstrapSource, action)
		if start < 0 {
			t.Fatalf("bootstrap.js lost %s", action)
		}
		end := strings.Index(bootstrapSource[start+len(action):], "case 'browser.view.")
		if end < 0 {
			t.Fatalf("%s is not followed by another view action", action)
		}
		block := bootstrapSource[start : start+len(action)+end]
		if !strings.Contains(block, "livePluginView(answer, target);") {
			t.Fatalf("%s no longer resolves the view the core authorized", action)
		}
		if !strings.Contains(block, "currentPluginPage(view);") {
			t.Fatalf("%s does not check the page it is about to read", action)
		}
		if strings.Contains(block, "browserURL(answer.url)") {
			t.Fatalf("%s still requires the page to sit on the recorded address", action)
		}
	}
	guard := strings.Index(bootstrapSource, "function currentPluginPage(view) {")
	if guard < 0 {
		t.Fatal("bootstrap.js has no currentPluginPage")
	}
	guardEnd := strings.Index(bootstrapSource[guard:], "\n  }\n")
	if guardEnd < 0 {
		t.Fatal("currentPluginPage is not terminated")
	}
	guardBlock := bootstrapSource[guard : guard+guardEnd]
	for _, wanted := range []string{"view.webContents.getURL()", "isBrowserURL(current)"} {
		if !strings.Contains(guardBlock, wanted) {
			t.Fatalf("currentPluginPage lost %s", wanted)
		}
	}
}
