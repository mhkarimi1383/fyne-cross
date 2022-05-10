package command

import (
	"fmt"
	"os"
	"path"

	"github.com/fyne-io/fyne-cross/internal/log"
	"github.com/fyne-io/fyne-cross/internal/volume"
)

const (
	// androidOS is the android OS name
	androidOS = "android"
	// androidImage is the fyne-cross image for the Android OS
	androidImage = "docker.io/fyneio/fyne-cross:1.2-android"
)

var (
	// androidArchSupported defines the supported target architectures for the android OS
	androidArchSupported = []Architecture{ArchMultiple, ArchAmd64, Arch386, ArchArm, ArchArm64}
)

// Android build and package the fyne app for the android OS
type Android struct {
	CrossBuilderCommand
	CrossBuilder
}

func NewAndroidCommand() *Android {
	return &Android{CrossBuilder: CrossBuilder{name: "android", description: "Build and package a fyne application for the android OS"}}
}

// Parse parses the arguments and set the usage for the command
func (cmd *Android) Parse(args []string) error {
	commonFlags, err := newCommonFlags()
	if err != nil {
		return err
	}

	flags := &androidFlags{
		CommonFlags: commonFlags,
		TargetArch:  &targetArchFlag{string(ArchMultiple)},
	}

	flagSet.Var(flags.TargetArch, "arch", fmt.Sprintf(`List of target architecture to build separated by comma. Supported arch: %s.`, androidArchSupported))
	flagSet.StringVar(&flags.Keystore, "keystore", "", "The location of .keystore file containing signing information")
	flagSet.StringVar(&flags.KeystorePass, "keystore-pass", "", "Password for the .keystore file")
	flagSet.StringVar(&flags.KeyPass, "key-pass", "", "Password for the signer's private key, which is needed if the private key is password-protected")

	flagSet.Usage = cmd.Usage
	flagSet.Parse(args)

	err = cmd.makeAndroidContainerImages(flags, flagSet.Args())
	return err
}

// Run runs the command using helper code
func (cmd *Android) Run() error {
	return cmd.RunInternal(cmd)
}

// Run runs the command
func (cmd *Android) RunEach(image ContainerImage) (string, error) {
	//
	// package
	//
	log.Info("[i] Packaging app...")

	packageName := fmt.Sprintf("%s.apk", cmd.defaultContext.Name)

	err := prepareIcon(cmd.defaultContext, image)
	if err != nil {
		return "", err
	}

	if cmd.defaultContext.Release {
		err = fyneRelease(cmd.defaultContext, image)
	} else {
		err = fynePackage(cmd.defaultContext, image)
	}
	if err != nil {
		return "", fmt.Errorf("could not package the Fyne app: %v", err)
	}

	// move the dist package into the "dist" folder
	// The fyne tool sanitizes the package name to be acceptable as a
	// android package name. For details, see:
	// https://github.com/fyne-io/fyne/blob/v1.4.0/cmd/fyne/internal/mobile/build_androidapp.go#L297
	// To avoid to duplicate the fyne tool sanitize logic here, the location of
	// the dist package to move will be detected using a matching pattern
	command := fmt.Sprintf("mv %s %q",
		volume.JoinPathContainer(cmd.defaultContext.WorkDirContainer(), cmd.defaultContext.Package, "*.apk"),
		volume.JoinPathContainer(cmd.defaultContext.TmpDirContainer(), image.GetID(), packageName),
	)

	// move the dist package into the expected tmp/$ID/packageName location in the container
	// We use the shell to do the globbing and copy the file
	err = image.Run(cmd.defaultContext.Volume, Options{}, []string{
		"sh", "-c", command,
	})

	if err != nil {
		return "", fmt.Errorf("could not retrieve the packaged apk")
	}

	return packageName, nil
}

// Usage displays the command usage
func (cmd *Android) Usage() {
	data := struct {
		Name        string
		Description string
	}{
		Name:        cmd.Name(),
		Description: cmd.Description(),
	}

	template := `
Usage: fyne-cross {{ .Name }} [options] [package]

{{ .Description }}

Options:
`

	printUsage(template, data)
	flagSet.PrintDefaults()
}

// androidFlags defines the command-line flags for the android command
type androidFlags struct {
	*CommonFlags

	Keystore     string //Keystore represents the location of .keystore file containing signing information
	KeystorePass string //Password for the .keystore file
	KeyPass      string //Password for the signer's private key, which is needed if the private key is password-protected

	// TargetArch represents a list of target architecture to build on separated by comma
	TargetArch *targetArchFlag
}

// makeAndroidContext returns the command context for an android target
func (cmd *Android) makeAndroidContainerImages(flags *androidFlags, args []string) error {

	targetArch, err := targetArchFromFlag(*flags.TargetArch, androidArchSupported)
	if err != nil {
		return fmt.Errorf("could not make build context for %s OS: %s", androidOS, err)
	}

	ctx, err := makeDefaultContext(flags.CommonFlags, args)
	if err != nil {
		return err
	}

	// appID is mandatory for android
	if ctx.AppID == "" {
		return fmt.Errorf("appID is mandatory for %s", androidOS)
	}

	cmd.defaultContext = ctx
	runner, err := NewContainerRunner(ctx)
	if err != nil {
		return err
	}

	for _, arch := range targetArch {
		// By default, the fyne cli tool builds a fat APK for all supported
		// instruction sets (arm, 386, amd64, arm64). A subset of instruction sets can
		// be selected by specifying target type with the architecture name.
		// E.g.: -os=android/arm
		image := runner.NewImageContainer(arch, androidOS, overrideDockerImage(flags.CommonFlags, androidImage))

		if path.IsAbs(flags.Keystore) {
			return fmt.Errorf("keystore location must be relative to the project root: %s", ctx.Volume.WorkDirHost())
		}

		if !ctx.NoProjectUpload {
			if _, err := os.Stat(volume.JoinPathHost(ctx.Volume.WorkDirHost(), flags.Keystore)); err != nil {
				return fmt.Errorf("keystore location must be under the project root: %s", ctx.Volume.WorkDirHost())
			}
		}

		cmd.defaultContext.Keystore = volume.JoinPathContainer(cmd.defaultContext.Volume.WorkDirContainer(), flags.Keystore)
		cmd.defaultContext.KeystorePass = flags.KeystorePass
		cmd.defaultContext.KeyPass = flags.KeyPass

		cmd.Images = append(cmd.Images, image)
	}

	return nil
}
