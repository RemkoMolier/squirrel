package webhook

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	types "k8s.io/apimachinery/pkg/types"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/engine"
)

// withTestCache swaps the package-global compileCache for a fresh
// instance for the duration of the test, then restores the
// production cache via t.Cleanup. Without this, running the tests
// under `-count=N` would let entries from iteration K leak into
// iteration K+1 and the "first call is a cache miss" branches
// would silently run as cache hits, making the test name lie
// about what it exercises.
func withTestCache(t *testing.T) {
	t.Helper()
	saved := compileCache
	compileCache = newCompileCache(compileCacheSize)
	t.Cleanup(func() { compileCache = saved })
}

// TestCompileWithCacheKeyedOnUIDAndGeneration pins the cache hit
// contract: two compile requests with the same (UID, generation)
// share the cached []CompiledRule, and a generation bump
// invalidates that cache entry.
func TestCompileWithCacheKeyedOnUIDAndGeneration(t *testing.T) {
	// Not t.Parallel: the test mutates the process-wide compileCache.
	withTestCache(t)

	const policyName = "test"
	uid := types.UID("11111111-1111-1111-1111-111111111111")
	rules := []squirrelv1alpha1.Rule{
		{Match: rawMatch(t, `"docker.io/**:*"`)},
	}
	defaultTarget := &squirrelv1alpha1.Target{Registry: "mirror.internal"}
	source := func(idx int) engine.RuleSource {
		return engine.RuleSource{
			Scope:     engine.ScopeCluster,
			Name:      policyName,
			RuleIndex: idx,
		}
	}

	// The contract is observed via cache state (Contains / Len)
	// rather than slice-pointer identity: a future defensive-copy
	// change inside compileWithCache would silently invalidate the
	// pointer comparison while still honouring the
	// hit-skips-recompile contract.
	keyGen1 := compileCacheKey{uid: uid, generation: 1}
	keyGen2 := compileCacheKey{uid: uid, generation: 2}

	// First call: cache miss -> populates.
	if compileCache.Contains(keyGen1) {
		t.Fatalf("withTestCache fixture: cache must start empty for keyGen1")
	}
	first, firstErrs := compileWithCache(compileCache, keyGen1, rules, defaultTarget, "", source)
	if len(firstErrs) != 0 {
		t.Fatalf("unexpected per-rule errors on first compile: %v", firstErrs)
	}
	if len(first) != 1 {
		t.Fatalf("first compile rules: got %d, want 1", len(first))
	}
	if !compileCache.Contains(keyGen1) {
		t.Errorf("after first call: cache must contain keyGen1 (cache miss must populate)")
	}

	// Second call with same (UID, generation): cache hit. Population
	// state must be unchanged - no new keys, no eviction.
	lenBefore := compileCache.Len()
	_, _ = compileWithCache(compileCache, keyGen1, rules, defaultTarget, "", source)
	if got := compileCache.Len(); got != lenBefore {
		t.Errorf("cache length changed on a cache hit: got %d, want %d", got, lenBefore)
	}

	// Third call with generation bumped: cache miss for keyGen2; the
	// gen=1 entry stays. (A bump does not evict the prior generation;
	// LRU eviction is exercised by a dedicated test.)
	if compileCache.Contains(keyGen2) {
		t.Fatalf("withTestCache fixture: keyGen2 must not be present before its first compile")
	}
	_, _ = compileWithCache(compileCache, keyGen2, rules, defaultTarget, "", source)
	if !compileCache.Contains(keyGen2) {
		t.Errorf("after third call: cache must contain keyGen2 (generation bump must populate as miss)")
	}
}

