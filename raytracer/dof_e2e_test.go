package raytracer

import (
	"image"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// Fixed seed for every end-to-end run: the pinhole pipeline uses the global
// math/rand source (pixel shuffle, antialiasing permutation), and the lens
// camera uses its own seeded xorshift source, so renders are reproducible.
func TestMain(m *testing.M) {
	rand.Seed(1234)
	os.Exit(m.Run())
}

// dofTestConfig returns a lightweight, deterministic configuration: no lights,
// no ambient/occlusion and no reflections, so shading reduces to the
// emissive material color plus a black background.
func dofTestConfig(width, height int) Config {
	c := DEFAULT
	c.Width = width
	c.Height = height
	c.AntialiasSamples = 0
	c.Percentage = 100
	// Lights stay enabled so emissive materials show their color; the scene
	// has no non-emissive lights, so shading is just the material color.
	c.RenderLights = true
	c.RenderOcclusion = false
	c.RenderAmbientColors = false
	c.RenderReflections = false
	c.RenderRefractions = false
	c.RenderBumpMap = false
	c.RenderCaustics = false
	c.TransparentColor = Vector{0, 0, 0, 0}
	c.DepthOfField = false
	c.Aperture = 0
	return c
}

// buildDOFTestScene builds a fixed scene with two separate objects:
// a red emissive quad at z=5 (near) and a blue emissive quad at z=10 (far),
// both facing a camera at the origin looking down +Z.
func buildDOFTestScene() *Scene {
	red := Material{Color: Vector{0.9, 0.1, 0.1, 1}, Light: true, LightStrength: 1}
	blue := Material{Color: Vector{0.1, 0.2, 0.9, 1}, Light: true, LightStrength: 1}
	red.Indices = []indice{{0, 1, 2, 0}, {0, 2, 3, 0}}
	blue.Indices = []indice{{0, 1, 2, 0}, {0, 2, 3, 0}}

	near := &Object{
		Matrix:    identityHmgMatrix,
		Materials: map[string]Material{"red": red},
		Vertices: []Vector{
			{-1.5, -1, 5, 1}, {0.5, -1, 5, 1},
			{0.5, 1, 5, 1}, {-1.5, 1, 5, 1},
		},
		Normals: []Vector{{0, 0, 1, 0}, {0, 0, 1, 0}, {0, 0, 1, 0}, {0, 0, 1, 0}},
	}
	far := &Object{
		Matrix:    identityHmgMatrix,
		Materials: map[string]Material{"blue": blue},
		Vertices: []Vector{
			{1.0, -1.5, 10, 1}, {4.0, -1.5, 10, 1},
			{4.0, 1.5, 10, 1}, {1.0, 1.5, 10, 1},
		},
		Normals: []Vector{{0, 0, 1, 0}, {0, 0, 1, 0}, {0, 0, 1, 0}, {0, 0, 1, 0}},
	}
	// prepare() runs the full flatten/UnifyTriangles/KDTree pipeline.

	return &Scene{
		Objects: map[string]*Object{"near": near, "far": far},
		Cameras: []Camera{{
			Position: Vector{0, 0, 0, 1},
			Target:   Vector{0, 0, 1, 1},
			Up:       Vector{0, 1, 0, 0},
			Fov:      60,
			Near:     0.1,
			Far:      1000,
		}},
	}
}

func renderDOFScene(t *testing.T, name string, cfg Config) image.Image {
	t.Helper()
	GlobalConfig = cfg
	scene := buildDOFTestScene()
	out := filepath.Join(t.TempDir(), name+".png")
	scene.OutputFilename = out
	size := ""
	if err := Render(scene, 0, 0, 0, 0, 100, &size); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	return img
}

// pixelsEqual compares two decoded images pixel by pixel.
func pixelsEqual(a, b image.Image) bool {
	ba, bb := a.Bounds(), b.Bounds()
	if ba.Dx() != bb.Dx() || ba.Dy() != bb.Dy() {
		return false
	}
	for y := ba.Min.Y; y < ba.Max.Y; y++ {
		for x := ba.Min.X; x < ba.Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, b2, ba2 := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != b2 || aa != ba2 {
				return false
			}
		}
	}
	return true
}

