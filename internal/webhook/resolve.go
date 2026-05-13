package webhook

import (
	"context"
	"fmt"

	lru "github.com/hashicorp/golang-lru/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	client "sigs.k8s.io/controller-runtime/pkg/client"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/engine"
)

// compileCacheKey identifies a compiled rule set by its source policy.
// UID is globally unique per resource lifetime; generation bumps on
// every spec change, so the (UID, generation) pair is a content
// identity for the rules emitted by compilePolicyRules. A delete +
// recreate produces a fresh UID and bypasses the cache; a normal
// spec edit bumps generation and likewise bypasses.
type compileCacheKey struct {
	uid        types.UID
	generation int64
}

// compileCacheValue stores the per-(UID,generation) compile result.
// Two slices, mirroring compilePolicyRules' return shape: rules
// successfully compiled, plus per-rule non-fatal errors that the
// webhook still surfaces through InvalidAdmissionRulesTotal.
type compileCacheValue struct {
	rules []engine.CompiledRule
	errs  []error
}

// compileCacheSize bounds the LRU's footprint. The expected working
// set is the number of currently-installed policies (typical
// single-digit to low-hundreds); 1024 entries gives several orders
// of magnitude of headroom for delete+recreate churn before the LRU
// starts evicting entries that are still hot. Each entry holds a
// small []CompiledRule + the per-rule error slice, both bounded by
// the policy's spec.rules length.
const compileCacheSize = 1024

// compileCache is the package-level fallback the resolver uses when
// a caller passes no per-PodMutator cache. New production code
// constructs a fresh cache on every PodMutator (see SetupManager)
// so per-process state stays bound to the handler that owns it;
// the package-level fallback is preserved as a default for tests
// and ad-hoc resolver use that does not thread a cache.
//
// The cache memoises compilePolicyRules output keyed by source
// policy identity. controller-tools markers cannot put per-rule
// glob compilation on the CRD storage layer, so the resolver would
// compile matches every admission without this cache. For a stable
// policy set the cache reduces the per-admission cost from
// O(rules-per-policy * policies) to O(1) lookups per policy.
//
// LRU-bounded: an earlier sync.Map implementation kept entries
// forever, which leaked memory under delete+recreate churn (the
// recreated policy got a fresh UID, the old entry stayed indefinitely).
// golang-lru/v2 is already a transitive dependency through
// controller-runtime, so adding it here has no new module-graph
// cost. The Cache is concurrent-safe; controller-runtime's
// admission worker pool calls Handle from multiple goroutines.
var compileCache = newCompileCache(compileCacheSize)

func newCompileCache(size int) *lru.Cache[compileCacheKey, compileCacheValue] {
	// lru.New returns an error only on a non-positive size; we
	// pass a positive const so panic-on-error is acceptable here.
	c, err := lru.New[compileCacheKey, compileCacheValue](size)
	if err != nil {
		panic(fmt.Sprintf("compileCache: lru.New(%d): %v", size, err))
	}
	return c
}

// ApplicableRules returns the flat slice of compiled rules whose
// parent policies are Accepted=True (for the current spec generation)
// and whose scope filter (namespace selector for ClusterImagePolicy,
// namespace match for ImagePolicy) applies to the given namespace.
//
// namespaceLabels is the target namespace's labels (as observed by the
// webhook before this call). The caller fetches them once per
// admission request rather than letting the resolver re-fetch them per
// policy.
//
// Three return values, in order:
//
//   - rules: the flat compiled-rule slice the engine consumes.
//   - perRuleErrs: non-fatal compile failures, one per rule whose
//     match expression failed to compile despite the reconciler
//     having accepted the parent policy. The webhook logs these and
//     increments squirrel_invalid_admission_rules_total (Phase 7
//     wiring); they do not block the admission, and the rules slice
//     still contains every successfully-compiled rule.
//   - fatalErr: a List failure or other infrastructure error that
//     means the resolver could not determine the full policy set.
//     The webhook must admit the pod *unchanged* in this case rather
//     than applying a partial policy set - a missing cluster-wide
//     skip rule (for example) could otherwise let a normally-blocked
//     image through. fatalErr is non-nil iff rules and perRuleErrs
//     are both nil.
func ApplicableRules(
	ctx context.Context,
	c client.Reader,
	namespace string,
	namespaceLabels map[string]string,
) ([]engine.CompiledRule, []error, error) {
	return applicableRulesWith(ctx, c, namespace, namespaceLabels, compileCache)
}

