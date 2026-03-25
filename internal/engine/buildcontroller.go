package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tilt-dev/tilt/internal/timecmp"

	"github.com/pkg/errors"

	"github.com/tilt-dev/tilt/internal/controllers/apis/uibutton"
	"github.com/tilt-dev/tilt/internal/engine/buildcontrol"
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/internal/store/buildcontrols"
	"github.com/tilt-dev/tilt/internal/store/k8sconv"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
	"github.com/tilt-dev/tilt/pkg/model/logstore"
)

const BuildControlSource = "buildcontrol"

type BuildController struct {
	b                  buildcontrol.BuildAndDeployer
	buildsStartedCount int // used to synchronize with state
	disabledForTesting bool

	// CancelFuncs for in-progress builds
	mu           sync.Mutex
	stopBuildFns map[model.ManifestName]context.CancelFunc
}

type buildEntry struct {
	name          model.ManifestName
	targets       []model.TargetSpec
	buildStateSet store.BuildStateSet
	filesChanged  []string
	buildReason   model.BuildReason
	spanID        logstore.SpanID
}

func (e buildEntry) Name() model.ManifestName       { return e.name }
func (e buildEntry) FilesChanged() []string         { return e.filesChanged }
func (e buildEntry) BuildReason() model.BuildReason { return e.buildReason }

func NewBuildController(b buildcontrol.BuildAndDeployer) *BuildController {
	return &BuildController{
		b:            b,
		stopBuildFns: make(map[model.ManifestName]context.CancelFunc),
	}
}

func (c *BuildController) needsBuilds(ctx context.Context, st store.RStore) []buildEntry {
	state := st.RLockState()
	defer st.RUnlockState()

	// Don't start any new builds until all previous actions have been recorded,
	// so that we don't accidentally repeat the same build.
	if c.buildsStartedCount > state.BuildControllerStartCount {
		return nil
	}

	var entries []buildEntry
	exclude := make(map[model.ManifestName]bool)

	for {
		if state.AvailableBuildSlots()-len(entries) < 1 {
			break
		}

		mt, _ := buildcontrol.NextTargetToBuildExcluding(state, exclude)
		if mt == nil {
			break
		}

		exclude[mt.Manifest.Name] = true
		c.buildsStartedCount += 1
		ms := mt.State
		manifest := mt.Manifest

		buildReason := mt.NextBuildReason()
		targets := buildcontrol.BuildTargets(manifest)
		bss := buildStateSet(ctx,
			manifest,
			state.KubernetesResources[manifest.Name.String()],
			state.DockerComposeServices[manifest.Name.String()],
			state.Clusters[manifest.ClusterName()],
			targets,
			ms,
			buildReason)

		entries = append(entries, buildEntry{
			name:          manifest.Name,
			targets:       targets,
			buildReason:   buildReason,
			buildStateSet: bss,
			filesChanged:  append(ms.ConfigFilesThatCausedChange, bss.FilesChanged()...),
			spanID:        SpanIDForBuildLog(c.buildsStartedCount),
		})
	}

	return entries
}

func (c *BuildController) DisableForTesting() {
	c.disabledForTesting = true
}

func (c *BuildController) OnChange(ctx context.Context, st store.RStore, summary store.ChangeSummary) error {
	if summary.IsLogOnly() {
		return nil
	}

	c.cleanUpCanceledBuilds(st)

	if c.disabledForTesting {
		return nil
	}

	entries := c.needsBuilds(ctx, st)
	for _, entry := range entries {
		st.Dispatch(buildcontrols.BuildStartedAction{
			ManifestName:       entry.name,
			StartTime:          time.Now(),
			FilesChanged:       entry.filesChanged,
			Reason:             entry.buildReason,
			SpanID:             entry.spanID,
			FullBuildTriggered: entry.buildStateSet.FullBuildTriggered(),
			Source:             BuildControlSource,
		})

		go func(entry buildEntry) {
			ctx = c.buildContext(ctx, entry, st)
			defer c.cleanupBuildContext(entry.name)

			buildcontrols.LogBuildEntry(ctx, buildcontrols.BuildEntry{
				Name:         entry.Name(),
				BuildReason:  entry.BuildReason(),
				FilesChanged: entry.FilesChanged(),
			})

			result, err := c.buildAndDeploy(ctx, st, entry)
			if ctx.Err() == context.Canceled {
				err = errors.New("build canceled")
			}
			st.Dispatch(buildcontrols.NewBuildCompleteAction(entry.name, BuildControlSource, entry.spanID, result, err))
		}(entry)
	}

	return nil
}

