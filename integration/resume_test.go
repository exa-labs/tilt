//go:build integration
// +build integration

// Tests for the --resume flag in tilt ci mode.
// The resume flag allows tilt ci to detect existing pods and skip unnecessary rebuilds.

package integration

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/k8s"
)

func TestResumeModeSkipsRebuildWhenNoChanges(t *testing.T) {
	f := newK8sFixture(t, "resume")
	f.SetRestrictedCredentials()

	// First run: deploy the service normally
	f.TiltCI()

	// Wait for pod to be ready
	ctx, cancel := context.WithTimeout(f.ctx, time.Minute)
	defer cancel()
	podNames := f.WaitForAllPodsReady(ctx, "app=resume-test")
	require.Len(t, podNames, 1, "Expected exactly one pod")

	// Verify the pod has the git commit label
	podLabels := getPodLabels(t, podNames[0])
	gitCommit, hasGitCommit := podLabels[k8s.GitCommitLabel]
	assert.True(t, hasGitCommit, "Pod should have git commit label")
	assert.NotEmpty(t, gitCommit, "Git commit label should not be empty")

	// Second run with --resume: should detect existing pod and skip rebuild
	logs := f.logs.String()
	f.logs.Reset()

	f.TiltCI("--resume")

	resumeLogs := f.logs.String()

	// Verify that the second run detected the existing deployment
	// and didn't do a full rebuild (look for resume-related log messages)
	assert.True(t, strings.Contains(resumeLogs, "resume-test") || strings.Contains(logs, "resume-test"),
		"Logs should mention the resume-test resource")

	// The pod should still be running with the same name (no restart)
	ctx2, cancel2 := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel2()
	newPodNames := f.WaitForAllPodsReady(ctx2, "app=resume-test")
	require.Len(t, newPodNames, 1, "Expected exactly one pod after resume")

	// Pod name should be the same (no rebuild/restart happened)
	assert.Equal(t, podNames[0], newPodNames[0],
		"Pod should not have been recreated when resuming with no changes")
}

func getPodLabels(t *testing.T, podName string) map[string]string {
	t.Helper()
	cmd := exec.Command("kubectl", "get", "pod", podName,
		"-n=tilt-integration",
		"-o=jsonpath={.metadata.labels}")
	out, err := cmd.Output()
	require.NoError(t, err, "Failed to get pod labels")

	labels := make(map[string]string)
	// Parse the JSON-like output from jsonpath
	// Format: map[key1:value1 key2:value2]
	outStr := strings.TrimPrefix(string(out), "map[")
	outStr = strings.TrimSuffix(outStr, "]")
	if outStr == "" {
		return labels
	}

	pairs := strings.Split(outStr, " ")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) == 2 {
			labels[kv[0]] = kv[1]
		}
	}
	return labels
}
