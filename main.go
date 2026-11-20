package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/pprof"

	"github.com/sinanislekdemir/raylar/raytracer"
)

var buildTime string

func main() {
	s := raytracer.Scene{}

	sceneFile := ""

	configFile := flag.String("config", "", "Scene Config JSON")
	outputFilename := flag.String("output", "awesome.png", "Render output image filename")
	environmentMap := flag.String("environment", "", "Environment map image file for infinite reflections")
	percent := flag.Int("percent", 100, "Render completion percentage")
	size := flag.String("size", "", "width x height: Eg: 1600x900")
	left := flag.Int("left", 0, "Left X")
	right := flag.Int("right", 0, "Right X")
	top := flag.Int("top", 0, "Top")
	bottom := flag.Int("bottom", 0, "Bottom")
	profiling := flag.Bool("profile", false, "Set 1 for debugging")
	showHelp := flag.Bool("help", false, "Show help!")
	createConfig := flag.Bool("createconfig", false, "Create config")
	dof := flag.Bool("dof", false, "Enable depth of field (thin lens camera)")
	aperture := flag.Float64("aperture", 0, "Lens aperture radius in world units; 0 keeps the pinhole camera")
	focalDistance := flag.Float64("focal-distance", 0, "Distance from the camera to the focus plane")
	dofSamples := flag.Int("dof-samples", 0, "Rays per pixel for depth of field (1-1024)")
	dofSeed := flag.Int64("dof-seed", 0, "Fixed seed for reproducible lens sampling")

	flag.Parse()

	if flag.NArg() == 0 {
		sceneFile = "scene.json"
	} else {
		sceneFile = flag.Arg(0)
		if sceneFile == "" {
			sceneFile = "scene.json"
		}
	}

	if showHelp != nil && *showHelp {
		fmt.Println("--config <config.json>  : Render configurations")
		fmt.Println("--output <out.png>      : Output image filename")
		fmt.Println("--percent <percent>     : Render Percentage")
		fmt.Println("--profile               : Turn on profiling for golang")
		fmt.Println("--size <width>x<height> : Set width x height explicitly, overwriting config. 1600x900 eg.")
		fmt.Println("--createconfig          : Create a default config.json to modify scene parameters")
		fmt.Println("--environment           : Environment map image file for infinite reflections")
		fmt.Println("--dof                   : Enable depth of field (thin lens camera)")
		fmt.Println("--aperture <radius>     : Lens aperture radius in world units; 0 is the pinhole camera")
		fmt.Println("--focal-distance <dist> : Distance from the camera to the focus plane")
		fmt.Println("--dof-samples <n>       : Rays per pixel for depth of field (1-1024)")
		fmt.Println("--dof-seed <seed>       : Fixed seed for reproducible lens sampling")
		os.Exit(0)
	}

	if outputFilename == nil {
		s.OutputFilename = "out.png"
	} else {
		s.OutputFilename = *outputFilename
	}

	if *profiling {
		fx, _ := os.Create("profiling.prof")
		_ = pprof.StartCPUProfile(fx)
		defer pprof.StopCPUProfile()
	}

	fmt.Printf("Raylar - Build %s", buildTime)
	if *createConfig {
		err := raytracer.CreateConfig("config.json")
		if err != nil {
			fmt.Println(err.Error())
		}
		return
	}

	if _, err := os.Stat("config.json"); os.IsNotExist(err) {
		err := raytracer.CreateConfig("config.json")
		if err != nil {
			fmt.Println(err.Error())
		}
	} else {
		cf := "config.json"
		configFile = &cf
	}

	err := s.Init(sceneFile, *configFile, *environmentMap)
	if err != nil {
		log.Println(err.Error())
		return
	}
	log.Printf("Render %d percent of the image", *percent)
	raytracer.GlobalConfig.Percentage = *percent

	// Depth of field overrides. Flags only apply when explicitly provided, so
	// config.json stays the source of truth for untouched parameters.
	if *dof {
		raytracer.GlobalConfig.DepthOfField = true
	}
	if *aperture != 0 {
		raytracer.GlobalConfig.Aperture = *aperture
	}
	if *focalDistance != 0 {
		raytracer.GlobalConfig.FocalDistance = *focalDistance
	}
	if *dofSamples != 0 {
		raytracer.GlobalConfig.DOFSamples = *dofSamples
	}
	if *dofSeed != 0 {
		raytracer.GlobalConfig.DOFSeed = *dofSeed
	}

	if err := raytracer.Render(&s, *left, *right, *top, *bottom, *percent, size); err != nil {
		log.Fatalf("Render failed: %s", err.Error())
	}
}
