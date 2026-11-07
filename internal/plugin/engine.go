package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"gopkg.in/yaml.v3"
)

// startFunction is the initializer exported by WASI reactor modules
// (TinyGo `-buildmode=c-shared`, Rust cdylib). Calling it runs the language
// runtime setup that module init would otherwise perform.
const startFunction = "_initialize"

type Engine struct {
	plugins []wasmPlugin
}

// Names returns loaded plugin names in execution order.
func (e *Engine) Names() []string {
	if e == nil {
		return nil
	}
	names := make([]string, 0, len(e.plugins))
	for _, p := range e.plugins {
		names = append(names, p.manifest.Name)
	}
	return names
}

type wasmPlugin struct {
	manifest Manifest
	wasm     []byte
	patterns []compiledPattern
}

// LoadDir loads plugins from dir. A flat layout (dir/x.yaml + dir/x.wasm)
// and a per-plugin folder layout (dir/x/plugin.yaml + dir/x/x.wasm) are both
// supported; relative entrypoints resolve against the manifest's directory.
func LoadDir(dir string, defaults Limits) (*Engine, error) {
	if strings.TrimSpace(dir) == "" {
		return &Engine{}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read plugin directory: %w", err)
	}
	manifests, err := manifestPaths(dir, entries)
	if err != nil {
		return nil, err
	}
	var plugins []wasmPlugin
	for _, manifestPath := range manifests {
		baseDir := filepath.Dir(manifestPath)
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", manifestPath, err)
		}
		var manifest Manifest
		if err := yaml.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("parse %s: %w", manifestPath, err)
		}
		if manifest.Enabled != nil && !*manifest.Enabled {
			continue
		}
		if manifest.Name == "" {
			return nil, fmt.Errorf("%s: name is required", manifestPath)
		}
		if manifest.APIVersion == "" {
			manifest.APIVersion = APIVersion
		}
		if manifest.APIVersion != APIVersion {
			return nil, fmt.Errorf("%s: unsupported api_version %q", manifestPath, manifest.APIVersion)
		}
		if manifest.Entrypoint == "" {
			// Prefer the plugin folder's own name over the manifest filename so
			// the conventional x/plugin.yaml + x/x.wasm layout just works.
			defaultName := filepath.Base(baseDir)
			if defaultName == "." || defaultName == string(filepath.Separator) {
				defaultName = strings.TrimSuffix(filepath.Base(manifestPath), filepath.Ext(manifestPath))
			}
			manifest.Entrypoint = defaultName + ".wasm"
		}
		if manifest.Limits.Timeout <= 0 {
			manifest.Limits.Timeout = defaults.Timeout
		}
		if manifest.Limits.MaxInput <= 0 {
			manifest.Limits.MaxInput = defaults.MaxInput
		}
		if manifest.Limits.MaxOutput <= 0 {
			manifest.Limits.MaxOutput = defaults.MaxOutput
		}
		if manifest.Limits.MaxOutput <= 0 {
			manifest.Limits.MaxOutput = DefaultMaxOutput
		}
		patterns, err := compilePatterns(manifest.Patterns)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", manifestPath, err)
		}
		wasm, err := os.ReadFile(filepath.Join(baseDir, manifest.Entrypoint))
		if err != nil {
			return nil, fmt.Errorf("plugin %s: read %s: %w", manifest.Name, manifest.Entrypoint, err)
		}
		plugins = append(plugins, wasmPlugin{manifest: manifest, wasm: wasm, patterns: patterns})
	}
	sort.SliceStable(plugins, func(i, j int) bool {
		if plugins[i].manifest.Priority != plugins[j].manifest.Priority {
			return plugins[i].manifest.Priority > plugins[j].manifest.Priority
		}
		return plugins[i].manifest.Name < plugins[j].manifest.Name
	})
	return &Engine{plugins: plugins}, nil
}