// TestCompileWithCacheBypassesEmptyUID pins the test-fixture
// escape hatch: when the UID is empty (no apiserver-issued UID
// yet), compileWithCache skips the cache entirely and compiles
// fresh. Without this, fake-client policies in tests would all
// collide on (zero UID, generation=1).
func TestCompileWithCacheBypassesEmptyUID(t *testing.T) {
	// Not t.Parallel: the test mutates the process-wide compileCache.
	withTestCache(t)

	const policyName = "test"
	rules := []squirrelv1alpha1.Rule{
		{Match: rawMatch(t, `"docker.io/**:*"`)},
	}
	defaultTarget := &squirrelv1alpha1.Target{Registry: "mirror.internal"}
	source := func(idx int) engine.RuleSource {
		return engine.RuleSource{
			Scope:     engine.ScopeCluster,
			Name:      policyName,
			RuleIndex: idx,
		}
	}

	// Empty-UID compiles must not populate the cache; observing
	// Len() instead of slice-pointer identity insulates the test
	// from defensive-copy changes inside compileWithCache.
	_, _ = compileWithCache(compileCache,
		compileCacheKey{uid: "", generation: 1},
		rules,
		defaultTarget,
		"",
		source,
	)
	_, _ = compileWithCache(compileCache,
		compileCacheKey{uid: "", generation: 1},
		rules,
		defaultTarget,
		"",
		source,
	)
	if got := compileCache.Len(); got != 0 {
		t.Errorf("compileCache populated despite empty UID: got Len=%d, want 0", got)
	}
}

// TestCompileWithCacheEvictsLeastRecentlyUsed pins the LRU bound:
// once the cache is full, inserting an extra entry evicts the
// least-recently-used one. Without this test a regression that
// dropped golang-lru in favour of an unbounded map (or that ran
// the LRU at the wrong size) would not fail any unit test and
// would re-introduce the memory leak the LRU was added to fix.
func TestCompileWithCacheEvictsLeastRecentlyUsed(t *testing.T) {
	// Not t.Parallel: the test mutates the process-wide compileCache.

	// Stand up a 2-entry cache so eviction is provable in three
	// inserts. Use the same swap-and-restore pattern as
	// withTestCache so the production cache survives the test.
	saved := compileCache
	compileCache = newCompileCache(2)
	t.Cleanup(func() { compileCache = saved })

	rules := []squirrelv1alpha1.Rule{
		{Match: rawMatch(t, `"docker.io/**:*"`)},
	}
	defaultTarget := &squirrelv1alpha1.Target{Registry: "mirror.internal"}
	source := func(idx int) engine.RuleSource {
		return engine.RuleSource{
			Scope:     engine.ScopeCluster,
			Name:      "test",
			RuleIndex: idx,
		}
	}
	keyA := compileCacheKey{uid: types.UID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), generation: 1}
	keyB := compileCacheKey{uid: types.UID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"), generation: 1}
	keyC := compileCacheKey{uid: types.UID("cccccccc-cccc-cccc-cccc-cccccccccccc"), generation: 1}

	// Insert A and B (cache now full).
	_, _ = compileWithCache(compileCache, keyA, rules, defaultTarget, "", source)
	_, _ = compileWithCache(compileCache, keyB, rules, defaultTarget, "", source)

	// Insert C: must evict A (least-recently-used).
	_, _ = compileWithCache(compileCache, keyC, rules, defaultTarget, "", source)

	// Eviction is observable directly via Contains: A is gone, B and
	// C remain. Asserting cache state insulates the test from
	// defensive-copy changes inside compileWithCache that would
	// invalidate a pointer-equality check.
	if compileCache.Contains(keyA) {
		t.Errorf("LRU did not evict the oldest entry (keyA still in cache after C insert)")
	}
	if !compileCache.Contains(keyB) {
		t.Errorf("LRU evicted keyB; want keyA evicted instead")
	}
	if !compileCache.Contains(keyC) {
		t.Errorf("after C insert: cache must contain keyC")
	}
}

// rawMatch builds the apiextensionsv1.JSON value the Rule struct
// stores in its Match field. The compile path runs MatchExpression()
// to parse this back into a MatchExpr; we pass the JSON bytes
// directly to keep the test free of API decoding wiring.
func rawMatch(t *testing.T, s string) apiextensionsv1.JSON {
	t.Helper()
	return apiextensionsv1.JSON{Raw: []byte(s)}
}
