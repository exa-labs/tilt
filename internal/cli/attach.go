package cli

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/tilt-dev/tilt/internal/analytics"
	engineanalytics "github.com/tilt-dev/tilt/internal/engine/analytics"
	"github.com/tilt-dev/tilt/internal/hud/prompt"
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/pkg/logger"
	"github.com/tilt-dev/tilt/pkg/model"
)

type attachCmd struct {
	fileName             string
	outputSnapshotOnExit string

	legacy bool
	stream bool
}

func (c *attachCmd) name() model.TiltSubcommand { return "attach" }

func (c *attachCmd) register() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "attach [<tilt flags>] [-- <Tiltfile args>]",
		DisableFlagsInUseLine: true,
		Short:                 "Attach to existing resources without rebuilding",
		Long: `
Starts Tilt and attaches to any resources that already exist on remote
without verifying the image hash. Resources that are missing are built
and deployed normally (identical to tilt up).

Accepts the same flags and Tiltfile args as tilt up.
`,
	}

	cmd.Flags().BoolVar(&c.legacy, "legacy", false, "If true, tilt will open in legacy terminal mode.")
	cmd.Flags().BoolVar(&c.stream, "stream", false, "If true, tilt will stream logs in the terminal.")
	cmd.Flags().BoolVar(&logActionsFlag, "logactions", false, "log all actions and state changes")
	addStartServerFlags(cmd)
	addDevServerFlags(cmd)
	addTiltfileFlag(cmd, &c.fileName)
	addKubeContextFlag(cmd)
	addNamespaceFlag(cmd)
	addLogFilterFlags(cmd, "log-")
	addLogFilterResourcesFlag(cmd)
	cmd.Flags().Lookup("logactions").Hidden = true
	cmd.Flags().StringVar(&c.outputSnapshotOnExit, "output-snapshot-on-exit", "", "If specified, Tilt will dump a snapshot of its state to the specified path when it exits")

	return cmd
}

func (c *attachCmd) initialTermMode(isTerminal bool) store.TerminalMode {
	if !isTerminal {
		return store.TerminalModeStream
	}
	if c.legacy {
		return store.TerminalModeHUD
	}
	if c.stream {
		return store.TerminalModeStream
	}
	return store.TerminalModePrompt
}

func (c *attachCmd) run(ctx context.Context, args []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	a := analytics.Get(ctx)
	defer a.Flush(time.Second)

	log.SetFlags(log.Flags() &^ (log.Ldate | log.Ltime))
	isTTY := isatty.IsTerminal(os.Stdout.Fd())
	termMode := c.initialTermMode(isTTY)

	cmdTags := engineanalytics.CmdTags(map[string]string{
		"term_mode": strconv.Itoa(int(termMode)),
	})
	a.Incr("cmd.attach", cmdTags.AsMap())

	deferred := logger.NewDeferredLogger(ctx)
	ctx = redirectLogs(ctx, deferred)

	webHost := provideWebHost()
	webURL, _ := provideWebURL(webHost, provideWebPort())
	startLine := prompt.StartStatusLine(webURL, webHost)
	log.Print(startLine)
	log.Print(buildStamp())

	if ok, reason := analytics.IsAnalyticsDisabledFromEnv(); ok {
		log.Printf("Tilt analytics disabled: %s", reason)
	}

	cmdUpDeps, err := wireCmdUp(ctx, a, cmdTags, "attach")
	if err != nil {
		deferred.SetOutput(deferred.Original())
		return err
	}

	upper := cmdUpDeps.Upper
	if termMode == store.TerminalModePrompt {
		cmdUpDeps.Prompt.SetInitOutput(deferred.CopyBuffered(logger.InfoLvl))
	}

	l := store.NewLogActionLogger(ctx, upper.Dispatch)
	deferred.SetOutput(l)
	ctx = redirectLogs(ctx, l)
	if c.outputSnapshotOnExit != "" {
		defer cmdUpDeps.Snapshotter.WriteSnapshot(ctx, c.outputSnapshotOnExit)
	}

	err = upper.Start(ctx, args, cmdUpDeps.TiltBuild,
		c.fileName, termMode, a.UserOpt(), cmdUpDeps.Token, string(cmdUpDeps.CloudAddress), true)
	if err != context.Canceled {
		return err
	}
	return nil
}
