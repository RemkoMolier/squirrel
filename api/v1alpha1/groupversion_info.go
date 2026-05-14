package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the group/version used by these API types.
var GroupVersion = schema.GroupVersion{
	Group:   "squirrel.molier.dev",
	Version: "v1alpha1",
}

// SchemeBuilder collects the runtime.Object types in this package so a
// controller-runtime manager can register them with its scheme.
//
// The api package depends only on k8s.io/apimachinery (per the upstream
// recommendation against the deprecated
// sigs.k8s.io/controller-runtime/pkg/scheme.Builder) so that consumers
// of the types do not transitively pull in controller-runtime.
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme adds the types in this package to the given scheme. It is
// used by the manager's main wiring (see cmd/manager).
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&ClusterImagePolicy{}, &ClusterImagePolicyList{},
		&ImagePolicy{}, &ImagePolicyList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
