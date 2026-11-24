# Repository instructions

<!-- BEGIN PROXIMAL BUILD CACHE -->
## Build storage on proximal

On `proximal`, put build trees, compiler caches and campaign scratch under
`$HOME/.cache` on SSD3. Check `findmnt -T "$HOME/.cache"` before a large build
and confirm it resolves to the same device as `/srv/ssd3`.

Reuse a build directory per repo and worktree when the tool supports it. For
an isolated run, `build_dir=$(mktemp -d "$HOME/.cache/build.XXXXXXXX")` creates
one on SSD3. Set `CARGO_TARGET_DIR="$build_dir"` in the build environment or
pass `--target-dir "$build_dir"` to Cargo. For CMake, use `-B "$build_dir"`.
Pass that path through wrappers that select their own output directory. If a
tool requires an in-tree build, use an SSD3-backed worktree. Copy the final
binaries, reports and other required evidence to the run's durable location.
<!-- END PROXIMAL BUILD CACHE -->