// countDifferentPixels returns the number of pixels that differ between two
// decoded images of equal size.
func countDifferentPixels(a, b image.Image) int {
	ba := a.Bounds()
	diff := 0
	for y := ba.Min.Y; y < ba.Max.Y; y++ {
		for x := ba.Min.X; x < ba.Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, b2, ba2 := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != b2 || aa != ba2 {
				diff++
			}
		}
	}
	return diff
}

// dominant returns the index (0=red, 1=green, 2=blue) and the normalized
// value of the strongest color channel.
func dominant(r, g, b uint32) (int, float64) {
	chans := [3]float64{float64(r) / 65535, float64(g) / 65535, float64(b) / 65535}
	idx, max := 0, chans[0]
	for i := 1; i < 3; i++ {
		if chans[i] > max {
			idx, max = i, chans[i]
		}
	}
	return idx, max
}

// blurWidth measures the circle of confusion of one object along the middle
// image row. It counts partially covered pixels whose dominant channel is the
// object's color but whose brightness stays below solidCoverage: a sharp
// pinhole silhouette contributes none, while a defocused object spreads a
// gradient band whose width grows with the circle of confusion. Pixels
// dominated by another color are naturally excluded, so adjacent quads do
// not contaminate each other's measurement.
func blurWidth(img image.Image, objectChannel int) int {
	// Solid emissive material renders at ~0.81 normalized intensity; anything
	// noticeably dimmer is a partially covered (defocused) pixel.
	const solidCoverage = 0.75
	bnd := img.Bounds()
	y := bnd.Min.Y + bnd.Dy()/2
	width := 0
	for x := bnd.Min.X; x < bnd.Max.X; x++ {
		r, g, b, _ := img.At(x, y).RGBA()
		idx, strength := dominant(r, g, b)
		if strength == 0 || idx != objectChannel {
			continue
		}
		if strength < solidCoverage {
			width++
		}
	}
	return width
}

const (
	redChannel   = 0
	greenChannel = 1
	blueChannel  = 2
)

func isRed(r, g, b uint32) bool  { return r > 30000 && g < 20000 && b < 20000 }
func isBlue(r, g, b uint32) bool { return b > 30000 && r < 20000 }

// rowHasColor reports whether the middle image row contains at least one
// pixel matching the material color predicate, i.e. the object is visible.
func rowHasColor(img image.Image, isColor func(r, g, b uint32) bool) bool {
	bd := img.Bounds()
	y := bd.Min.Y + bd.Dy()/2
	for x := bd.Min.X; x < bd.Max.X; x++ {
		r, g, b, _ := img.At(x, y).RGBA()
		if isColor(r, g, b) {
			return true
		}
	}
	return false
}

// The same fixed scene rendered twice with the same seed must be identical.
func TestDOFReproducibleFixedSeed(t *testing.T) {
	const w, h = 96, 72
	cfg := dofTestConfig(w, h)
	cfg.DepthOfField = true
	cfg.Aperture = 0.4
	cfg.FocalDistance = 5
	cfg.DOFSamples = 32
	cfg.DOFSeed = 2024

	first := renderDOFScene(t, "dof_repro_1", cfg)
	second := renderDOFScene(t, "dof_repro_2", cfg)
	if !pixelsEqual(first, second) {
		t.Fatalf("rendered images differ")
	}

	// A different seed must change the lens samples (and therefore the image).
	other := cfg
	other.DOFSeed = 777
	third := renderDOFScene(t, "dof_repro_3", other)
	if diff := countDifferentPixels(first, third); diff == 0 {
		t.Fatal("changing the seed had no effect on the depth of field render")
	}
}

// A zero aperture (with DoF enabled) must take the pinhole path and produce
// exactly the same image as DoF disabled.
func TestDOFZeroApertureMatchesPinhole(t *testing.T) {
	const w, h = 96, 72
	pinhole := dofTestConfig(w, h)
	imgPinhole := renderDOFScene(t, "pinhole", pinhole)

	zero := dofTestConfig(w, h)
	zero.DepthOfField = true
	zero.Aperture = 0
	zero.FocalDistance = 5
	zero.DOFSamples = 32
	imgZero := renderDOFScene(t, "zero_aperture", zero)

	if !pixelsEqual(imgPinhole, imgZero) {
		t.Fatalf("zero-aperture render diverges from pinhole")
	}
}

