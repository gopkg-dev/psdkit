package psdfont

import "math"

type hintAnchor struct{ source, target float64 }

type hintAxisMap struct {
	anchors [192]hintAnchor
	count   int
}

type hintGlyphMaps struct {
	x, y                             hintAxisMap
	active                           [12]byte
	nextMask                         int
	initialized                      bool
	scaleX, scaleY, offsetX, offsetY float64
	latticeX, latticeY               float64
	bluePolicy                       CFFBluePolicy
}

func (maps *hintGlyphMaps) initialize(
	work *hintWork, hints *CFFHints, transform [6]float64, scale float64, positioned PositionedGlyph,
	options HintOptions,
) error {
	if hints == nil {
		return fontError(ErrUnsupported, "hint.source", positioned.ClusterStart,
			"CFF1 fitting requires PreserveCFFHints metadata")
	}
	for _, value := range hints.Matrix {
		if !finite(value) {
			return fontError(ErrMalformed, "hint.matrix", 0, "nonfinite source FontMatrix")
		}
	}
	if hints.Matrix[0] <= 0 || hints.Matrix[3] <= 0 || hints.Matrix[1] != 0 || hints.Matrix[2] != 0 {
		return fontError(ErrUnsupported, "hint.matrix", 0, "fitting requires positive axis-aligned FontMatrix")
	}
	if len(hints.Stems) > 96 {
		return fontError(ErrLimit, "hint.stems", 0, "more than 96 source stems")
	}
	maps.scaleX, maps.scaleY = transform[0]*scale*hints.Matrix[0], transform[3]*scale*hints.Matrix[3]
	maps.offsetX = transform[0]*(positioned.X+scale*hints.Matrix[4]) + transform[4]
	maps.offsetY = -transform[3]*positioned.Y - transform[5] + transform[3]*scale*hints.Matrix[5]
	if options.SnapBaselineXQuarter {
		maps.offsetX = hintRoundQuarter(transform[0]*positioned.X+transform[4]) +
			transform[0]*scale*hints.Matrix[4]
	}
	if options.SnapBaselineY {
		maps.offsetY = -hintRound(transform[3]*positioned.Y+transform[5]) +
			transform[3]*scale*hints.Matrix[5]
	}
	maps.bluePolicy = options.BluePolicy
	if options.Anchor == CFFHintBaseline {
		maps.latticeX = transform[0]*positioned.X + transform[4]
		maps.latticeY = -transform[3]*positioned.Y - transform[5]
	}
	if options.Grid == CFFHintCenters {
		maps.latticeX += .5
		maps.latticeY += .5
	}
	if !finite(maps.scaleX) || !finite(maps.scaleY) || maps.scaleX <= 0 || maps.scaleY <= 0 ||
		!finite(maps.offsetX) || !finite(maps.offsetY) {
		return fontError(ErrLimit, "hint.matrix", 0, "source-to-device coefficients exceed precision")
	}
	for _, stem := range hints.Stems {
		if err := work.step(); err != nil {
			return err
		}
		if !finite(stem.Low) || !finite(stem.High) || stem.Low > stem.High ||
			stem.Edge != (stem.Ghost == -20 || stem.Ghost == -21) || stem.Edge && stem.Low != stem.High ||
			!stem.Edge && stem.Ghost != 0 {
			return fontError(ErrMalformed, "hint.stems", 0, "invalid source stem or ghost identity")
		}
	}
	previous := -1
	for _, mask := range hints.Masks {
		if err := work.step(); err != nil {
			return err
		}
		if mask.CommandIndex < previous || mask.CommandIndex < 0 || mask.CommandIndex > len(positioned.Glyph.Path) ||
			mask.StemCount < 1 || mask.StemCount > len(hints.Stems) || len(mask.Active) != (mask.StemCount+7)/8 {
			return fontError(ErrMalformed, "hint.mask", 0, "invalid active mask range or stem count")
		}
		if mask.StemCount%8 != 0 && mask.Active[len(mask.Active)-1]&byte((1<<(8-mask.StemCount%8))-1) != 0 {
			return fontError(ErrMalformed, "hint.mask", 0, "unused active mask bits are nonzero")
		}
		previous = mask.CommandIndex
	}
	if err := validateHintPrivate(work, hints.Private); err != nil {
		return err
	}
	for index := range maps.active {
		maps.active[index] = 0xff
	}
	return nil
}

