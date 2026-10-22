package psdfont

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
)

func cff2VerifyPortableFonts(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("testdata/cff2/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Fonts map[string]struct {
			SHA256 string `json:"sha256"`
			Size   int
		}
	}
	if err := json.Unmarshal(data, &manifest, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Fonts) == 0 {
		t.Fatal("portable font manifest must not be empty")
	}
	for name, record := range manifest.Fonts {
		font, err := os.ReadFile(filepath.Join("testdata/cff2/fonts", name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(font)
		if len(font) != record.Size || hex.EncodeToString(digest[:]) != record.SHA256 {
			t.Fatalf("portable font %s source bytes differ from owned manifest", name)
		}
	}
}