// manifestPaths returns every plugin manifest under dir, supporting both the
// flat and the per-plugin folder layouts.
func manifestPaths(dir string, entries []os.DirEntry) ([]string, error) {
	var out []string
	for _, entry := range entries {
		switch {
		case entry.IsDir():
			inner, err := os.ReadDir(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
			}
			for _, f := range inner {
				if !f.IsDir() && strings.HasSuffix(f.Name(), ".yaml") {
					out = append(out, filepath.Join(dir, entry.Name(), f.Name()))
				}
			}
		case strings.HasSuffix(entry.Name(), ".yaml"):
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	return out, nil
}

func (e *Engine) Process(ctx context.Context, msg Mail) (Result, error) {
	input, err := json.Marshal(msg)
	if err != nil {
		return Result{}, fmt.Errorf("marshal plugin input: %w", err)
	}
	for _, p := range e.plugins {
		if p.manifest.Limits.MaxInput > 0 && len(input) > p.manifest.Limits.MaxInput {
			return Result{}, fmt.Errorf("plugin %s: input exceeds limit", p.manifest.Name)
		}
		result, err := run(ctx, p, input)
		if err != nil {
			return Result{}, fmt.Errorf("plugin %s: %w", p.manifest.Name, err)
		}
		if result.Matched {
			result.Plugin = p.manifest.Name
			if err := mergePatternFields(&result, p.patterns, &msg); err != nil {
				return Result{}, fmt.Errorf("plugin %s: %w", p.manifest.Name, err)
			}
			return result, nil
		}
	}
	return Result{}, nil
}

// compileModule compiles a module in the background. wazero does not check for
// interrupts while compiling, so a guest crafted to wedge the compiler would
// otherwise block the poller forever; the caller's deadline bounds the wait.
func compileModule(ctx context.Context, runtime wazero.Runtime, wasm []byte) (wazero.CompiledModule, error) {
	type outcome struct {
		compiled wazero.CompiledModule
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		compiled, err := runtime.CompileModule(context.WithoutCancel(ctx), wasm)
		done <- outcome{compiled: compiled, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case out := <-done:
		return out.compiled, out.err
	}
}

func run(parent context.Context, p wasmPlugin, input []byte) (Result, error) {
	timeout := p.manifest.Limits.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runGuest(ctx, p, input)
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-ctx.Done():
		// The guest exceeded its budget. wazero interrupts normal guest code,
		// but not compilation or teardown, so do not wait for the goroutine.
		return Result{}, fmt.Errorf("timeout after %s: %w", timeout, ctx.Err())
	case out := <-done:
		return out.result, out.err
	}
}

func runGuest(ctx context.Context, p wasmPlugin, input []byte) (Result, error) {
	// Use the interpreter and enable context-done termination. The default
	// wazevo compiler cannot interrupt a tight guest loop at all, and even the
	// interpreter only inserts periodic termination checks when
	// WithCloseOnContextDone is set (it is off by default). Without both, a
	// runaway plugin wedges the poller and leaks a spinning goroutine.
	runtime := wazero.NewRuntimeWithConfig(ctx,
		wazero.NewRuntimeConfigInterpreter().WithCloseOnContextDone(true))
	defer runtime.Close(context.WithoutCancel(ctx))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		return Result{}, fmt.Errorf("wasi: %w", err)
	}
	compiled, err := compileModule(ctx, runtime, p.wasm)
	if err != nil {
		return Result{}, fmt.Errorf("compile: %w", err)
	}
	defer compiled.Close(context.WithoutCancel(ctx))
	mod, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithStartFunctions(startFunction))
	if err != nil {
		return Result{}, fmt.Errorf("instantiate: %w", err)
	}
	defer mod.Close(context.WithoutCancel(ctx))

	process := mod.ExportedFunction("process")
	if process == nil {
		return Result{}, fmt.Errorf("missing exported process function")
	}
	alloc := mod.ExportedFunction("alloc")
	if alloc == nil {
		return Result{}, fmt.Errorf("missing exported alloc function")
	}
	memory := mod.Memory()
	if memory == nil {
		return Result{}, fmt.Errorf("missing exported memory")
	}
	if init := mod.ExportedFunction(startFunction); init != nil {
		if _, err := init.Call(ctx); err != nil {
			return Result{}, fmt.Errorf("initialize: %w", err)
		}
	}
	ptrs, err := alloc.Call(ctx, uint64(len(input)))
	if err != nil || len(ptrs) == 0 {
		return Result{}, fmt.Errorf("alloc: %w", err)
	}
	ptr := uint32(ptrs[0])
	if ptr == 0 || !memory.Write(ptr, input) {
		return Result{}, fmt.Errorf("write input failed")
	}
	outs, err := process.Call(ctx, uint64(ptr), uint64(len(input)))
	if err != nil || len(outs) != 1 {
		return Result{}, fmt.Errorf("process: expected one packed result: %w", err)
	}
	packed := outs[0]
	if packed == 0 {
		return Result{}, fmt.Errorf("plugin returned no output")
	}
	resultPtr, resultLen := uint32(packed), uint32(packed>>32)
	limit := p.manifest.Limits.MaxOutput
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	if int(resultLen) > limit {
		return Result{}, fmt.Errorf("output exceeds limit")
	}
	output, ok := memory.Read(resultPtr, resultLen)
	if !ok {
		return Result{}, fmt.Errorf("read output failed")
	}
	var result Result
	if err := json.Unmarshal(output, &result); err != nil {
		return Result{}, fmt.Errorf("decode output: %w", err)
	}
	return result, nil
}
