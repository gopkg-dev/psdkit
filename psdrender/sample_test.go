package psdrender_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"os"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
	"github.com/gopkg-dev/psdkit/psdparse"
	"github.com/gopkg-dev/psdkit/psdrender"
)

// TestOfficialFrontendHashes compares raw RGBA against preexisting frontend engine goldens.
func TestOfficialFrontendHashes(t *testing.T) {
	filename := os.Getenv("PSD_OFFICIAL_PATH")
	if filename == "" {
		filename = testfixture.Sample(t)
	}
	document, err := psdparse.ParseFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := psdrender.New(document)
	if err != nil {
		t.Fatal(err)
	}
	config := psdrender.DefaultConfig(engine.Metadata())
	solid := image.NewNRGBA(image.Rect(0, 0, 1000, 2000))
	pattern := image.NewNRGBA(image.Rect(0, 0, 1000, 2000))
	for offset := 0; offset < len(solid.Pix); offset += 4 {
		copy(solid.Pix[offset:offset+4], []uint8{0, 220, 220, 255})
		alpha := uint8(255)
		if offset/4%4 == 0 {
			alpha = 0
		} else if offset/4%3 == 0 {
			alpha = 127
		}
		pattern.Pix[offset] = uint8(offset / 4 % 251)
		pattern.Pix[offset+1] = uint8(offset / 4000 % 251)
		pattern.Pix[offset+2] = psdrender.ClampByte(float64(offset)/16 - float64((offset/16)/255)*255)
		pattern.Pix[offset+3] = alpha
	}
	for _, test := range []struct {
		name     string
		pixels   *image.NRGBA
		expected string
	}{
		{"solid", solid, "f8bd8d186e681a1cae2c7e0dfebfa73d20c69a55558f93ef7cdbfaf938e7916e"},
		{"transparent", pattern, "1609bdc8cedf53eac8794f18018b6086cf269e546c453ea8586d4088eaff6ef1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := engine.Render(context.Background(), config,
				map[string]*image.NRGBA{config.Objects[0].Tag: test.pixels}, nil)
			if err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(result.Pix))
			if digest != test.expected {
				if directory := os.Getenv("PSD_RGBA_ORACLE_DIR"); directory != "" {
					reference, readError := os.ReadFile(directory + "/psd-" + test.name + "-front.rgba")
					if readError != nil {
						t.Fatal(readError)
					}
					count, maximum := 0, 0
					for index, value := range result.Pix {
						if value != reference[index] {
							if count < 10 {
								t.Logf("difference x=%d y=%d channel=%d got=%v expected=%v",
									index/4%800, index/3200, index%4, result.Pix[index/4*4:index/4*4+4], reference[index/4*4:index/4*4+4])
							}
							difference := int(value) - int(reference[index])
							if difference < 0 {
								difference = -difference
							}
							maximum = max(maximum, difference)
							count++
						}
					}
					t.Logf("differing channels=%d maximum=%d", count, maximum)
				}
				t.Fatalf("RGBA hash = %s, frontend = %s", digest, test.expected)
			}
		})
	}
}
