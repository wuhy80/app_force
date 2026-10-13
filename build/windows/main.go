// Windows executable resources, generated with go run ./build/windows.
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/tc-hib/winres"
)

func main() {
	resources := winres.ResourceSet{}
	icon, err := appIcon()
	if err != nil {
		log.Fatal(err)
	}
	if err := resources.SetIcon(winres.ID(1), icon); err != nil {
		log.Fatal(err)
	}
	resources.SetManifest(winres.AppManifest{
		Identity:       winres.AssemblyIdentity{Name: "AppForce", Version: [4]uint16{0, 1, 0, 0}},
		Description:    "AppForce application proxy",
		Compatibility:  winres.Win10AndAbove,
		ExecutionLevel: winres.RequireAdministrator,
		DPIAwareness:   winres.DPIPerMonitorV2,
		LongPathAware:  true,
	})
	for _, arch := range []winres.Arch{winres.ArchAMD64, winres.ArchARM64} {
		name := filepath.Join("cmd", "appforce", "rsrc_windows_"+string(arch)+".syso")
		file, err := os.Create(name)
		if err != nil {
			log.Fatal(err)
		}
		err = resources.WriteObject(file, arch)
		closeErr := file.Close()
		if err != nil {
			log.Fatal(err)
		}
		if closeErr != nil {
			log.Fatal(closeErr)
		}
	}
}
