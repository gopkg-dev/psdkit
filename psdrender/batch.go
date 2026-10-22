package psdrender

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf16"
)

// SeededRandom reproduces the editor backend's per-group Mulberry32 sequence.
func SeededRandom(seed int64, index int) func() float64 {
	state := uint32(seed) ^ (uint32(index+1) * 0x9e3779b1)
	return func() float64 {
		state += 0x6d2b79f5
		value := state
		value = (value ^ (value >> 15)) * (value | 1)
		value ^= value + (value^(value>>7))*(value|61)
		return float64(value^(value>>14)) / 4294967296
	}
}

// PickBackgrounds draws independently per enabled row, treating the saved original as one equal option.
// Absent keys deliberately retain original pixels, and disabled/empty rows consume no random draw.
func PickBackgrounds(config Config, metadata Metadata, random func() float64) (map[string]string, error) {
	if err := ValidateConfig(config, metadata); err != nil {
		return nil, err
	}
	if random == nil {
		return nil, errors.New("missing PSD random source")
	}
	selected := make(map[string]string)
	for _, row := range config.RandomImageRows {
		if !row.Enabled || len(row.RandomImages) == 0 {
			continue
		}
		for _, candidate := range row.RandomImages {
			if candidate.URL == "" {
				return nil, errors.New("随机候选图片地址为空")
			}
		}
		choice := random()
		if !finite(choice) || choice < 0 || choice >= 1 {
			return nil, errors.New("随机图抽样值必须在 0 到 1 之间")
		}
		count := len(row.RandomImages)
		if !row.ExcludeOriginalFromRandom {
			count++
		}
		index := int(math.Floor(choice * float64(count)))
		if index < len(row.RandomImages) {
			selected[row.LayerID] = row.RandomImages[index].URL
		}
	}
	return selected, nil
}

// BuildBatchInputs pairs images by upload order and broadcasts singleton tags to every group.
func BuildBatchInputs(config Config, byTag map[string][]string) ([]map[string]string, error) {
	tags := []string{}
	seen := make(map[string]bool)
	count := 1
	for _, object := range config.Objects {
		if !object.Enabled {
			continue
		}
		tag := strings.TrimSpace(object.Tag)
		if tag == "" {
			return nil, errors.New("启用的 PSD 印花对象必须填写印花标签")
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
		images := byTag[tag]
		if len(images) == 0 {
			return nil, fmt.Errorf("印花标签「%s」缺少图片", tag)
		}
		if len(images) > 200 {
			return nil, errors.New("PSD 批量合成最多支持 200 组")
		}
		if slices.Contains(images, "") {
			return nil, errors.New("PSD 素材地址为空")
		}
		if len(images) > 1 {
			if count > 1 && count != len(images) {
				return nil, errors.New("各印花标签的多张图片数量必须一致；单张图片可复用到所有组")
			}
			count = len(images)
		}
	}
	if len(tags) == 0 {
		return nil, errors.New("请至少启用一个 PSD 印花对象")
	}
	groups := make([]map[string]string, count)
	for index := range groups {
		groups[index] = make(map[string]string, len(tags))
		for _, tag := range tags {
			images := byTag[tag]
			selected := index
			if len(images) == 1 {
				selected = 0
			}
			groups[index][tag] = images[selected]
		}
	}
	return groups, nil
}

func safeCharacters(name string) string {
	return strings.Map(func(character rune) rune {
		if character < 32 || strings.ContainsRune("/\\:*?\"<>|", character) {
			return '_'
		}
		return character
	}, name)
}

// OutputNames gives every region a filesystem-safe unique name in document order.
func OutputNames(regions []Slice) []string {
	names := make([]string, len(regions))
	used := make(map[string]bool)
	for index, region := range regions {
		base := safeCharacters(region.Name)
		if base != "" && strings.Trim(base, ".") == "" {
			base = "_"
		}
		if base == "" {
			base = fmt.Sprintf("切片-%d", index+1)
		}
		name := base
		for suffix := 2; used[name]; suffix++ {
			name = fmt.Sprintf("%s-%d", base, suffix)
		}
		used[name], names[index] = true, name
	}
	return names
}

// GroupOutputName matches the frontend batch filename convention, including extension removal.
func GroupOutputName(groupIndex, imageIndex int, name, format string) string {
	name = strings.TrimSpace(safeCharacters(name))
	units := utf16.Encode([]rune(name))
	if len(units) > 150 {
		name = string(utf16.Decode(units[:150]))
	}
	if name == "" {
		name = "样机"
	}
	lower := strings.ToLower(name)
	for _, suffix := range []string{".png", ".jpg", ".jpeg"} {
		if strings.HasSuffix(lower, suffix) {
			name = name[:len(name)-len(suffix)]
			break
		}
	}
	extension := "png"
	if format == "jpeg" {
		extension = "jpg"
	}
	return fmt.Sprintf("%03d-%02d-%s.%s", groupIndex+1, imageIndex+1, name, extension)
}
