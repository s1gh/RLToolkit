// Statfeed eventName registry. The string literal below is rewritten
// at server boot by renderSDKJS in backend/internal/server/assets.go,
// substituting the verified Statfeed names from
// backend/internal/types/statfeed.go.
//
// Why a string literal rather than the old comment + empty-object
// placeholder? esbuild reformats comment+literal pairs (it inserts a
// newline between them), breaking byte-exact Go-side substitution.
// String literals come through bundlers verbatim. Note: esbuild
// normalizes single quotes → double quotes, so the marker must be
// written with double quotes here to match what the bundle emits.

export const stats = JSON.parse("__RLT_STATS_JSON__");
stats.known = new Set(Object.values(stats));
Object.freeze(stats);
