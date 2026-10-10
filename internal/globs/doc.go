// Package globs ports the docSync glob engine from TypeScript to Go.
//
// It is a faithful port of the TypeScript sources under
// packages/core/src/globs/ in the docSync repository:
//
//   - engine.ts      — CompileGlob/MatchPaths (picomatch-based glob matching)
//   - specificity.ts — GlobSpecificity/ScoreGlob/CompareSpecificity
//   - conflicts.ts   — CandidateResolution/ResolveOverlap (restrictiveness ranking)
//   - guards.ts      — GuardGlob (glob safety/validation rules)
//
// Semantics preserved from the TypeScript reference (picomatch is pinned to
// `{ dot: true, windows: false }`):
//
//   - `**` globstar matches across path separators (zero or more segments);
//     `*` and `?` never match `/`.
//   - Character classes `[...]` (with ranges, `^` negation, POSIX classes)
//     and brace expansion `{a,b}` are supported; braces without a comma or
//     range are literal.
//   - A leading `!` negates a pattern (picomatch negation); an even number of
//     leading `!` cancels.
//   - `dot: true` — hidden files and dotted directories DO match wildcards
//     (`**/*.yml` matches `.hidden/config.yml`); docSync opts in, so a
//     pattern's own leading dot is not privileged. The `.` and `..` segments
//     themselves never match a wildcard.
//   - POSIX path separators only (backslash patterns are guard-rejected);
//     patterns are anchored to the whole path, and a path exactly equal to
//     the pattern matches literally (picomatch.test semantics).
package globs
