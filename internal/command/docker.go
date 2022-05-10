package command

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fyne-io/fyne-cross/internal/log"
	"github.com/fyne-io/fyne-cross/internal/volume"

	"golang.org/x/sys/execabs"
)

// Options define the options for the docker run command
type Options struct {
	WorkDir string // WorkDir set the workdir, default to volume's workdir
}

type LocalContainerRunner struct {
	AllContainerRunner

	Engine *Engine

	pull         bool
	cacheEnabled bool
}

func NewLocalContainerRunner(context Context) (ContainerRunner, error) {
	return &LocalContainerRunner{
		AllContainerRunner: AllContainerRunner{
			Env:   context.Env,
			Tags:  context.Tags,
			vol:   context.Volume,
			debug: context.Debug,
		},
		Engine:       &context.Engine,
		pull:         context.Pull,
		cacheEnabled: context.CacheEnabled,
	}, nil
}

type LocalContainerImage struct {
	AllContainerImage

	Pull bool

	Runner *LocalContainerRunner
}

var _ io.Closer = (*LocalContainerImage)(nil)

func (r *LocalContainerRunner) NewImageContainer(arch Architecture, OS string, image string) ContainerImage {
	ret := r.newImageContainerInternal(arch, OS, image, func(arch Architecture, OS, ID, image string) ContainerImage {
		return &LocalContainerImage{
			AllContainerImage: AllContainerImage{
				Architecture: arch,
				OS:           OS,
				ID:           ID,
				DockerImage:  image,
				Env:          make(map[string]string),
			},
			Pull:   r.pull,
			Runner: r,
		}
	})

	// mount the cache dir if cache is enabled
	if r.cacheEnabled {
		ret.SetMount("cache", r.vol.CacheDirHost(), r.vol.CacheDirContainer())
	}

	return ret
}

func (*LocalContainerImage) Close() error {
	return nil
}

func AppendEnv(args []string, environs map[string]string, quoteNeeded bool) []string {
	for k, v := range environs {
		env := k + "=" + v
		if quoteNeeded && strings.Contains(v, "=") {
			// engine requires to double quote the env var when value contains
			// the `=` char
			env = fmt.Sprintf("%q", env)
		}
		args = append(args, "-e", env)
	}
	return args
}

func (i *LocalContainerImage) GetRunner() ContainerRunner {
	return i.Runner
}

// Cmd returns a command to run in a new container for the specified image
func (i *LocalContainerImage) Cmd(vol volume.Volume, opts Options, cmdArgs []string) *execabs.Cmd {

	// define workdir
	w := vol.WorkDirContainer()
	if opts.WorkDir != "" {
		w = opts.WorkDir
	}

	args := []string{
		"run", "--rm", "-t",
		"-w", w, // set workdir
	}

	for _, mountPoint := range i.Mount {
		args = append(args, "-v", fmt.Sprintf("%s:%s:z", mountPoint.LocalHost, mountPoint.InContainer))
	}

	// handle settings related to engine
	if i.Runner.Engine.IsPodman() {
		args = append(args, "--userns", "keep-id", "-e", "use_podman=1")
	} else {
		// docker: pass current user id to handle mount permissions on linux and MacOS
		if runtime.GOOS != "windows" {
			u, err := user.Current()
			if err == nil {
				args = append(args, "-u", fmt.Sprintf("%s:%s", u.Uid, u.Gid))
				args = append(args, "--entrypoint", "fixuid")
				if !i.Runner.debug {
					// silent fixuid if not debug mode
					cmdArgs = append([]string{"-q"}, cmdArgs...)
				}
			}
		}
	}

	// add default env variables
	args = append(args,
		"-e", "CGO_ENABLED=1", // enable CGO
		"-e", fmt.Sprintf("GOCACHE=%s", vol.GoCacheDirContainer()), // mount GOCACHE to reuse cache between builds
	)

	// add custom env variables
	args = AppendEnv(args, i.Runner.Env, i.Env["GOOS"] != freebsdOS)
	args = AppendEnv(args, i.Env, i.Env["GOOS"] != freebsdOS)

	// specify the image to use
	args = append(args, i.DockerImage)

	// add the command to execute
	args = append(args, cmdArgs...)

	cmd := execabs.Command(i.Runner.Engine.Binary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd
}

// Run runs a command in a new container for the specified image
func (i *LocalContainerImage) Run(vol volume.Volume, opts Options, cmdArgs []string) error {
	cmd := i.Cmd(vol, opts, cmdArgs)
	i.Runner.Debug(cmd)
	return cmd.Run()
}

// pullImage attempts to pull a newer version of the docker image
func (i *LocalContainerImage) Prepare() error {
	if !i.Pull {
		return nil
	}

	log.Infof("[i] Checking for a newer version of the docker image: %s", i.DockerImage)

	buf := bytes.Buffer{}

	// run the command inside the container
	cmd := execabs.Command(i.Runner.Engine.Binary, "pull", i.DockerImage)
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	i.Runner.Debug(cmd)

	err := cmd.Run()

	i.Runner.Debug(buf.String())

	if err != nil {
		return fmt.Errorf("could not pull the docker image: %v", err)
	}

	log.Infof("[✓] Image is up to date")
	return nil
}

func (i *LocalContainerImage) Finalize(packageName string) error {
	// move the dist package into the "dist" folder
	srcPath := volume.JoinPathHost(i.Runner.vol.TmpDirHost(), i.GetID(), packageName)
	distFile := volume.JoinPathHost(i.Runner.vol.DistDirHost(), i.GetID(), packageName)
	err := os.MkdirAll(filepath.Dir(distFile), 0755)
	if err != nil {
		return fmt.Errorf("could not create the dist package dir: %v", err)
	}

	err = os.Rename(srcPath, distFile)
	if err != nil {
		return err
	}

	log.Infof("[✓] Package: %q", distFile)

	return nil
}
