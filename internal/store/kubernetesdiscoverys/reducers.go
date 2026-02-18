package kubernetesdiscoverys

import (
	"time"

	v1 "k8s.io/api/core/v1"

	"github.com/tilt-dev/tilt/internal/controllers/apicmp"
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/internal/store/k8sconv"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

func HandleKubernetesDiscoveryUpsertAction(state *store.EngineState, action KubernetesDiscoveryUpsertAction) {
	n := action.KubernetesDiscovery.Name
	oldState := state.KubernetesDiscoverys[n]
	state.KubernetesDiscoverys[n] = action.KubernetesDiscovery

	// We only refresh when the K8sDiscovery is changed.
	//
	// This is really only needed for tests - we have tests that wait until we've
	// reached a steady state, then change some fields on EngineState.
	//
	// K8s controllers assume everything is idempotent, and will wipe out our changes
	// later with duplicate events.
	isChanged := oldState == nil ||
		!apicmp.DeepEqual(oldState.Status, action.KubernetesDiscovery.Status) ||
		!apicmp.DeepEqual(oldState.Spec, action.KubernetesDiscovery.Spec)
	if isChanged {
		RefreshKubernetesResource(state, n)
	}
}

func HandleKubernetesDiscoveryDeleteAction(state *store.EngineState, action KubernetesDiscoveryDeleteAction) {
	oldState := state.KubernetesDiscoverys[action.Name]
	delete(state.KubernetesDiscoverys, action.Name)

	isChanged := oldState != nil
	if isChanged {
		RefreshKubernetesResource(state, action.Name)
	}
}

func filterForResource(state *store.EngineState, name string) (*k8sconv.KubernetesApplyFilter, error) {
	a := state.KubernetesApplys[name]
	if a == nil {
		return nil, nil
	}

	// if the yaml matches the existing resource, use its filter to save re-parsing
	// (https://github.com/tilt-dev/tilt/issues/5837)
	if prevResource, ok := state.KubernetesResources[name]; ok {
		if prevResource.ApplyStatus != nil && a.Status.ResultYAML == prevResource.ApplyStatus.ResultYAML {
			return prevResource.ApplyFilter, nil
		}
	}

	return k8sconv.NewKubernetesApplyFilter(a.Status.ResultYAML)
}

func RefreshKubernetesResource(state *store.EngineState, name string) {
	var aStatus *v1alpha1.KubernetesApplyStatus
	a := state.KubernetesApplys[name]
	if a != nil {
		aStatus = &(a.Status)
	}

	d := state.KubernetesDiscoverys[name]
	filter, err := filterForResource(state, name)
	if err != nil {
		return
	}
	r := k8sconv.NewKubernetesResourceWithFilter(d, aStatus, filter)
	state.KubernetesResources[name] = r

	if a != nil {
		mn := model.ManifestName(a.Annotations[v1alpha1.AnnotationManifest])
		ms, ok := state.ManifestState(mn)
		if ok {
			krs := ms.K8sRuntimeState()

			if d == nil {
				krs.FilteredPods = nil
				ms.RuntimeState = krs
				return
			}

			krs.FilteredPods = r.FilteredPods
			krs.Conditions = r.ApplyStatus.Conditions

			if krs.RuntimeStatus() == v1alpha1.RuntimeStatusOK {
				krs.LastReadyOrSucceededTime = time.Now()
			}

			ms.RuntimeState = krs

			maybeInjectAttachBuild(state, ms, r)
		}
	}
}

// maybeInjectAttachBuild injects a synthetic build record when running in
// attach mode. This makes tilt treat already-running pods as "already deployed"
// so it skips the initial image build and kubectl apply. Resources without
// running pods fall through to the normal build path.
func maybeInjectAttachBuild(state *store.EngineState, ms *store.ManifestState, r *k8sconv.KubernetesResource) {
	if !state.AttachMode {
		return
	}
	if ms.StartedFirstBuild() {
		return
	}
	if !hasRunningPod(r.FilteredPods) {
		return
	}

	now := time.Now()
	ms.AddCompletedBuild(model.BuildRecord{
		StartTime:  now,
		FinishTime: now,
		Reason:     model.BuildReasonFlagInit,
	})
}

func hasRunningPod(pods []v1alpha1.Pod) bool {
	for i := range pods {
		if pods[i].Phase == string(v1.PodRunning) {
			return true
		}
	}
	return false
}
