package raytracer

import (
	"errors"
	"math"
)

// Depth of field (thin lens) camera.
//
// The original camera is a pinhole camera: every pixel fires exactly one ray
// from the camera position, so the whole image is sharp. A thin lens camera
// instead fires rays from random points on a circular lens (the aperture) and
// aims every ray at the point where the pinhole ray intersects the focus
// plane. Points on the focus plane are sampled by the same converging cone of
// rays and stay sharp; points in front of or behind the plane project to a
// circle of confusion and appear blurred.
//
// When the aperture radius is zero (or depth of field is disabled), no lens
// camera is built and the renderer keeps its original pinhole code path, so
// zero-aperture output is identical to the original image.

// Sentinel errors returned while building a lens camera. They make invalid
// configurations explicit instead of silently producing broken renders.
var (
	// ErrInvalidAperture is returned for NaN/Inf or negative aperture radius.
	ErrInvalidAperture = errors.New("dof: aperture must be a finite, non-negative radius")
	// ErrInvalidFocalDistance is returned for NaN/Inf or non-positive focal distance.
	ErrInvalidFocalDistance = errors.New("dof: focal distance must be a finite, positive number")
	// ErrInvalidSampleCount is returned for fewer than 1 or more than maxDOFSamples samples.
	ErrInvalidSampleCount = errors.New("dof: sample count must be between 1 and 1024")
	// ErrDegenerateCamera is returned when position and target coincide or the
	// up vector is parallel to the view direction.
	ErrDegenerateCamera = errors.New("dof: degenerate camera orientation")
	// ErrNoCamera is returned when the scene has no camera but DoF is requested.
	ErrNoCamera = errors.New("dof: scene has no camera")
)

// maxDOFSamples bounds the per-pixel work and the memory of a lens ray batch.
const maxDOFSamples = 1024

// LensCamera is a thin lens camera attached to the scene camera.
type LensCamera struct {
	position      Vector  // lens center, world space
	forward       Vector  // unit vector from the lens toward the target
	side          Vector  // unit lens basis vector
	up            Vector  // unit lens basis vector
	aperture      float64 // lens radius in world units
	focalDistance float64 // distance from the lens center to the focus plane
	samples       int     // rays fired per pixel
	seed          int64   // fixed seed for reproducible lens sampling
}

// LensRay is a single ray emitted from the lens surface.
type LensRay struct {
	Origin    Vector
	Direction Vector
}

// newLensCamera validates the depth of field parameters and the camera
// orientation and builds a thin lens camera.
func newLensCamera(camera *Camera, aperture, focalDistance float64, samples int, seed int64) (*LensCamera, error) {
	if math.IsNaN(aperture) || math.IsInf(aperture, 0) || aperture < 0 {
		return nil, ErrInvalidAperture
	}
	if math.IsNaN(focalDistance) || math.IsInf(focalDistance, 0) || focalDistance <= 0 {
		return nil, ErrInvalidFocalDistance
	}
	if samples < 1 || samples > maxDOFSamples {
		return nil, ErrInvalidSampleCount
	}

	forward := subVector(camera.Target, camera.Position)
	forward[3] = 0
	if vectorNorm(forward) < DIFF {
		return nil, ErrDegenerateCamera
	}
	forward = normalizeVector(forward)

	side := crossProduct(forward, camera.Up)
	if vectorNorm(side) < DIFF {
		// forward and up are parallel (or up is the zero vector): no stable
		// lens plane can be built.
		return nil, ErrDegenerateCamera
	}
	side = normalizeVector(side)
	up := normalizeVector(crossProduct(side, forward))

	position := camera.Position
	position[3] = 1

	return &LensCamera{
		position:      position,
		forward:       forward,
		side:          side,
		up:            up,
		aperture:      aperture,
		focalDistance: focalDistance,
		samples:       samples,
		seed:          seed,
	}, nil
}

// configureLensCamera builds the scene's lens camera from the global
// configuration. Disabled depth of field or a zero aperture yields (nil, nil),
// which keeps the original pinhole rendering path untouched.
func configureLensCamera(scene *Scene) (*LensCamera, error) {
	if !GlobalConfig.DepthOfField || GlobalConfig.Aperture == 0 {
		return nil, nil
	}
	if len(scene.Cameras) == 0 {
		return nil, ErrNoCamera
	}
	camera := &scene.Cameras[0]
	return newLensCamera(
		camera,
		GlobalConfig.Aperture,
		GlobalConfig.FocalDistance,
		GlobalConfig.DOFSamples,
		GlobalConfig.DOFSeed,
	)
}

