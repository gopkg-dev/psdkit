package psdfile

import (
	"context"
	"errors"
	"testing"
)

func TestMeasuredMetadataViewsHonorExactReservations(t *testing.T) {
	source := smartFixtureParse(t, 1, "SoLd", smartFixturePlacement(4, smartFixtureEntries(), nil))
	view, err := source.ReadSmartObjectView(context.Background())
	if err != nil || len(view.Records) != 1 || view.MetadataBytes <= 64 {
		t.Fatalf("measured placement view = %+v, %v", view, err)
	}
	if _, err := source.ReadSmartObjectView(nil, MetadataOptions{MaxBytes: view.MetadataBytes}); err != nil {
		t.Fatalf("exact placement reservation rejected: %v", err)
	}
	if _, err := source.ReadSmartObjectView(nil, MetadataOptions{MaxBytes: view.MetadataBytes - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("undersized placement reservation accepted: %v", err)
	}
	linked := linkedFixtureParse(t, linkedFixtureDocument(1, "lnk2",
		linkedFixtureEntry("liFD", 7, nil, smartFixtureNestedPSD(1), nil)))
	files, err := linked.ReadLinkedFileView(nil)
	if err != nil || len(files.Records) != 1 || files.MetadataBytes <= 64 {
		t.Fatalf("measured linked view = %+v, %v", files, err)
	}
	if _, err := linked.ReadLinkedFileView(nil, MetadataOptions{MaxBytes: files.MetadataBytes}); err != nil {
		t.Fatalf("exact linked reservation rejected: %v", err)
	}
	if _, err := linked.ReadLinkedFileView(nil, MetadataOptions{MaxBytes: files.MetadataBytes - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("undersized linked reservation accepted: %v", err)
	}
	_ = source.Close()
	if _, err := source.ReadSmartObjectView(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed view = %v", err)
	}
}

func TestMeasuredTextViewHonorsExactReservationAndLifecycle(t *testing.T) {
	payload, _ := textFixturePayload(textFixtureEngine("A"))
	source, err := Parse(textFixtureDocument(1, "TySh", payload, true))
	if err != nil {
		t.Fatal(err)
	}
	view, err := source.ReadTextLayerView(nil)
	if err != nil || len(view.Records) != 2 || view.MetadataBytes <= 64 {
		t.Fatalf("text reservation = %d, count=%d, error=%v", view.MetadataBytes, len(view.Records), err)
	}
	if _, err := source.ReadTextLayerView(nil, MetadataOptions{MaxBytes: view.MetadataBytes}); err != nil {
		t.Fatalf("exact text reservation rejected: %v", err)
	}
	if _, err := source.ReadTextLayerView(nil,
		MetadataOptions{MaxBytes: view.MetadataBytes - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("undersized text reservation accepted: %v", err)
	}
	_ = source.Close()
	if _, err := source.ReadTextLayerView(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed text source accepted: %v", err)
	}
	if view.Records[0].Text == "" {
		t.Fatal("owned text metadata was invalidated by source Close")
	}
}