// applicableRulesWith is the cache-injecting form ApplicableRules
// delegates to. PodMutator.Handle threads its own per-handler cache
// in via this helper so multiple mutator instances in the same
// process do not share an LRU - a step toward future per-tenant or
// per-cluster operator topologies.
func applicableRulesWith(
	ctx context.Context,
	c client.Reader,
	namespace string,
	namespaceLabels map[string]string,
	cache *lru.Cache[compileCacheKey, compileCacheValue],
) ([]engine.CompiledRule, []error, error) {
	clusterRules, clusterErrs, clusterFatal := applicableClusterPolicies(ctx, c, namespaceLabels, cache)
	if clusterFatal != nil {
		return nil, nil, clusterFatal
	}
	namespacedRules, namespacedErrs, namespacedFatal := applicableNamespacedPolicies(ctx, c, namespace, cache)
	if namespacedFatal != nil {
		return nil, nil, namespacedFatal
	}

	compiled := make([]engine.CompiledRule, 0, len(clusterRules)+len(namespacedRules))
	compiled = append(compiled, clusterRules...)
	compiled = append(compiled, namespacedRules...)

	errs := make([]error, 0, len(clusterErrs)+len(namespacedErrs))
	errs = append(errs, clusterErrs...)
	errs = append(errs, namespacedErrs...)

	return compiled, errs, nil
}

// applicableClusterPolicies lists ClusterImagePolicy resources from
// the cache and returns the compiled rules from the subset that is
// Accepted (at the current spec generation) and whose
// namespaceSelector matches namespaceLabels. The third return is the
// fatal-error channel; see ApplicableRules.
//
// Scaling: this is an O(policies) scan on every admission. The List
// is cache-served and the per-policy work (selector compile + match
// + compile-cache hit) is sub-millisecond, so at the CRD's
// MaxItems=128 cluster-policy cap the per-admission cost is bounded
// by single-digit milliseconds - well under the kubelet's admission
// timeout. A pre-indexed `selector -> [policy]` lookup would be the
// next step if MaxItems ever needs to grow into the thousands, but
// the apiserver's resourceVersion cache + the compile cache cover
// the current cap comfortably.
func applicableClusterPolicies(
	ctx context.Context,
	c client.Reader,
	namespaceLabels map[string]string,
	cache *lru.Cache[compileCacheKey, compileCacheValue],
) ([]engine.CompiledRule, []error, error) {
	var list squirrelv1alpha1.ClusterImagePolicyList
	if err := c.List(ctx, &list); err != nil {
		return nil, nil, fmt.Errorf("list ClusterImagePolicy: %w", err)
	}

	var compiled []engine.CompiledRule
	var errs []error
	labelSet := labels.Set(namespaceLabels)

	for i := range list.Items {
		policy := &list.Items[i]

		if !isAccepted(policy.Status.Conditions, policy.Generation) {
			continue
		}

		// LabelSelectorAsSelector(nil) returns labels.Nothing() (matches
		// no namespace), but the design specifies that a nil
		// namespaceSelector applies to every opted-in namespace.
		// Substitute an empty selector so LabelSelectorAsSelector
		// returns labels.Everything().
		raw := policy.Spec.NamespaceSelector
		if raw == nil {
			raw = &metav1.LabelSelector{}
		}
		selector, err := metav1.LabelSelectorAsSelector(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("ClusterImagePolicy/%s: invalid namespaceSelector: %w", policy.Name, err))
			continue
		}
		if !selector.Matches(labelSet) {
			continue
		}

		name := policy.Name
		rules, ruleErrs := compileWithCache(
			cache,
			compileCacheKey{uid: policy.UID, generation: policy.Generation},
			policy.Spec.Rules,
			policy.Spec.DefaultTarget,
			policy.Spec.DefaultAction,
			func(idx int) engine.RuleSource {
				return engine.RuleSource{
					Scope:     engine.ScopeCluster,
					Name:      name,
					RuleIndex: idx,
				}
			},
		)
		compiled = append(compiled, rules...)
		for _, e := range ruleErrs {
			errs = append(errs, fmt.Errorf("ClusterImagePolicy/%s: %w", name, e))
		}
	}
	return compiled, errs, nil
}

