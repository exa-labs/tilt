package dockerprune

import (
	"go.starlark.net/starlark"

	"github.com/tilt-dev/tilt/internal/tiltfile/starkit"
)

// No-op plugin that accepts docker_prune_settings() calls for backwards
// compatibility but does nothing. The Docker Pruner has been removed.
type Plugin struct{}

func NewPlugin() Plugin {
	return Plugin{}
}

func (e Plugin) OnStart(env *starkit.Environment) error {
	return env.AddBuiltin("docker_prune_settings", e.dockerPruneSettings)
}

func (e Plugin) dockerPruneSettings(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Accept all the old keyword arguments but ignore them.
	var disable bool
	var keepRecent starlark.Value
	var intervalHrs, numBuilds, maxAgeMins int
	if err := starkit.UnpackArgs(thread, fn.Name(), args, kwargs,
		"disable?", &disable,
		"max_age_mins?", &maxAgeMins,
		"num_builds?", &numBuilds,
		"interval_hrs?", &intervalHrs,
		"keep_recent?", &keepRecent); err != nil {
		return nil, err
	}
	return starlark.None, nil
}