func validateHintPrivate(work *hintWork, private CFFHintPrivate) error {
	if !finite(private.BlueScale) || private.BlueScale <= 0 || !finite(private.BlueShift) || private.BlueShift < 0 ||
		!finite(private.BlueFuzz) || private.BlueFuzz < 0 || !finite(private.StdHW) || private.StdHW < 0 ||
		!finite(private.StdVW) || private.StdVW < 0 {
		return fontError(ErrMalformed, "hint.private", 0, "invalid source blue or standard-width value")
	}
	for family, values := range [][]float64{
		private.BlueValues, private.OtherBlues, private.FamilyBlues, private.FamilyOtherBlues,
		private.StemSnapH, private.StemSnapV,
	} {
		blueLimit := 14
		if family == 1 || family == 3 {
			blueLimit = 10
		}
		if family < 4 && (len(values)%2 != 0 || len(values) > blueLimit) || family >= 4 && len(values) > 12 {
			return fontError(ErrMalformed, "hint.private", 0, "invalid source blue or stem-snap array length")
		}
		for index, value := range values {
			if err := work.step(); err != nil {
				return err
			}
			badOrder := family < 4 && index%2 == 1 && value < values[index-1]
			if family >= 4 && index > 0 && value < values[index-1] {
				badOrder = true
			}
			if !finite(value) || badOrder || family >= 4 && value <= 0 {
				return fontError(ErrMalformed, "hint.private", 0, "unordered or invalid source hint array")
			}
		}
	}
	return nil
}

func (maps *hintGlyphMaps) command(work *hintWork, hints *CFFHints, index int, mode CFFHintMode) error {
	changed := !maps.initialized
	for maps.nextMask < len(hints.Masks) && hints.Masks[maps.nextMask].CommandIndex <= index {
		if err := work.step(); err != nil {
			return err
		}
		mask := hints.Masks[maps.nextMask]
		maps.nextMask++
		if mask.Counter {
			continue
		}
		maps.active = [12]byte{}
		copy(maps.active[:], mask.Active)
		changed = true
	}
	if !changed {
		return nil
	}
	maps.initialized = true
	maps.x.count, maps.y.count = 0, 0
	for stemIndex, stem := range hints.Stems {
		if err := work.step(); err != nil {
			return err
		}
		if maps.active[stemIndex/8]&(0x80>>uint(stemIndex%8)) == 0 || stem.Vertical && mode == CFFVertical {
			continue
		}
		for earlierIndex := range stemIndex {
			if err := work.step(); err != nil {
				return err
			}
			if maps.active[earlierIndex/8]&(0x80>>uint(earlierIndex%8)) == 0 {
				continue
			}
			earlier := hints.Stems[earlierIndex]
			if earlier.Vertical == stem.Vertical && !earlier.Edge && !stem.Edge &&
				stem.Low < earlier.High && earlier.Low < stem.High {
				return fontError(ErrUnsupported, "hint.stems", 0, "active source stems overlap")
			}
		}
		axis, coefficient, translation, lattice := &maps.y, maps.scaleY, maps.offsetY, maps.latticeY
		standard, snaps := hints.Private.StdHW, hints.Private.StemSnapH
		if stem.Vertical {
			axis, coefficient, translation, lattice = &maps.x, maps.scaleX, maps.offsetX, maps.latticeX
			standard, snaps = hints.Private.StdVW, hints.Private.StemSnapV
		}
		low, high := coefficient*stem.Low+translation, coefficient*stem.High+translation
		if !finite(low) || !finite(high) || math.Abs(low) > 1<<40 || math.Abs(high) > 1<<40 {
			return fontError(ErrLimit, "hint.stems", 0, "device stem exceeds supported precision")
		}
		if stem.Edge {
			target := hintSnap(low, lattice)
			if !stem.Vertical {
				captured, blueTarget, err := blueHintTargetPolicy(work, hints.Private,
					stem.Low, stem.Ghost == -21, coefficient, translation, lattice, maps.bluePolicy)
				if err != nil {
					return err
				}
				if captured {
					target = blueTarget
				}
			}
			axis.anchors[axis.count], axis.count = hintAnchor{low, target}, axis.count+1
			continue
		}
		width := (stem.High - stem.Low) * coefficient
		closest, distance := width, math.Inf(1)
		if standard > 0 {
			distance, closest = math.Abs(standard*coefficient-width), standard*coefficient
		}
		for _, snap := range snaps {
			if err := work.step(); err != nil {
				return err
			}
			if candidate := math.Abs(snap*coefficient - width); candidate < distance {
				distance, closest = candidate, snap*coefficient
			}
		}
		if distance < .5 && mode != CFFFractional && mode != CFFEdgeFractional {
			width = closest
		}
		fittedWidth := max(1.0, hintRound(width))
		fittedLow := hintSnap((low+high-fittedWidth)/2, lattice)
		if mode == CFFFractional {
			fittedWidth = (stem.High - stem.Low) * coefficient
			fittedLow = hintSnap((low+high)/2, lattice) - fittedWidth/2
		}
		if mode == CFFEdgeFractional {
			fittedWidth = (stem.High - stem.Low) * coefficient
			fittedLow = low + hintClosestEdgeShift(low, high, lattice)
		}
		fittedHigh := fittedLow + fittedWidth
		if !stem.Vertical {
			lowerCaptured, lowerTarget, err := blueHintTargetPolicy(work, hints.Private,
				stem.Low, true, coefficient, translation, lattice, maps.bluePolicy)
			if err != nil {
				return err
			}
			upperCaptured, upperTarget, err := blueHintTargetPolicy(work, hints.Private,
				stem.High, false, coefficient, translation, lattice, maps.bluePolicy)
			if err != nil {
				return err
			}
			if lowerCaptured {
				fittedLow, fittedHigh = lowerTarget, lowerTarget+fittedWidth
			}
			if upperCaptured {
				fittedLow, fittedHigh = upperTarget-fittedWidth, upperTarget
			}
			if lowerCaptured && upperCaptured {
				if (mode == CFFFractional || mode == CFFEdgeFractional) &&
					!hintSameCoordinate(upperTarget-lowerTarget, fittedWidth) {
					return fontError(ErrUnsupported, "hint.blue", 0,
						"paired blue-zone captures conflict with the preserved source width")
				}
				fittedLow, fittedHigh = lowerTarget, upperTarget
			}
		}
		axis.anchors[axis.count], axis.anchors[axis.count+1] = hintAnchor{low, fittedLow}, hintAnchor{high, fittedHigh}
		axis.count += 2
	}
	if err := maps.y.prepare(work); err != nil {
		return err
	}
	if mode == CFFFull || mode == CFFFractional || mode == CFFEdgeFractional {
		return maps.x.prepare(work)
	}
	return nil
}

