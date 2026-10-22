package psdcompose

import (
	"context"
	"image"
	"runtime"
	"sync"
)

func (plan *composePlan) renderPixels(ctx context.Context, result *image.NRGBA) error {
	workers := plan.options.Workers
	if workers == 0 {
		workers = min(4, runtime.GOMAXPROCS(0))
	}
	workers = min(workers, runtime.GOMAXPROCS(0), max(1, (result.Rect.Dy()+31)/32))
	if runtime.GOARCH == "wasm" || result.Rect.Dx()*result.Rect.Dy() < 65536 {
		workers = 1
	}
	// Each worker owns its recursion, cancellation and per-pixel evaluation counters.
	extra := int64(plan.depth+1) * 512
	for workers > 1 && int64(workers-1) > (plan.options.MaxWorkingBytes-plan.workingBytes)/extra {
		workers--
	}
	if workers <= 1 {
		return plan.renderRows(ctx, result, result.Rect)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var tasks sync.WaitGroup
	var failure error
	var failed sync.Once
	for worker := range workers {
		tasks.Go(func() {
			operation := *plan
			operation.renderContext = ctx
			for y := result.Rect.Min.Y + worker*32; y < result.Rect.Max.Y; y += workers * 32 {
				window := image.Rect(result.Rect.Min.X, y, result.Rect.Max.X, min(y+32, result.Rect.Max.Y))
				if err := operation.renderRows(ctx, result, window); err != nil {
					failed.Do(func() { failure = err; cancel() })
					return
				}
			}
		})
	}
	tasks.Wait()
	return failure
}

func (plan *composePlan) renderRows(ctx context.Context, result *image.NRGBA, window image.Rectangle) error {
	for y := window.Min.Y; y < window.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := window.Min.X; x < window.Max.X; x++ {
			if x&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			plan.pixelEvaluations = 0
			pixel := plan.stack(plan.document.Layers, composePixel{}, x, y)
			if plan.renderFailure != nil {
				return plan.renderFailure
			}
			offset := (y-result.Rect.Min.Y)*result.Stride + (x-result.Rect.Min.X)*4
			for component := range 3 {
				result.Pix[offset+component] = composeByte(pixel.color[component])
			}
			result.Pix[offset+3] = composeByte(pixel.alpha)
		}
	}
	return nil
}
