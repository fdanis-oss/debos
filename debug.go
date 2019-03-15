package debos

import (
	"fmt"
	"log"
	"os"
)

/*
DebugShell function launches an interactive shell for
debug and problems investigation.
*/
func DebugShell(context DebosContext) {

	if len(context.Debos.DebugShell) == 0 {
		return
	}

	pa := os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Dir:   context.Debos.Scratchdir,
	}

	// Start an interactive shell for debug.
	log.Printf(">>> Starting a debug shell")
	if proc, err := os.StartProcess(context.Debos.DebugShell, []string{}, &pa); err != nil {
		fmt.Printf("Failed: %s\n", err)
	} else {
		proc.Wait()
	}
}
