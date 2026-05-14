package v1alpha1

// Target is the structured rewrite target on a Rule (or the policy-level
// DefaultTarget). Fields are literal strings that may contain
// `{placeholder}` tokens drawn from the grammar documented in
// docs/design/v1alpha1.md and implemented in internal/imageref.
//
// Merge semantics (per design): a rule's Target overrides the policy's
// DefaultTarget field-by-field; the effective merged Target must end up
// with a Registry. Per-field defaults for fields unset after the merge:
//
//	Repository -> "{repository}" (passthrough of the matched input)
//	Tags       -> ["{tag}"]      (passthrough of the matched input)
//
// Digest always passes through from the matched input; there is no
// target.digest field, which is the squirrel content-integrity guarantee
// (see ADR-tracked design discussion).
//
// CEL guard against tag + tags both being meaningfully set. Both
// presence AND non-empty content are checked so that an explicit
// empty form (`tag: ""` with a populated `tags`, or `tags: []` with
// a populated `tag`) is accepted at the CRD layer and resolved by
// the reconciler. The MaxLength bound on Tag and MaxItems bound on
// Tags below cap the CEL estimator's cost so the rule stays inside
// K8s 1.33+'s budget; without those bounds the rule estimates as
// "up to 10MiB string × unbounded array" and fails CRD installation
// with `estimated rule cost exceeds budget`.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.tag) && size(self.tag) > 0 && has(self.tags) && size(self.tags) > 0)",message="target.tag and target.tags are mutually exclusive"
type Target struct {
	// Registry is the target-registry template (literal or `{placeholder}`).
	// Required in the *effective* merged target: either Rule.Target.Registry
	// or Spec.DefaultTarget.Registry must be set.
	// +optional
	Registry string `json:"registry,omitempty"`

	// Repository is the target-repository template.
	// Defaults to "{repository}" (passthrough) when neither Rule.Target
	// nor Spec.DefaultTarget supplies one.
	// +optional
	Repository string `json:"repository,omitempty"`

	// Tags is the ordered list of tag templates. At admission time the
	// rewrite action uses the first template that renders to a non-empty
	// OCI-valid tag; remaining entries are reserved for future actions
	// (see internal/imageref.SelectTag).
	//
	// Mutually exclusive with Tag. MaxItems=16 caps the array for the
	// CEL cost estimator; in practice a tag fallback chain longer than
	// a handful of entries indicates a misuse of the feature.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Tags []string `json:"tags,omitempty"`

	// Tag is sugar for a single-entry Tags list. The reconciler folds it
	// into Tags via EffectiveTags() and rejects rules that set both Tag
	// and Tags simultaneously.
	//
	// Mutually exclusive with Tags. MaxLength=256 caps the field for the
	// CEL cost estimator; OCI tags are 128 chars max and tag templates
	// add a small placeholder budget, so 256 is comfortably wide.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Tag string `json:"tag,omitempty"`
}

// EffectiveTags returns the effective tag-template list for this target.
// The singular Tag is promoted into a single-entry list; an empty Target
// returns nil so callers can apply their own default ("{tag}" passthrough,
// per the design).
//
// Callers should call this only after policy-level validation has
// rejected the case where both Tag and Tags are set.
func (t Target) EffectiveTags() []string {
	if t.Tag != "" {
		return []string{t.Tag}
	}
	return t.Tags
}

// HasBothTagForms reports whether the target sets both Tag and Tags. The
// reconciler treats this as a validation error and the rule is skipped
// at admission time.
func (t Target) HasBothTagForms() bool {
	return t.Tag != "" && len(t.Tags) > 0
}
