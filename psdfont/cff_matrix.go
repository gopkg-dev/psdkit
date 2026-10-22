package psdfont

import "math/big"

var cffIdentityMatrix = [6]float64{1, 0, 0, 1, 0, 0}

const cffMatrixScratchBytes int64 = 64 << 10

type cffMatrixScratch struct {
	first, second, left, right big.Rat
}

// validateCFFMatrix tests rank using the exact binary64 operands rather than an overflowing determinant product.
func validateCFFMatrix(matrix [6]float64, span Span) error {
	var scratch cffMatrixScratch
	return scratch.validate(matrix, span)
}

func (scratch *cffMatrixScratch) validate(matrix [6]float64, span Span) error {
	for _, value := range matrix {
		if !finite(value) {
			return fontError(ErrMalformed, "cff.fontMatrix", span.Offset, "nonfinite FontMatrix operand")
		}
	}
	scratch.first.SetFloat64(matrix[0])
	scratch.second.SetFloat64(matrix[3])
	scratch.left.Mul(&scratch.first, &scratch.second)
	scratch.first.SetFloat64(matrix[1])
	scratch.second.SetFloat64(matrix[2])
	scratch.right.Mul(&scratch.first, &scratch.second)
	if scratch.left.Cmp(&scratch.right) == 0 {
		return fontError(ErrMalformed, "cff.fontMatrix", span.Offset, "singular FontMatrix")
	}
	return nil
}

// composeCFFMatrix computes the full six-component Top(FD(point)) affine transformation.
func composeCFFMatrix(top, fd [6]float64, units uint16, span Span) ([6]float64, error) {
	var scratch cffMatrixScratch
	return composeCFFMatrixWithScratch(top, fd, units, span, &scratch)
}

func composeCFFMatrixWithScratch(
	top, fd [6]float64, units uint16, span Span, scratch *cffMatrixScratch,
) ([6]float64, error) {
	// Sum exact binary64 products before rounding, so intermediate overflow or underflow cannot lose a valid affine.
	terms := [6][5]float64{
		{top[0], fd[0], top[2], fd[1], 0},
		{top[1], fd[0], top[3], fd[1], 0},
		{top[0], fd[2], top[2], fd[3], 0},
		{top[1], fd[2], top[3], fd[3], 0},
		{top[0], fd[4], top[2], fd[5], top[4]},
		{top[1], fd[4], top[3], fd[5], top[5]},
	}
	var result [6]float64
	for index, term := range terms {
		scratch.first.SetFloat64(term[0])
		scratch.second.SetFloat64(term[1])
		scratch.left.Mul(&scratch.first, &scratch.second)
		scratch.first.SetFloat64(term[2])
		scratch.second.SetFloat64(term[3])
		scratch.right.Mul(&scratch.first, &scratch.second)
		scratch.left.Add(&scratch.left, &scratch.right)
		scratch.first.SetFloat64(term[4])
		scratch.left.Add(&scratch.left, &scratch.first)
		scratch.second.SetInt64(int64(units))
		scratch.left.Mul(&scratch.left, &scratch.second)
		result[index], _ = scratch.left.Float64()
		if !finite(result[index]) {
			return [6]float64{}, fontError(ErrLimit, "cff.fontMatrix", span.Offset, "FontMatrix composition overflow")
		}
	}
	if err := scratch.validate(result, span); err != nil {
		return [6]float64{}, fontError(ErrLimit, "cff.fontMatrix.compose", span.Offset,
			"rounded FontMatrix composition loses affine rank")
	}
	return result, nil
}

// resolvedCFFMatrix retains the old canonical identity arithmetic without reciprocal-UPEM multiplication.
func resolvedCFFMatrix(top cffDict, fd *cffDict, units uint16) ([6]float64, error) {
	var scratch cffMatrixScratch
	return resolvedCFFMatrixWithScratch(top, fd, units, &scratch)
}

func resolvedCFFMatrixWithScratch(
	top cffDict, fd *cffDict, units uint16, scratch *cffMatrixScratch,
) ([6]float64, error) {
	canonical := [6]float64{1 / float64(units), 0, 0, 1 / float64(units), 0, 0}
	matrix := [6]float64{0.001, 0, 0, 0.001, 0, 0}
	if top.hasMatrix {
		matrix = top.matrix
	}
	if fd == nil || !fd.hasMatrix {
		if matrix == canonical {
			return cffIdentityMatrix, nil
		}
	}
	if err := scratch.validate(matrix, top.matrixSpan); err != nil {
		return [6]float64{}, err
	}
	if fd == nil || !fd.hasMatrix {
		return composeCFFMatrixWithScratch(matrix, cffIdentityMatrix, units, top.matrixSpan, scratch)
	}
	if err := scratch.validate(fd.matrix, fd.matrixSpan); err != nil {
		return [6]float64{}, err
	}
	if !top.hasMatrix {
		// The legacy explicit-FD CID loading profile cancels the inserted Top .001 and FD x1000 factors.
		return composeCFFMatrixWithScratch(fd.matrix, cffIdentityMatrix, units, fd.matrixSpan, scratch)
	}
	return composeCFFMatrixWithScratch(matrix, fd.matrix, units, fd.matrixSpan, scratch)
}

func (p *cffParser) resolveMatrix(top cffDict, fd *cffDict) ([6]float64, error) {
	if err := p.ctx.Err(); err != nil {
		return [6]float64{}, err
	}
	canonical := [6]float64{1 / float64(p.face.UnitsPerEm), 0, 0, 1 / float64(p.face.UnitsPerEm), 0, 0}
	knownIdentity := (fd == nil || !fd.hasMatrix) &&
		(top.hasMatrix && top.matrix == canonical || !top.hasMatrix && p.face.UnitsPerEm == 1000)
	if knownIdentity {
		return cffIdentityMatrix, p.ctx.Err()
	}
	offset := int(top.matrixOffset - p.base)
	if fd != nil && fd.hasMatrix {
		offset = int(fd.matrixOffset - p.base)
	}
	if err := p.step(offset); err != nil {
		return [6]float64{}, err
	}
	if p.matrixScratch == nil {
		// One reusable reservation covers retained rational limbs and bounded arithmetic/GCD/division temporaries.
		// Binary64 exponents bound final numerators to 4213 bits and unnormalized intermediates to 6361 bits.
		if err := p.reserve(cffMatrixScratchBytes, offset); err != nil {
			return [6]float64{}, err
		}
		p.matrixScratch = &cffMatrixScratch{}
	}
	matrix, err := resolvedCFFMatrixWithScratch(top, fd, p.face.UnitsPerEm, p.matrixScratch)
	if err != nil {
		return [6]float64{}, err
	}
	if err := p.step(offset); err != nil {
		return [6]float64{}, err
	}
	return matrix, nil
}
