package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Test fixtures are tiny hand-assembled wasm modules under testdata/. They
// exercise the host ABI without needing a Go or Rust wasm toolchain:
//
//	echo.wasm    memory + process() -> packed(1024, 27) reading
//	             `{"matched":true,"text":"hit"}` from the data segment
//	silent.wasm  memory + process() -> 0 (no match)
//	noproc.wasm  memory only, no process export
//	alloc.wasm   memory + alloc() -> 2048 + process() -> 0
//	spin.wasm    memory + alloc() + process() that loops forever
const (
	fixtureEcho   = "echo.wasm"
	fixtureSilent = "silent.wasm"
	fixtureNoProc = "noproc.wasm"
	fixtureAlloc  = "alloc.wasm"
	fixtureRich   = "rich.wasm"
	fixtureSpin   = "spin.wasm"
)

// writePlugin creates a per-plugin folder layout on disk.
func writePlugin(t *testing.T, root, name, manifest, fixture string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".wasm"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func manifestFor(name, extra string) string {
	return "name: " + name + "\napi_version: " + APIVersion + "\n" + extra
}

func TestLoadDirMissingDirectory(t *testing.T) {
	if _, err := LoadDir(filepath.Join(t.TempDir(), "nope"), Limits{}); err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestLoadDirEmptyPath(t *testing.T) {
	engine, err := LoadDir("", Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := engine.Names(); len(got) != 0 {
		t.Fatalf("expected no plugins, got %v", got)
	}
}

func TestLoadDirIgnoresNonManifests(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine, err := LoadDir(dir, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := engine.Names(); len(got) != 0 {
		t.Fatalf("expected no plugins, got %v", got)
	}
}

func TestLoadDirRejectsWrongAPIVersion(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "bad", "name: bad\napi_version: mailer.plugin.v0\n", fixtureEcho)
	if _, err := LoadDir(dir, Limits{}); err == nil {
		t.Fatal("expected api version error")
	}
}

func TestLoadDirRequiresName(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "anon", "api_version: "+APIVersion+"\n", fixtureEcho)
	if _, err := LoadDir(dir, Limits{}); err == nil {
		t.Fatal("expected name required error")
	}
}

func TestLoadDirSkipsDisabledPlugins(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "off", manifestFor("off", "enabled: false\n"), fixtureEcho)
	engine, err := LoadDir(dir, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := engine.Names(); len(got) != 0 {
		t.Fatalf("expected disabled plugin to be skipped, got %v", got)
	}
}

func TestLoadDirResolvesEntrypointFromPluginFolder(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "otp", manifestFor("otp", ""), fixtureEcho)
	engine, err := LoadDir(dir, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := engine.Names(); len(got) != 1 || got[0] != "otp" {
		t.Fatalf("unexpected names %v", got)
	}
}

func TestProcessReturnsFirstMatchByPriority(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "lowat", manifestFor("low", "priority: 1\n"), fixtureEcho)
	writePlugin(t, dir, "high", manifestFor("high", "priority: 9\n"), fixtureEcho)
	engine, err := LoadDir(dir, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, err := engine.Process(context.Background(), Mail{Subject: "s"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Plugin != "high" {
		t.Fatalf("expected high priority plugin, got %q", res.Plugin)
	}
	if res.Text != "hit" {
		t.Fatalf("expected text %q, got %q", "hit", res.Text)
	}
}

func TestProcessSkipsUnmatchedAndFallsThrough(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "silent", manifestFor("silent", "priority: 9\n"), fixtureSilent)
	writePlugin(t, dir, "loud", manifestFor("loud", "priority: 1\n"), fixtureEcho)
	engine, err := LoadDir(dir, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, err := engine.Process(context.Background(), Mail{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Plugin != "loud" {
		t.Fatalf("expected plugin loud, got %q", res.Plugin)
	}
}

func TestProcessRequiresProcessExport(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "nop", manifestFor("nop", ""), fixtureNoProc)
	engine, err := LoadDir(dir, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := engine.Process(context.Background(), Mail{}); err == nil {
		t.Fatal("expected error for module without process export")
	}
}

func TestProcessRejectsOversizedInput(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "echo", manifestFor("echo", "limits:\n  max_input: 32\n"), fixtureAlloc)
	engine, err := LoadDir(dir, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	big := Mail{Subject: string(make([]byte, 4096))}
	if _, err := engine.Process(context.Background(), big); err == nil {
		t.Fatal("expected input size error")
	}
}

func TestProcessRejectsOversizedOutput(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "mem", manifestFor("mem", "limits:\n  max_output: 4\n"), fixtureEcho)
	engine, err := LoadDir(dir, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := engine.Process(context.Background(), Mail{}); err == nil {
		t.Fatal("expected output size error")
	}
}

func TestProcessHonoursTimeout(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "spin", manifestFor("spin", "limits:\n  timeout: 50ms\n"), fixtureSpin)
	engine, err := LoadDir(dir, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	start := time.Now()
	if _, err := engine.Process(context.Background(), Mail{}); err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout not enforced, took %s", elapsed)
	}
}

func TestTimeoutDefaultsWhenManifestOmitsIt(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "spin", manifestFor("spin", ""), fixtureSpin)
	engine, err := LoadDir(dir, Limits{Timeout: 30 * time.Millisecond, MaxInput: 4096, MaxOutput: 4096})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := engine.Process(context.Background(), Mail{}); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestNamesNilEngine(t *testing.T) {
	var engine *Engine
	if got := engine.Names(); got != nil {
		t.Fatalf("expected nil names, got %v", got)
	}
}

func TestProcessDecodesFieldsAndNotifiers(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "rich", manifestFor("rich", ""), fixtureRich)
	engine, err := LoadDir(dir, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, err := engine.Process(context.Background(), Mail{Subject: "s"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Matched {
		t.Fatal("expected matched result")
	}
	if got := res.Fields["code"]; got != "123456" {
		t.Errorf("fields[code] = %q, want 123456", got)
	}
	if len(res.Notifiers) != 1 || res.Notifiers[0] != "telegram" {
		t.Errorf("notifiers = %v, want [telegram]", res.Notifiers)
	}
	if res.Title != "T" || res.Text != "tx" {
		t.Errorf("title/text = %q/%q, want T/tx", res.Title, res.Text)
	}
}
