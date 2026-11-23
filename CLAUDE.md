# Project instructions

## Commits

- Subjects follow [Conventional Commits](https://www.conventionalcommits.org):
  `<type>(<scope>): <description>`, for example `feat(ingestor): add Kraken order book adapter`.
- Types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`,
  `revert`. Append `!` after the type or scope for a breaking change.
- Scope is optional and names the area touched: `ingestor`, `detector`, `deploy`, `ci`, `proto`.
  Leave it out when a change spans several areas.
- Description: English, imperative mood, lowercase, no trailing period. Whole subject at most
  72 characters.
- `scripts/check-commits.sh` enforces this in CI and in the local `commit-msg` hook
  (`make hooks`). Run `make check-commits` before pushing.