// compileWithCache wraps compilePolicyRules with the compileCache
// memo. The cache key is (UID, generation); on a hit the cached
// rules + errors are returned unchanged. On a miss the compile runs
// once and the result is stored before being returned.
//
// The cached []CompiledRule is safe to share across admissions
// because every consumer treats it as read-only: the resolver
// concatenates slices, the engine sorts a local copy of each slice
// (or now a PreparedRules built from one), and the webhook only
// reads the engine's RuleSource fields off the Decision. Mutation
// would be a regression worth catching in a test, not a
// possibility worth defending against here.
func compileWithCache(
	cache *lru.Cache[compileCacheKey, compileCacheValue],
	key compileCacheKey,
	rules []squirrelv1alpha1.Rule,
	defaultTarget *squirrelv1alpha1.Target,
	defaultAction squirrelv1alpha1.Action,
	sourceFor func(idx int) engine.RuleSource,
) ([]engine.CompiledRule, []error) {
	// Nil cache falls back to the package-level default. Callers
	// that own a handler-scoped cache (PodMutator.Handle) pass it;
	// ad-hoc callers (tests, the deprecated ApplicableRules
	// signature) get the global.
	if cache == nil {
		cache = compileCache
	}
	// Empty UID means the policy was constructed without going
	// through the apiserver - typical for unit-test fixtures. Skip
	// the cache: keying everything to (zero UID, generation=1) would
	// collide every fixture into the same memoised result and
	// produce spectacularly confusing test failures. Production
	// objects always carry a UID set by the apiserver on create.
	if key.uid == "" {
		return compilePolicyRules(rules, defaultTarget, defaultAction, sourceFor)
	}
	if v, ok := cache.Get(key); ok {
		return v.rules, v.errs
	}
	compiled, errs := compilePolicyRules(rules, defaultTarget, defaultAction, sourceFor)
	cache.Add(key, compileCacheValue{rules: compiled, errs: errs})
	return compiled, errs
}

// applicableNamespacedPolicies lists ImagePolicy resources in the
// given namespace and returns the compiled rules from the Accepted
// (current-generation) subset. The third return is the fatal-error
// channel; see ApplicableRules.
func applicableNamespacedPolicies(
	ctx context.Context,
	c client.Reader,
	namespace string,
	cache *lru.Cache[compileCacheKey, compileCacheValue],
) ([]engine.CompiledRule, []error, error) {
	var list squirrelv1alpha1.ImagePolicyList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, nil, fmt.Errorf("list ImagePolicy in namespace %q: %w", namespace, err)
	}

	var compiled []engine.CompiledRule
	var errs []error
	for i := range list.Items {
		policy := &list.Items[i]

		if !isAccepted(policy.Status.Conditions, policy.Generation) {
			continue
		}

		name := policy.Name
		ns := policy.Namespace
		rules, ruleErrs := compileWithCache(
			cache,
			compileCacheKey{uid: policy.UID, generation: policy.Generation},
			policy.Spec.Rules,
			policy.Spec.DefaultTarget,
			policy.Spec.DefaultAction,
			func(idx int) engine.RuleSource {
				return engine.RuleSource{
					Scope:     engine.ScopeNamespaced,
					Name:      name,
					Namespace: ns,
					RuleIndex: idx,
				}
			},
		)
		compiled = append(compiled, rules...)
		for _, e := range ruleErrs {
			errs = append(errs, fmt.Errorf("ImagePolicy/%s/%s: %w", ns, name, e))
		}
	}
	return compiled, errs, nil
}

// isAccepted reports whether the policy's Accepted condition is True
// for the given spec generation. A missing condition, a stale
// observedGeneration (the reconciler has not yet evaluated the latest
// spec edit), or Accepted=False all return false: the webhook must
// not apply an unblessed spec just because a previous generation of
// the policy was accepted.
func isAccepted(conditions []metav1.Condition, generation int64) bool {
	cond := meta.FindStatusCondition(conditions, squirrelv1alpha1.ConditionAccepted)
	return cond != nil &&
		cond.Status == metav1.ConditionTrue &&
		cond.ObservedGeneration == generation
}
