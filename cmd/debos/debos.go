package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"strings"

	"github.com/docker/go-units"
	"github.com/go-debos/debos"
	"github.com/go-debos/debos/actions"
	"github.com/go-debos/fakemachine"
	"github.com/jessevdk/go-flags"
)

func checkError(context *debos.DebosContext, err error, a debos.Action, stage string) int {
	if err == nil {
		return 0
	}

	context.Debos.State = debos.Failed
	log.Printf("Action `%s` failed at stage %s, error: %s", a, stage, err)
	debos.DebugShell(*context)
	return 1
}

func do_run(r actions.Recipe, context *debos.DebosContext) int {
	for _, a := range r.Actions {
		err := a.Run(context)

		// This does not stop the call of stacked Cleanup methods for other Actions
		// Stack Cleanup methods
		defer a.Cleanup(context)

		// Check the state of Run method
		if exitcode := checkError(context, err, a, "Run"); exitcode != 0 {
			return exitcode
		}
	}

	return 0
}

func warnLocalhost(variable string, value string) {
	message := `WARNING: Environment variable %[1]s contains a reference to
		    localhost. This may not work when running from fakemachine.
		    Consider using an address that is valid on your network.`

	if strings.Contains(value, "localhost") ||
	   strings.Contains(value, "127.0.0.1") ||
	   strings.Contains(value, "::1") {
		log.Printf(message, variable)
	}
}