// With the focus plane on the near quad, the near silhouette stays sharp and
// the far quad blurs; the blur radius grows with the aperture. Reversing the
// focus swaps which object is sharp.
func TestDOFBlurRadiusAndFocus(t *testing.T) {
	const w, h = 96, 72

	base := dofTestConfig(w, h)
	imgPinhole := renderDOFScene(t, "focus_base", base)
	pinholeRed := blurWidth(imgPinhole, redChannel)
	pinholeBlue := blurWidth(imgPinhole, blueChannel)

	focusNear := base
	focusNear.DepthOfField = true
	focusNear.Aperture = 0.4
	focusNear.FocalDistance = 5
	focusNear.DOFSamples = 64
	focusNear.DOFSeed = 55
	imgNear := renderDOFScene(t, "focus_near", focusNear)
	nearRed := blurWidth(imgNear, redChannel)
	nearBlue := blurWidth(imgNear, blueChannel)

	// Numerical tolerance for 8-bit rounding at the silhouette of an
	// otherwise perfectly focused object: a few edge pixels may round to a
	// slightly dim shade even though every ray hits the same focal point.
	const focusTolerance = 5

	if nearRed > focusTolerance {
		t.Errorf("focused near quad must stay sharp: blur %d (tolerance %d)", nearRed, focusTolerance)
	}
	if nearBlue <= pinholeBlue {
		t.Errorf("defocused far quad must blur: blur %d vs pinhole %d", nearBlue, pinholeBlue)
	}
	if nearBlue <= nearRed {
		t.Errorf("defocused far quad must blur more than focused near: %d vs %d", nearBlue, nearRed)
	}

	// Larger aperture -> larger circle of confusion on the defocused object.
	wider := focusNear
	wider.Aperture = 0.8
	imgWider := renderDOFScene(t, "focus_near_wide", wider)
	widerBlue := blurWidth(imgWider, blueChannel)
	if widerBlue <= nearBlue {
		t.Errorf("wider aperture must increase far blur: %d vs %d", widerBlue, nearBlue)
	}

	// Move the focus plane to the far quad: the far silhouette sharpens and
	// the near quad becomes the blurred one.
	focusFar := focusNear
	focusFar.Aperture = 0.4
	focusFar.FocalDistance = 10
	imgFar := renderDOFScene(t, "focus_far", focusFar)
	farBlue := blurWidth(imgFar, blueChannel)
	farRed := blurWidth(imgFar, redChannel)
	if farBlue > focusTolerance {
		t.Errorf("focused far quad must stay sharp: blur %d (tolerance %d)", farBlue, focusTolerance)
	}
	if farRed <= pinholeRed {
		t.Errorf("defocused near quad must blur: blur %d vs pinhole %d", farRed, pinholeRed)
	}
	if farRed <= farBlue {
		t.Errorf("defocused near quad must blur more than focused far: %d vs %d", farRed, farBlue)
	}

	// Focused objects keep their original material color (DoF changes focus,
	// never materials or scene contents).
	if !rowHasColor(imgNear, isRed) {
		t.Errorf("focused near quad lost its red material color")
	}
	if !rowHasColor(imgFar, isBlue) {
		t.Errorf("focused far quad lost its blue material color")
	}
}

