package raytracer

import (
	"math"
	"testing"
)

func testCamera() *Camera {
	return &Camera{
		Position: Vector{0, 0, 0, 1},
		Target:   Vector{0, 0, 10, 1},
		Up:       Vector{0, 1, 0, 0},
		Fov:      60,
		Near:     0.1,
		Far:      100,
	}
}

func TestNewLensCameraConstruction(t *testing.T) {
	lens, err := newLensCamera(testCamera(), 0.5, 8.0, 16, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lens.aperture != 0.5 || lens.focalDistance != 8.0 || lens.samples != 16 || lens.seed != 42 {
		t.Fatalf("lens parameters not stored: %+v", lens)
	}
	// Basis must be orthonormal.
	if n := vectorNorm(lens.forward); math.Abs(n-1) > 1e-12 {
		t.Errorf("forward not unit: %f", n)
	}
	if n := vectorNorm(lens.side); math.Abs(n-1) > 1e-12 {
		t.Errorf("side not unit: %f", n)
	}
	if n := vectorNorm(lens.up); math.Abs(n-1) > 1e-12 {
		t.Errorf("up not unit: %f", n)
	}
	if dot(lens.forward, lens.side) > 1e-12 || dot(lens.forward, lens.up) > 1e-12 || dot(lens.side, lens.up) > 1e-12 {
		t.Errorf("basis not orthogonal: %+v", lens)
	}
	if lens.forward[2] <= 0 {
		t.Errorf("forward must point toward target, got %+v", lens.forward)
	}
}

func TestNewLensCameraInvalidInputs(t *testing.T) {
	base := testCamera()
	cases := []struct {
		name     string
		aperture float64
		focal    float64
		samples  int
		mutate   func(*Camera)
		want     error
	}{
		{"NaN aperture", math.NaN(), 8, 16, nil, ErrInvalidAperture},
		{"+Inf aperture", math.Inf(1), 8, 16, nil, ErrInvalidAperture},
		{"negative aperture", -0.1, 8, 16, nil, ErrInvalidAperture},
		{"zero focal distance", 0.5, 0, 16, nil, ErrInvalidFocalDistance},
		{"negative focal distance", 0.5, -3, 16, nil, ErrInvalidFocalDistance},
		{"NaN focal distance", 0.5, math.NaN(), 16, nil, ErrInvalidFocalDistance},
		{"Inf focal distance", 0.5, math.Inf(-1), 16, nil, ErrInvalidFocalDistance},
		{"zero samples", 0.5, 8, 0, nil, ErrInvalidSampleCount},
		{"negative samples", 0.5, 8, -2, nil, ErrInvalidSampleCount},
		{"too many samples", 0.5, 8, maxDOFSamples + 1, nil, ErrInvalidSampleCount},
		{
			"degenerate position equals target", 0.5, 8, 16,
			func(c *Camera) { c.Target = c.Position }, ErrDegenerateCamera,
		},
		{
			"up parallel to view direction", 0.5, 8, 16,
			func(c *Camera) { c.Up = Vector{0, 0, 1, 0} }, ErrDegenerateCamera,
		},
		{
			"zero up vector", 0.5, 8, 16,
			func(c *Camera) { c.Up = Vector{0, 0, 0, 0} }, ErrDegenerateCamera,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cam := *base
			if tc.mutate != nil {
				tc.mutate(&cam)
			}
			lens, err := newLensCamera(&cam, tc.aperture, tc.focal, tc.samples, 1)
			if err != tc.want {
				t.Fatalf("want %v, got %v (lens=%v)", tc.want, err, lens)
			}
			if lens != nil {
				t.Fatalf("lens must be nil on error")
			}
		})
	}
}

func TestConfigureLensCameraZeroApertureAndDisabled(t *testing.T) {
	scene := &Scene{Cameras: []Camera{*testCamera()}}

	GlobalConfig.DepthOfField = false
	GlobalConfig.Aperture = 0.5
	if lens, err := configureLensCamera(scene); err != nil || lens != nil {
		t.Fatalf("disabled DoF must yield nil lens, got %v %v", lens, err)
	}

	GlobalConfig.DepthOfField = true
	GlobalConfig.Aperture = 0
	if lens, err := configureLensCamera(scene); err != nil || lens != nil {
		t.Fatalf("zero aperture must yield nil lens (pinhole path), got %v %v", lens, err)
	}

	scene.Cameras = nil
	GlobalConfig.Aperture = 0.5
	if _, err := configureLensCamera(scene); err != ErrNoCamera {
		t.Fatalf("missing camera must return ErrNoCamera, got %v", err)
	}
}