func (c *BuildController) buildAndDeploy(ctx context.Context, st store.RStore, entry buildEntry) (store.BuildResultSet, error) {
	targets := entry.targets
	for _, target := range targets {
		err := target.Validate()
		if err != nil {
			return store.BuildResultSet{}, err
		}
	}
	return c.b.BuildAndDeploy(ctx, st, targets, entry.buildStateSet)
}

// cancel any in-progress builds associated with canceled builds and disabled UIResources
// when builds are fully represented by api objects, cancellation should probably
// be tied to those rather than the UIResource
func (c *BuildController) cleanUpCanceledBuilds(st store.RStore) {
	state := st.RLockState()
	defer st.RUnlockState()

	for _, ms := range state.ManifestStates() {
		if !ms.IsBuilding() {
			continue
		}
		disabled := ms.DisableState == v1alpha1.DisableStateDisabled
		canceled := false
		if cancelButton, ok := state.UIButtons[uibutton.StopBuildButtonName(ms.Name.String())]; ok {
			lastCancelClick := cancelButton.Status.LastClickedAt
			canceled = timecmp.AfterOrEqual(lastCancelClick, ms.EarliestCurrentBuild().StartTime)
		}
		if disabled || canceled {
			c.cleanupBuildContext(ms.Name)
		}
	}
}

func (c *BuildController) buildContext(ctx context.Context, entry buildEntry, st store.RStore) context.Context {
	// Send the logs to both the EngineState and the normal log stream.
	ctx = store.WithManifestLogHandler(ctx, st, entry.name, entry.spanID)

	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopBuildFns[entry.name] = cancel
	return ctx
}

func (c *BuildController) cleanupBuildContext(mn model.ManifestName) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cancel, ok := c.stopBuildFns[mn]; ok {
		cancel()
		delete(c.stopBuildFns, mn)
	}
}

func SpanIDForBuildLog(buildCount int) logstore.SpanID {
	return logstore.SpanID(fmt.Sprintf("build:%d", buildCount))
}

// Extract a set of build states from a manifest for BuildAndDeploy.
func buildStateSet(ctx context.Context, manifest model.Manifest,
	kresource *k8sconv.KubernetesResource,
	dcs *v1alpha1.DockerComposeService,
	cluster *v1alpha1.Cluster,
	specs []model.TargetSpec,
	ms *store.ManifestState, reason model.BuildReason) store.BuildStateSet {
	result := store.BuildStateSet{}

	for _, spec := range specs {
		id := spec.ID()
		status, ok := ms.BuildStatus(id)
		if !ok {
			continue
		}

		filesChanged := status.PendingFileChangesSorted()

		var depsChanged []model.TargetID
		for dep := range status.PendingDependencyChanges() {
			depsChanged = append(depsChanged, dep)
		}

		state := store.NewBuildState(status.LastResult, filesChanged, depsChanged)
		state.Cluster = cluster
		result[id] = state
	}

	isFullBuildTrigger := reason.HasTrigger() && !buildcontrol.IsLiveUpdateEligibleTrigger(manifest, reason)
	if isFullBuildTrigger {
		for k, v := range result {
			result[k] = v.WithFullBuildTriggered(true)
		}
	}

	return result
}

var _ store.Subscriber = &BuildController{}