// dofRNG is a deterministic xorshift64* random source. The stream depends only
// on the fixed lens seed and the pixel coordinates, so the render is
// reproducible for a fixed seed and independent of the (shuffled) pixel
// processing order. It never consumes the global math/rand source.
type dofRNG struct {
	state uint64
}

// newDOFRNG mixes the lens seed and the pixel coordinates into a non-zero
// xorshift state. Negative coordinates (antialiasing subpixels) are allowed.
func newDOFRNG(seed int64, x, y int) *dofRNG {
	h := uint64(seed)
	h ^= uint64(int64(x))*0x9E3779B97F4A7C15 + 0xD1B54A32D192ED03
	h = (h ^ (h >> 30)) * 0xBF58476D1CE4E5B9
	h ^= uint64(int64(y))*0x94D049BB133111EB + 0x27D4EB2F165667C5
	h = (h ^ (h >> 27)) * 0x94D049BB133111EB
	h ^= h >> 31
	if h == 0 {
		h = 0x2545F4914F6CDD1D
	}
	return &dofRNG{state: h}
}

// float64 returns a pseudo random number in [0, 1).
func (r *dofRNG) float64() float64 {
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return float64((x*0x2545F4914F6CDD1D)>>11) / float64(uint64(1)<<53)
}

// ray produces lens sample number `sample` for the pinhole ray baseDir.
//
// Sample 0 is always the pinhole ray through the lens center, so a single
// sample per pixel reproduces the original image even with a non-zero
// aperture. Remaining samples are uniform over the lens area (sqrt disk
// sampling) and aimed at the intersection of the pinhole ray with the focus
// plane.
func (l *LensCamera) ray(baseDir Vector, rng *dofRNG, sample int) LensRay {
	// A zero aperture is a pinhole: every lens sample is the pinhole ray.
	if sample == 0 || l.aperture == 0 {
		return LensRay{Origin: l.position, Direction: baseDir}
	}

	radius := l.aperture * math.Sqrt(rng.float64())
	theta := 2 * math.Pi * rng.float64()
	offset := addVector(
		scaleVector(l.side, math.Cos(theta)*radius),
		scaleVector(l.up, math.Sin(theta)*radius),
	)
	origin := addVector(l.position, offset)
	origin[3] = 1

	// Focal point: where the pinhole ray crosses the focus plane
	// {p | (p - position) . forward = focalDistance}.
	denom := dot(baseDir, l.forward)
	if !(denom > 0) {
		// View ray points away from the focus plane: fall back to the
		// pinhole ray instead of dividing by a non-positive number.
		return LensRay{Origin: origin, Direction: baseDir}
	}
	t := l.focalDistance / denom
	focalPoint := addVector(l.position, scaleVector(baseDir, t))

	direction := subVector(focalPoint, origin)
	if vectorNorm(direction) < DIFF {
		// Extremely unlikely degenerate geometry (lens point on the focal
		// point): keep a finite, well-defined direction.
		return LensRay{Origin: origin, Direction: baseDir}
	}
	return LensRay{Origin: origin, Direction: normalizeVector(direction)}
}

// rays generates the full deterministic lens ray batch for the pixel (or
// antialiasing subpixel) identified by keyX, keyY.
func (l *LensCamera) rays(baseDir Vector, keyX, keyY int) []LensRay {
	rng := newDOFRNG(l.seed, keyX, keyY)
	result := make([]LensRay, l.samples)
	for i := 0; i < l.samples; i++ {
		result[i] = l.ray(baseDir, rng, i)
	}
	return result
}

// averageLensRays fires the lens ray batch for a pinhole ray and averages the
// shaded result. The averaging weight per ray is equal, matching the
// antialiasing semantics used by the pinhole renderer. Misses contribute the
// configured transparent color just like ordinary scene rays; alpha is kept
// at 0 only when every ray missed.
func averageLensRays(scene *Scene, baseDir Vector, keyX, keyY int) Vector {
	lens := scene.lens
	rays := lens.rays(baseDir, keyX, keyY)

	totalColor := Vector{}
	hits := 0.0
	for i := range rays {
		hit := raycastSceneIntersect(scene, rays[i].Origin, rays[i].Direction)
		totalColor = addVector(totalColor, hit.render(scene, 0))
		if hit.Hit {
			hits++
		}
	}
	totalColor = scaleVector(totalColor, 1.0/float64(len(rays)))
	if hits > 0 {
		totalColor[3] = 1
	}
	return totalColor
}
