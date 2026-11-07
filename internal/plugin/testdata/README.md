# Test wasm fixtures

Tiny hand-assembled modules used by `internal/plugin` tests. They are checked
in as binaries so the tests run without a Go or Rust wasm toolchain, and they
are only a few dozen bytes each.

| File         | Exports                     | Behaviour                              |
| ------------ | --------------------------- | -------------------------------------- |
| `echo.wasm`  | `memory`, `alloc`, `process`| returns packed(1024, 29) → `{"matched":true,"text":"hit"}` |
| `silent.wasm`| `memory`, `alloc`, `process`| returns packed(1024, 17) → `{"matched":false}` |
| `noproc.wasm`| `memory`                    | no `process` export                    |
| `alloc.wasm` | `memory`, `alloc`           | `process` missing                      |
| `spin.wasm`  | `memory`, `alloc`, `process`| `process` loops forever                |

All modules share the ABI documented in `plugins/README.md`:

- `alloc(size i32) -> i32`
- `process(ptr i32, len i32) -> i64` where the low 32 bits are the result
  pointer and the high 32 bits the result length.