// hintRound uses a translation-invariant pixel lattice, including negative document coordinates.
func hintRound(value float64) float64 { return math.Floor(value + .5) }

func hintRoundQuarter(value float64) float64 { return math.Floor(value*4+.5) / 4 }

func hintSnap(value, lattice float64) float64 { return lattice + hintRound(value-lattice) }

func hintClosestEdgeShift(low, high, lattice float64) float64 {
	shift, distance := 0.0, math.Inf(1)
	for _, edge := range [2]float64{low, high} {
		fraction := edge - lattice - math.Floor(edge-lattice)
		for _, movement := range [2]float64{-fraction, 1 - fraction} {
			if current := math.Abs(movement); current < distance || current == distance && movement > shift {
				shift, distance = movement, current
			}
		}
	}
	return shift
}

func blueHintTarget(
	work *hintWork, private CFFHintPrivate, edge float64, bottom bool, scale, offset, lattice float64,
) (bool, float64, error) {
	return blueHintTargetPolicy(work, private, edge, bottom, scale, offset, lattice, CFFBlueGrid)
}

func blueHintTargetPolicy(
	work *hintWork, private CFFHintPrivate, edge float64, bottom bool, scale, offset, lattice float64,
	policy CFFBluePolicy,
) (bool, float64, error) {
	for family, values := range [][]float64{private.BlueValues, private.OtherBlues} {
		for index := 0; index < len(values); index += 2 {
			if err := work.step(); err != nil {
				return false, 0, err
			}
			bottomZone := family == 1 || index == 0
			if bottom != bottomZone || edge < values[index]-private.BlueFuzz ||
				edge > values[index+1]+private.BlueFuzz {
				continue
			}
			flat := values[index]
			if bottomZone {
				flat = values[index+1]
			}
			familyValues := private.FamilyBlues
			if family == 1 {
				familyValues = private.FamilyOtherBlues
			}
			familyDistance := math.Inf(1)
			alignedFlat := flat
			for familyIndex := 0; familyIndex < len(familyValues); familyIndex += 2 {
				if err := work.step(); err != nil {
					return false, 0, err
				}
				familyBottom := family == 1 || familyIndex == 0
				if familyBottom != bottomZone {
					continue
				}
				familyFlat := familyValues[familyIndex]
				if familyBottom {
					familyFlat = familyValues[familyIndex+1]
				}
				if distance := math.Abs(familyFlat-flat) * scale; distance < 1 && distance < familyDistance {
					familyDistance, alignedFlat = distance, familyFlat
				}
			}
			target := hintSnap(alignedFlat*scale+offset, lattice)
			overshoot := edge - flat
			if policy == CFFBlueResolvedOutward && (values[index+1]-values[index])*scale >= .5 {
				if bottomZone {
					target = math.Floor(alignedFlat*scale) + offset
				} else {
					target = math.Ceil(alignedFlat*scale) + offset
				}
				if scale >= private.BlueScale && math.Abs(overshoot) >= private.BlueShift {
					if bottomZone && overshoot < 0 {
						target -= max(1.0, hintRound(-overshoot*scale))
					} else if !bottomZone && overshoot > 0 {
						target += max(1.0, hintRound(overshoot*scale))
					}
				}
				return true, target, nil
			}
			if scale < private.BlueScale {
				return true, target, nil
			}
			// Above BlueScale, small source overshoots retain their actual device rounding.
			// BlueShift only requires a minimum one-pixel separation for sufficiently distant edges.
			actualTarget := hintSnap(edge*scale+offset, lattice)
			if bottomZone && overshoot < 0 && -overshoot >= private.BlueShift {
				actualTarget = min(actualTarget, target-1)
			} else if !bottomZone && overshoot > 0 && overshoot >= private.BlueShift {
				actualTarget = max(actualTarget, target+1)
			}
			return true, actualTarget, nil
		}
	}
	return false, 0, nil
}