// DoF must preserve the original antialiasing semantics: getPixel still takes
// AntialiasSamples equally weighted jittered subpixels from the original 8x8
// jitter grid, but each subpixel ray is routed through the lens. The lens
// component itself is deterministic for fixed subpixel coordinates (its RNG
// is keyed by the seed and the coordinates, not the global rand stream).
func TestDOFAntialiasingSemantics(t *testing.T) {
	const w, h = 64, 48
	cfg := dofTestConfig(w, h)
	cfg.AntialiasSamples = 1
	cfg.DepthOfField = true
	cfg.Aperture = 0.4
	cfg.FocalDistance = 5
	cfg.DOFSamples = 16
	cfg.DOFSeed = 9
	GlobalConfig = cfg

	scene := buildDOFTestScene()
	scene.prepare(w, h)
	lens, err := configureLensCamera(scene)
	if err != nil {
		t.Fatal(err)
	}
	scene.lens = lens

	x, y := w/2, h/2

	// Reproduce getPixel's single-subpixel computation manually, consuming the
	// same permutation from a freshly seeded global source.
	rand.Seed(4242)
	p := rand.Perm(64)
	n := p[:cfg.AntialiasSamples][0]
	observer := scene.Cameras[0]
	sw, sh := w*8, h*8
	yi := int(math.Floor(float64(n)/8.0)) + (y * 8) - 4
	xi := (n % 8) + (x * 8) - 4
	rayDir := screenToWorld(xi, yi, sw, sh, observer.Position, *observer.Projection, observer.view)
	manual := averageLensRays(scene, rayDir, xi, yi)

	// getPixel with one AA sample must produce exactly the same value: the
	// original jitter-grid coordinate, routed through the lens, equal weight.
	rand.Seed(4242)
	got := getPixel(scene, x, y)
	if !vectorsClose(got, manual, 1e-12) {
		t.Fatalf("lens antialiasing diverges from the equal-weight subpixel semantics: got %+v want %+v", got, manual)
	}

	// Repeating from the same global stream state is fully reproducible.
	rand.Seed(4242)
	again := getPixel(scene, x, y)
	if !vectorsClose(again, got, 1e-12) {
		t.Fatalf("lens antialiasing is not reproducible")
	}
	if !finiteVector(got) || got[3] != 1 {
		t.Fatalf("antialiased lens pixel is invalid: %+v", got)
	}

	// AntialiasSamples == 0 keeps the original shortcut: return the stored
	// pixel color without firing any extra rays.
	cfg.AntialiasSamples = 0
	GlobalConfig = cfg
	stored := scene.Pixels[x][y].Color
	if got := getPixel(scene, x, y); got != stored {
		t.Fatalf("zero AA samples must return the stored pixel, got %+v want %+v", got, stored)
	}
}

// Invalid configurations must surface as explicit errors from Render instead
// of producing a broken image.
func TestDOFInvalidConfigFromRender(t *testing.T) {
	base := dofTestConfig(48, 36)
	base.DepthOfField = true
	cases := []struct {
		name     string
		aperture float64
		focal    float64
		samples  int
		want     error
	}{
		{"bad focal", 0.4, 0, 16, ErrInvalidFocalDistance},
		{"non-finite aperture", math.NaN(), 5, 16, ErrInvalidAperture},
		{"too few samples", 0.4, 5, 0, ErrInvalidSampleCount},
		{"too many samples", 0.4, 5, maxDOFSamples + 1, ErrInvalidSampleCount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Aperture = tc.aperture
			cfg.FocalDistance = tc.focal
			cfg.DOFSamples = tc.samples
			GlobalConfig = cfg
			scene := buildDOFTestScene()
			scene.OutputFilename = filepath.Join(t.TempDir(), "bad.png")
			size := ""
			if err := Render(scene, 0, 0, 0, 0, 100, &size); err != tc.want {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}

	// Degenerate camera orientation is rejected as well.
	cfg := base
	cfg.Aperture = 0.4
	cfg.FocalDistance = 5
	cfg.DOFSamples = 16
	GlobalConfig = cfg
	scene := buildDOFTestScene()
	scene.Cameras[0].Target = scene.Cameras[0].Position
	scene.OutputFilename = filepath.Join(t.TempDir(), "bad_cam.png")
	size := ""
	if err := Render(scene, 0, 0, 0, 0, 100, &size); err != ErrDegenerateCamera {
		t.Fatalf("want %v, got %v", ErrDegenerateCamera, err)
	}
}

// A higher resolution render with DoF enabled must complete and stay finite
// and reproducible; the sample cap keeps per-pixel work bounded.
func TestDOFHighResolution(t *testing.T) {
	const w, h = 320, 240
	cfg := dofTestConfig(w, h)
	cfg.DepthOfField = true
	cfg.Aperture = 0.4
	cfg.FocalDistance = 5
	cfg.DOFSamples = 4
	cfg.DOFSeed = 13

	first := renderDOFScene(t, "hires_1", cfg)
	second := renderDOFScene(t, "hires_2", cfg)
	if first.Bounds().Dx() != w || first.Bounds().Dy() != h {
		t.Fatalf("unexpected dimensions: %v", first.Bounds())
	}
	if !pixelsEqual(first, second) {
		t.Fatalf("high resolution render not reproducible")
	}
}
