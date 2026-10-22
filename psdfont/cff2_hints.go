package psdfont

type cff2Hint struct {
	encodedLow, encodedHigh float64
	low, high               float64
	vertical, edge, hm      bool
}

type cff2HintState struct {
	stems       [96]cff2Hint
	selected    [96]uint8
	lastSort    [2]float64
	seen        [2]bool
	horizontal  int
	stage       uint8
	regular     bool
	overlapping bool
	maskSeen    bool
	path        bool
}

// cff2HintsOverlap compares ordinary stem interiors; edge hints never overlap.
func cff2HintsOverlap(first, second cff2Hint, encoded bool) bool {
	if first.vertical != second.vertical || first.edge || second.edge {
		return false
	}
	firstLow, firstHigh, secondLow, secondHigh := first.low, first.high, second.low, second.high
	if encoded {
		firstLow, firstHigh = first.encodedLow, first.encodedHigh
		secondLow, secondHigh = second.encodedLow, second.encodedHigh
	}
	return firstLow < firstHigh && secondLow < secondHigh && firstLow < secondHigh && secondLow < firstHigh
}

// hintStems records encoded identity separately from instance geometry.
func (r *cff2VM) hintStems(operator int, offset int64) error {
	state := &r.hintState
	vertical, hm := operator == 3 || operator == 23, operator == 18 || operator == 23
	orientation := 0
	if vertical {
		orientation = 1
	}
	if r.count == 0 || r.count%2 != 0 || state.stage >= 2 || state.path || r.sink.open ||
		!vertical && state.stage != 0 {
		return r.fail(ErrMalformed, offset, "invalid CFF2 stem arguments or horizontal/vertical order")
	}
	if r.count/2 > len(state.stems)-r.hints {
		return r.fail(ErrLimit, offset, "CFF2 stem count exceeds 96")
	}
	// Each independent stem operator starts its relative coordinates at zero.
	encodedPosition, position := 0.0, 0.0
	for index := 0; index < r.count; index += 2 {
		if err := r.sink.step(offset); err != nil {
			return err
		}
		encodedWidth, width := r.encodedStack[index+1], r.stack[index+1]
		edge := encodedWidth == -20 || encodedWidth == -21
		if !finite(encodedWidth) || !finite(width) ||
			edge && (r.operandBlended[index+1] || width != encodedWidth) ||
			!edge && (encodedWidth < 0 || width < 0) {
			return r.fail(ErrMalformed, offset, "invalid CFF2 stem width or blended edge width")
		}
		encodedFirst := encodedPosition + r.encodedStack[index]
		first := position + r.stack[index]
		encodedPosition, position = encodedFirst+encodedWidth, first+width
		sortPosition := encodedFirst
		stem := cff2Hint{encodedLow: encodedFirst, encodedHigh: encodedPosition,
			low: first, high: position, vertical: vertical, edge: edge, hm: hm}
		if edge {
			if encodedWidth == -21 {
				sortPosition, stem.encodedLow, stem.low = encodedPosition, encodedPosition, position
			}
			stem.encodedHigh, stem.high = stem.encodedLow, stem.low
		}
		if !finite(encodedFirst) || !finite(first) || !finite(encodedPosition) || !finite(position) {
			return r.fail(ErrLimit, offset, "CFF2 stem coordinates exceed supported precision")
		}
		if state.seen[orientation] && sortPosition < state.lastSort[orientation] {
			return r.fail(ErrMalformed, offset, "CFF2 encoded stem or edge order is descending")
		}
		for previous := 0; previous < r.hints; previous++ {
			if err := r.sink.step(offset); err != nil {
				return err
			}
			other := state.stems[previous]
			overlap := cff2HintsOverlap(stem, other, true) || cff2HintsOverlap(stem, other, false)
			if overlap && (!hm || !other.hm) {
				return r.fail(ErrMalformed, offset, "regular CFF2 stems overlap")
			}
			state.overlapping = state.overlapping || overlap
		}
		state.stems[r.hints], r.hints = stem, r.hints+1
		state.lastSort[orientation], state.seen[orientation] = sortPosition, true
		if !vertical {
			state.horizontal++
		}
	}
	state.regular = state.regular || !hm
	if vertical {
		state.stage = 1
	}
	r.count = 0
	return r.sink.ctx.Err()
}

// hintOperator consumes stem or mask data through the glyph's shared work budget.
func (r *cff2VM) hintOperator(operator int, remaining []byte, offset int64) (int, error) {
	if operator != 19 && operator != 20 {
		return 0, r.hintStems(operator, offset)
	}
	state := &r.hintState
	if operator == 20 && (state.stage >= 3 || state.path || r.sink.open) {
		return 0, r.fail(ErrMalformed, offset, "CFF2 counter mask follows a hint mask or path")
	}
	if r.count != 0 {
		if state.horizontal == 0 {
			return 0, r.fail(ErrMalformed, offset, "implicit vertical stems require preceding horizontal stems")
		}
		stemOperator := 23
		if operator == 20 && state.regular {
			stemOperator = 3
		}
		if err := r.hintStems(stemOperator, offset); err != nil {
			return 0, err
		}
	}
	if operator == 19 && state.regular {
		return 0, r.fail(ErrMalformed, offset, "CFF2 hintmask requires HM stem operators exclusively")
	}
	length := (r.hints + 7) / 8
	if length == 0 || length > len(remaining) {
		return 0, r.fail(ErrMalformed, offset, "CFF2 mask is absent or truncated")
	}
	for index := range length {
		if err := r.sink.step(offset + 1 + int64(index)); err != nil {
			return 0, err
		}
	}
	if r.hints%8 != 0 && remaining[length-1]&byte((1<<(8-r.hints%8))-1) != 0 {
		return 0, r.fail(ErrMalformed, offset, "CFF2 mask has nonzero unused bits")
	}
	selected := &state.selected
	selectedCount, horizontal, vertical := 0, 0, 0
	for index := 0; index < r.hints; index++ {
		if err := r.sink.step(offset); err != nil {
			return 0, err
		}
		if remaining[index/8]&(0x80>>uint(index%8)) == 0 {
			continue
		}
		selected[selectedCount], selectedCount = uint8(index), selectedCount+1
		if state.stems[index].vertical {
			vertical++
		} else {
			horizontal++
		}
	}
	if operator == 20 && horizontal < 2 && vertical < 2 {
		return 0, r.fail(ErrMalformed, offset, "CFF2 counter mask needs two stems of the same orientation")
	}
	for first := 0; first < selectedCount; first++ {
		for second := first + 1; second < selectedCount; second++ {
			if err := r.sink.step(offset); err != nil {
				return 0, err
			}
			left, right := state.stems[selected[first]], state.stems[selected[second]]
			if cff2HintsOverlap(left, right, true) || cff2HintsOverlap(left, right, false) {
				return 0, r.fail(ErrMalformed, offset, "selected CFF2 mask stems overlap under the strict hint profile")
			}
		}
	}
	if err := r.sink.ctx.Err(); err != nil {
		return 0, err
	}
	if operator == 20 {
		state.stage = 2
	} else {
		state.maskSeen = true
		if !state.path {
			state.stage = 3
		}
	}
	return length, nil
}

// hintPath starts path data after checking the strict active-hint profile.
func (r *cff2VM) hintPath(offset int64) error {
	if r.hintState.overlapping && !r.hintState.maskSeen {
		return r.fail(ErrMalformed, offset, "overlapping CFF2 HM stems need an initial hint mask")
	}
	r.hintState.path = true
	return nil
}
