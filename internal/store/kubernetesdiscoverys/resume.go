package kubernetesdiscoverys

import (
	"context"
	"path/filepath"
	"time"

	"github.com/tilt-dev/tilt/internal/git"
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/pkg/model"
)

// ResumeGitDiffComputedAction carries the result of an asynchronous git diff
// computation performed outside the store lock.
type ResumeGitDiffComputedAction struct {
	ManifestName model.ManifestName
	DiffFiles    []string
}

func (ResumeGitDiffComputedAction) Action() {}

// HandleResumeGitDiffComputedAction injects diff files as pending file changes
// for the image targets of the resumed manifest. Files are added to all image
// targets so the build controller can determine which targets need rebuilds.
func HandleResumeGitDiffComputedAction(state *store.EngineState, action ResumeGitDiffComputedAction) {
	delete(state.ResumeDiffPending, action.ManifestName)
	if len(action.DiffFiles) == 0 {
		return
	}

	ms, ok := state.ManifestState(action.ManifestName)
	if !ok {
		return
	}

	mt, ok := state.ManifestTargets[action.ManifestName]
	if !ok {
		return
	}

	now := time.Now()
	imageTargets := mt.Manifest.ImageTargets
	if len(imageTargets) > 0 {
		for _, iTarget := range imageTargets {
			targetID := iTarget.ID()
			for _, file := range action.DiffFiles {
				absPath, err := filepath.Abs(file)
				if err != nil {
					continue
				}
				ms.AddPendingFileChange(targetID, absPath, now)
			}
		}
	} else {
		k8sTarget := mt.Manifest.K8sTarget()
		if !k8sTarget.ID().Empty() {
			for _, file := range action.DiffFiles {
				absPath, err := filepath.Abs(file)
				if err != nil {
					continue
				}
				ms.AddPendingFileChange(k8sTarget.ID(), absPath, now)
			}
		}
	}

}

// ResumeGitDiffSubscriber watches for manifests needing git diff computation
// and performs the (potentially slow) git operations outside the store lock.
type ResumeGitDiffSubscriber struct {
	computed map[model.ManifestName]bool
}

func NewResumeGitDiffSubscriber() *ResumeGitDiffSubscriber {
	return &ResumeGitDiffSubscriber{
		computed: make(map[model.ManifestName]bool),
	}
}

func (s *ResumeGitDiffSubscriber) OnChange(ctx context.Context, st store.RStore, summary store.ChangeSummary) error {
	state := st.RLockState()

	if !state.ResumeMode || state.GitCommit == "" {
		st.RUnlockState()
		return nil
	}

	gitCommit := state.GitCommit

	type diffRequest struct {
		mn           model.ManifestName
		podGitCommit string
	}
	var requests []diffRequest

	for mn, podCommit := range state.ResumeDiffPending {
		if !s.computed[mn] && podCommit != "" {
			requests = append(requests, diffRequest{mn: mn, podGitCommit: podCommit})
		}
	}

	st.RUnlockState()

	for _, req := range requests {
		s.computed[req.mn] = true

		diffFiles, err := git.GetDiffFiles(".", req.podGitCommit, gitCommit)
		if err != nil || len(diffFiles) == 0 {
			st.Dispatch(ResumeGitDiffComputedAction{ManifestName: req.mn})
			continue
		}

		st.Dispatch(ResumeGitDiffComputedAction{
			ManifestName: req.mn,
			DiffFiles:    diffFiles,
		})
	}

	return nil
}
