package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"gitlab.com/gitlab-org/gitlab-runner/common"
	"gitlab.com/gitlab-org/gitlab-runner/executors/custom/api"
	"gitlab.com/gitlab-org/gitlab-runner/helpers/process"
)

const (
	BuildFailureExitCode  = 1
	SystemFailureExitCode = 2
)

type CreateOptions struct {
	Dir string
	Env []string

	Stdout io.Writer
	Stderr io.Writer

	Logger common.BuildLogger

	GracefulKillTimeout time.Duration
	ForceKillTimeout    time.Duration
}

type Command interface {
	Run() error
}

var newProcessKillWaiter = process.NewOSKillWait

type command struct {
	context context.Context
	cmd     commander

	waitCh chan error

	logger common.BuildLogger

	gracefulKillTimeout time.Duration
	forceKillTimeout    time.Duration
}

func New(ctx context.Context, executable string, args []string, options CreateOptions) Command {
	defaultVariables := map[string]string{
		"TMPDIR":                          options.Dir,
		api.BuildFailureExitCodeVariable:  strconv.Itoa(BuildFailureExitCode),
		api.SystemFailureExitCodeVariable: strconv.Itoa(SystemFailureExitCode),
	}

	env := os.Environ()
	for key, value := range defaultVariables {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}
	options.Env = append(env, options.Env...)

	return &command{
		context:             ctx,
		cmd:                 newCmd(executable, args, options),
		waitCh:              make(chan error),
		logger:              options.Logger,
		gracefulKillTimeout: options.GracefulKillTimeout,
		forceKillTimeout:    options.ForceKillTimeout,
	}
}

func (c *command) Run() error {
	err := c.cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	go c.waitForCommand()

	select {
	case err = <-c.waitCh:
		return err

	case <-c.context.Done():
		return newProcessKillWaiter(c.logger, c.gracefulKillTimeout, c.forceKillTimeout).
			KillAndWait(c.cmd.Process(), c.waitCh)
	}
}

var getExitCode = func(err *exec.ExitError) int {
	return err.ExitCode()
}

func (c *command) waitForCommand() {
	err := c.cmd.Wait()

	eerr, ok := err.(*exec.ExitError)
	if ok {
		exitCode := getExitCode(eerr)
		switch {
		case exitCode == BuildFailureExitCode:
			err = &common.BuildError{Inner: eerr}
		case exitCode != SystemFailureExitCode:
			err = &ErrUnknownFailure{Inner: eerr, ExitCode: exitCode}
		}
	}

	c.waitCh <- err
}
