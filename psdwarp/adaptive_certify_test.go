package psdwarp

import (
	"context"
	"errors"
	"testing"
)

func TestPrincipalSubdivisionProvesPositivePolynomial(t *testing.T) {
	for _, test := range []struct {
		name     string
		offset   float64
		accepted bool
	}{
		{"positive interior minimum", .01, true},
		{"negative interior minimum", -.01, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var net [4][4]float64
			for v := range 4 {
				net[0][v], net[1][v], net[2][v] = .25+test.offset, -.25+test.offset, .25+test.offset
			}
			work := 0
			err := certifyPrincipal(t.Context(), net, 2, 3, 0, &work)
			if (err == nil) != test.accepted {
				t.Fatalf("Bernstein positivity: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	work := 0
	if err := certifyPrincipal(ctx, [4][4]float64{}, 2, 3, 0, &work); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