// All rays emitted by the lens must pass through the same focal point on the
// focus plane: the pinhole ray's intersection with that plane.
func TestLensRaysConvergeOnFocusPlane(t *testing.T) {
	lens, err := newLensCamera(testCamera(), 0.8, 7.0, 64, 7)
	if err != nil {
		t.Fatal(err)
	}
	baseDir := normalizeVector(Vector{0.15, -0.1, 1, 0})
	rays := lens.rays(baseDir, 11, 23)
	if len(rays) != 64 {
		t.Fatalf("want 64 rays, got %d", len(rays))
	}

	// Sample 0 is the pinhole ray from the lens center.
	if rays[0].Origin != lens.position {
		t.Errorf("first sample must originate at the lens center, got %+v", rays[0].Origin)
	}
	if rays[0].Direction != baseDir {
		t.Errorf("first sample must be the pinhole direction")
	}

	denom := dot(baseDir, lens.forward)
	tFocus := lens.focalDistance / denom
	want := addVector(lens.position, scaleVector(baseDir, tFocus))

	for i, r := range rays {
		// Intersect the ray with the focus plane and compare to the focal point.
		d := dot(r.Direction, lens.forward)
		if d <= 0 {
			t.Fatalf("ray %d does not travel toward the focus plane", i)
		}
		tt := dot(subVector(want, r.Origin), lens.forward) / d
		hit := addVector(r.Origin, scaleVector(r.Direction, tt))
		if dist := vectorDistance(hit, want); dist > 1e-9 {
			t.Fatalf("ray %d misses focal point by %g", i, dist)
		}
		if !finiteVector(r.Origin) || !finiteVector(r.Direction) {
			t.Fatalf("ray %d contains non-finite values", i)
		}
	}
}

// Lens sample origins must lie inside the aperture disk and stay on the lens
// plane; with a zero aperture every ray is the pinhole ray.
func TestLensSampleOriginsInsideAperture(t *testing.T) {
	lens, err := newLensCamera(testCamera(), 0.6, 5.0, 128, 3)
	if err != nil {
		t.Fatal(err)
	}
	rays := lens.rays(normalizeVector(Vector{0.2, 0.1, 1, 0}), 4, 5)
	for i, r := range rays {
		off := subVector(r.Origin, lens.position)
		if dot(off, lens.forward) > 1e-12 {
			t.Errorf("ray %d origin leaves the lens plane", i)
		}
		radius := math.Sqrt(vectorNorm(off))
		if radius > lens.aperture+1e-12 {
			t.Errorf("ray %d origin outside aperture: %f > %f", i, radius, lens.aperture)
		}
	}

	zeroAperture, err := newLensCamera(testCamera(), 0.0, 5.0, 8, 3)
	if err != nil {
		t.Fatal(err)
	}
	base := normalizeVector(Vector{0.2, 0.1, 1, 0})
	zr := zeroAperture.rays(base, 4, 5)
	for _, r := range zr {
		if r.Origin != zeroAperture.position || !vectorsClose(r.Direction, base, 1e-12) {
			t.Fatal("zero aperture must only emit pinhole rays")
		}
	}
}

// A single sample is the pinhole ray, so one-sample DoF is well defined even
// at the minimum allowed sample count.
func TestSingleSampleIsPinhole(t *testing.T) {
	lens, err := newLensCamera(testCamera(), 0.9, 4.0, 1, 11)
	if err != nil {
		t.Fatal(err)
	}
	base := normalizeVector(Vector{-0.3, 0.2, 1, 0})
	rays := lens.rays(base, 0, 0)
	if len(rays) != 1 || rays[0].Origin != lens.position || rays[0].Direction != base {
		t.Fatalf("single sample must be the pinhole ray, got %+v", rays)
	}
}

// Fixed seed + identical coordinates must produce an identical stream, and
// different coordinates must not share a stream (pixel order independence).
func TestDOFRNGDeterminism(t *testing.T) {
	a := newDOFRNG(99, 10, 20)
	b := newDOFRNG(99, 10, 20)
	c := newDOFRNG(99, 11, 20)
	for i := 0; i < 100; i++ {
		va, vb, vc := a.float64(), b.float64(), c.float64()
		if va != vb {
			t.Fatalf("same seed/coords diverged at %d: %f != %f", i, va, vb)
		}
		if va == vc && i < 5 {
			// Different coordinates should (overwhelmingly) differ.
			t.Fatalf("different pixel coordinates produced identical stream")
		}
		if math.IsNaN(va) || math.IsInf(va, 0) || va < 0 || va >= 1 {
			t.Fatalf("rng out of [0,1): %f", va)
		}
	}
}

func finiteVector(v Vector) bool {
	for i := 0; i < 4; i++ {
		if math.IsNaN(v[i]) || math.IsInf(v[i], 0) {
			return false
		}
	}
	return true
}

func vectorsClose(a, b Vector, eps float64) bool {
	for i := 0; i < 4; i++ {
		if math.Abs(a[i]-b[i]) > eps {
			return false
		}
	}
	return true
}