func (axis *hintAxisMap) prepare(work *hintWork) error {
	for index := 1; index < axis.count; index++ {
		anchor := axis.anchors[index]
		position := index
		for position > 0 {
			if err := work.step(); err != nil {
				return err
			}
			if axis.anchors[position-1].source <= anchor.source {
				break
			}
			axis.anchors[position] = axis.anchors[position-1]
			position--
		}
		axis.anchors[position] = anchor
	}
	retained := 0
	for index := 0; index < axis.count; index++ {
		if err := work.step(); err != nil {
			return err
		}
		anchor := axis.anchors[index]
		if retained != 0 {
			previous := axis.anchors[retained-1]
			if anchor.source == previous.source {
				if math.Abs(anchor.target-previous.target) > 1e-9 {
					return fontError(ErrUnsupported, "hint.map", 0, "shared source edges have conflicting fitted targets")
				}
				continue
			}
			if anchor.target < previous.target {
				return fontError(ErrUnsupported, "hint.map", 0, "fitted source edges reverse their order")
			}
		}
		axis.anchors[retained], retained = anchor, retained+1
	}
	axis.count = retained
	return nil
}

func (axis *hintAxisMap) mapPoint(work *hintWork, value float64) (float64, error) {
	if err := work.step(); err != nil {
		return 0, err
	}
	if axis.count == 0 {
		return value, nil
	}
	first, last := axis.anchors[0], axis.anchors[axis.count-1]
	if hintSameCoordinate(value, first.source) {
		return first.target, nil
	}
	if hintSameCoordinate(value, last.source) {
		return last.target, nil
	}
	if value <= first.source {
		return value + first.target - first.source, nil
	}
	if value >= last.source {
		return value + last.target - last.source, nil
	}
	begin, end := 0, axis.count-1
	for end-begin > 1 {
		if err := work.step(); err != nil {
			return 0, err
		}
		middle := begin + (end-begin)/2
		if value < axis.anchors[middle].source {
			end = middle
		} else {
			begin = middle
		}
	}
	lower, upper := axis.anchors[begin], axis.anchors[end]
	if hintSameCoordinate(value, lower.source) {
		return lower.target, nil
	}
	if hintSameCoordinate(value, upper.source) {
		return upper.target, nil
	}
	return lower.target + (value-lower.source)*(upper.target-lower.target)/(upper.source-lower.source), nil
}

// Equivalent source-matrix evaluation orders can differ by a few floating-point ulps.
func hintSameCoordinate(first, second float64) bool {
	magnitude := max(1.0, math.Abs(first), math.Abs(second))
	return math.Abs(first-second) <= 4*(math.Nextafter(magnitude, math.Inf(1))-magnitude)
}