func main() {
	var common debos.CommonContext
	var context = debos.DebosContext { Debos: &common }
	var options struct {
		ArtifactDir   string            `long:"artifactdir" description:"Directory for packed archives and ostree repositories (default: current directory)"`
		InternalImage string            `long:"internal-image" hidden:"true"`
		TemplateVars  map[string]string `short:"t" long:"template-var" description:"Template variables (use -t VARIABLE:VALUE syntax)"`
		DebugShell    bool              `long:"debug-shell" description:"Fall into interactive shell on error"`
		Shell         string            `short:"s" long:"shell" description:"Redefine interactive shell binary (default: bash)" optionsl:"" default:"/bin/bash"`
		ScratchSize   string            `long:"scratchsize" description:"Size of disk backed scratch space"`
		CPUs          int               `short:"c" long:"cpus" description:"Number of CPUs to use for build VM (default: 2)"`
		Memory        string            `short:"m" long:"memory" description:"Amount of memory for build VM (default: 2048MB)"`
		ShowBoot      bool              `long:"show-boot" description:"Show boot/console messages from the fake machine"`
		EnvironVars   map[string]string `short:"e" long:"environ-var" description:"Environment variables (use -e VARIABLE:VALUE syntax)"`
		Verbose       bool              `short:"v" long:"verbose" description:"Verbose output"`
		PrintRecipe   bool              `long:"print-recipe" description:"Print final recipe"`
		DryRun        bool              `long:"dry-run" description:"Compose final recipe to build but without any real work started"`
	}

	// These are the environment variables that will be detected on the
	// host and propagated to fakemachine. These are listed lower case, but
	// they are detected and configured in both lower case and upper case.
	var environ_vars = [...]string {
		"http_proxy",
		"https_proxy",
		"ftp_proxy",
		"rsync_proxy",
		"all_proxy",
		"no_proxy",
	}

	var exitcode int = 0
	// Allow to run all deferred calls prior to os.Exit()
	defer func() {
		os.Exit(exitcode)
	}()

	parser := flags.NewParser(&options, flags.Default)
	args, err := parser.Parse()

	if err != nil {
		flagsErr, ok := err.(*flags.Error)
		if ok && flagsErr.Type == flags.ErrHelp {
			return
		} else {
			fmt.Printf("%v\n", flagsErr)
			exitcode = 1
			return
		}
	}

	if len(args) != 1 {
		log.Println("No recipe given!")
		exitcode = 1
		return
	}

	// Set interactive shell binary only if '--debug-shell' options passed
	if options.DebugShell {
		context.Debos.DebugShell = options.Shell
	}

	if options.PrintRecipe {
		context.Debos.PrintRecipe = options.PrintRecipe
	}

	if options.Verbose {
		context.Debos.Verbose = options.Verbose
	}

	file := args[0]
	file = debos.CleanPath(file)

	r := actions.Recipe{}
	if _, err := os.Stat(file); os.IsNotExist(err) {
		log.Println(err)
		exitcode = 1
		return
	}
	if err := r.Parse(file, options.PrintRecipe, options.Verbose, options.TemplateVars); err != nil {
		log.Println(err)
		exitcode = 1
		return
	}

	/* If fakemachine is supported the outer fake machine will never use the
	 * scratchdir, so just set it to /scratch as a dummy to prevent the
	 * outer debos creating a temporary direction */
	if fakemachine.InMachine() || fakemachine.Supported() {
		context.Debos.Scratchdir = "/scratch"
	} else {
		log.Printf("fakemachine not supported, running on the host!")
		cwd, _ := os.Getwd()
		context.Debos.Scratchdir, err = ioutil.TempDir(cwd, ".debos-")
		defer os.RemoveAll(context.Debos.Scratchdir)
	}

	context.Debos.Rootdir = path.Join(context.Debos.Scratchdir, "root")
	context.Debos.Image = options.InternalImage
	context.RecipeDir = path.Dir(file)

	context.Debos.Artifactdir = options.ArtifactDir
	if context.Debos.Artifactdir == "" {
		context.Debos.Artifactdir, _ = os.Getwd()
	}
	context.Debos.Artifactdir = debos.CleanPath(context.Debos.Artifactdir)

	// Initialise origins map
	context.Debos.Origins = make(map[string]string)
	context.Debos.Origins["artifacts"] = context.Debos.Artifactdir
	context.Debos.Origins["filesystem"] = context.Debos.Rootdir
	context.Debos.Origins["recipe"] = context.RecipeDir

	context.Architecture = r.Architecture

	context.Debos.State = debos.Success

	// Initialize environment variables map
	context.Debos.EnvironVars = make(map[string]string)

	// First add variables from host
	for _, e := range environ_vars {
		lowerVar := strings.ToLower(e) // lowercase not really needed
		lowerVal := os.Getenv(lowerVar)
		if lowerVal != "" {
			context.Debos.EnvironVars[lowerVar] = lowerVal
		}

		upperVar := strings.ToUpper(e)
		upperVal := os.Getenv(upperVar)
		if upperVal != "" {
			context.Debos.EnvironVars[upperVar] = upperVal
		}
	}

	// Then add/overwrite with variables from command line
	for k, v := range options.EnvironVars {
		// Allows the user to unset environ variables with -e
		if v == "" {
			delete(context.Debos.EnvironVars, k)
		} else {
			context.Debos.EnvironVars[k] = v
		}
	}

	for _, a := range r.Actions {
		err = a.Verify(&context)
		if exitcode = checkError(&context, err, a, "Verify"); exitcode != 0 {
			return
		}
	}

	if options.DryRun {
		log.Printf("==== Recipe done (Dry run) ====")
		return
	}

	if !fakemachine.InMachine() && fakemachine.Supported() {
		m := fakemachine.NewMachine()
		var args []string

		if options.Memory == "" {
			// Set default memory size for fakemachine
			options.Memory = "2Gb"
		}
		memsize, err := units.RAMInBytes(options.Memory)
		if err != nil {
			fmt.Printf("Couldn't parse memory size: %v\n", err)
			exitcode = 1
			return
		}
		m.SetMemory(int(memsize / 1024 / 1024))

		if options.CPUs == 0 {
			// Set default CPU count for fakemachine
			options.CPUs = 2
		}
		m.SetNumCPUs(options.CPUs)

		if options.ScratchSize != "" {
			size, err := units.FromHumanSize(options.ScratchSize)
			if err != nil {
				fmt.Printf("Couldn't parse scratch size: %v\n", err)
				exitcode = 1
				return
			}
			m.SetScratch(size, "")
		}

		m.SetShowBoot(options.ShowBoot)

		// Puts in a format that is compatible with output of os.Environ()
		if context.Debos.EnvironVars != nil {
			EnvironString := []string{}
			for k, v := range context.Debos.EnvironVars {
				warnLocalhost(k, v)
				EnvironString = append(EnvironString, fmt.Sprintf("%s=%s", k, v))
			}
			m.SetEnviron(EnvironString) // And save the resulting environ vars on m
		}

		m.AddVolume(context.Debos.Artifactdir)
		args = append(args, "--artifactdir", context.Debos.Artifactdir)

		for k, v := range options.TemplateVars {
			args = append(args, "--template-var", fmt.Sprintf("%s:\"%s\"", k, v))
		}

		for k, v := range options.EnvironVars {
			args = append(args, "--environ-var", fmt.Sprintf("%s:\"%s\"", k, v))
		}

		m.AddVolume(context.RecipeDir)
		args = append(args, file)

		if options.DebugShell {
			args = append(args, "--debug-shell")
			args = append(args, "--shell", fmt.Sprintf("%s", options.Shell))
		}

		for _, a := range r.Actions {
			// Stack PostMachineCleanup methods
			defer a.PostMachineCleanup(&context)

			err = a.PreMachine(&context, m, &args)
			if exitcode = checkError(&context, err, a, "PreMachine"); exitcode != 0 {
				return
			}
		}

		exitcode, err = m.RunInMachineWithArgs(args)
		if err != nil {
			fmt.Println(err)
			return
		}

		if exitcode != 0 {
			context.Debos.State = debos.Failed
			return
		}

		for _, a := range r.Actions {
			err = a.PostMachine(&context)
			if exitcode = checkError(&context, err, a, "Postmachine"); exitcode != 0 {
				return
			}
		}

		log.Printf("==== Recipe done ====")
		return
	}

	if !fakemachine.InMachine() {
		for _, a := range r.Actions {
			// Stack PostMachineCleanup methods
			defer a.PostMachineCleanup(&context)

			err = a.PreNoMachine(&context)
			if exitcode = checkError(&context, err, a, "PreNoMachine"); exitcode != 0 {
				return
			}
		}
	}

	// Create Rootdir
	if _, err = os.Stat(context.Debos.Rootdir); os.IsNotExist(err) {
		err = os.Mkdir(context.Debos.Rootdir, 0755)
		if err != nil && os.IsNotExist(err) {
			exitcode = 1
			return
		}
	}

	exitcode = do_run(r, &context)
	if exitcode != 0 {
		return
	}

	if !fakemachine.InMachine() {
		for _, a := range r.Actions {
			err = a.PostMachine(&context)
			if exitcode = checkError(&context, err, a, "PostMachine"); exitcode != 0 {
				return
			}
		}
		log.Printf("==== Recipe done ====")
	}
}
