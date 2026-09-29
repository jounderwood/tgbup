package convert_test

import (
	"testing"

	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/convert"
)

func photoMessage() *tg.Message {
	p := &tg.Photo{ID: 777, AccessHash: 2, FileReference: []byte("r"), Sizes: []tg.PhotoSizeClass{
		&tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1}},
		&tg.PhotoSize{Type: "m", W: 320, H: 240, Size: 1000},
		&tg.PhotoSizeProgressive{Type: "y", W: 1280, H: 960, Sizes: []int{100, 5000, 20000}},
	}}
	mm := &tg.MessageMediaPhoto{}
	mm.SetPhoto(p)
	m := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 1}}
	m.SetMedia(mm)
	return m
}

func docMessage(mm *tg.MessageMediaDocument) *tg.Message {
	m := &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 1}}
	m.SetMedia(mm)
	return m
}

func doc(id int64, mime string, attrs ...tg.DocumentAttributeClass) *tg.Document {
	return &tg.Document{ID: id, AccessHash: 3, FileReference: []byte("f"), MimeType: mime, Size: 4096, Attributes: attrs}
}

func withDoc(d *tg.Document, set func(*tg.MessageMediaDocument)) *tg.MessageMediaDocument {
	mm := &tg.MessageMediaDocument{}
	mm.SetDocument(d)
	if set != nil {
		set(mm)
	}
	return mm
}

func TestExtractPhotoPicksLargestSize(t *testing.T) {
	got, ok := convert.ExtractMedia(photoMessage())
	if !ok {
		t.Fatal("no media")
	}
	if got.Key != "photo:777" || got.Kind != "photo" || got.Size != 20000 || got.Mime != "image/jpeg" {
		t.Fatalf("media = %+v", got)
	}
	loc, err := convert.FileLocation(photoMessage())
	if err != nil {
		t.Fatal(err)
	}
	pl, ok := loc.(*tg.InputPhotoFileLocation)
	if !ok || pl.ID != 777 || pl.ThumbSize != "y" || string(pl.FileReference) != "r" {
		t.Fatalf("location = %#v", loc)
	}
}

func TestExtractDocumentKinds(t *testing.T) {
	cases := []struct {
		name string
		mm   *tg.MessageMediaDocument
		kind string
		file string
	}{
		{"pdf", withDoc(doc(1, "application/pdf", &tg.DocumentAttributeFilename{FileName: "отчёт.pdf"}), nil), "document", "отчёт.pdf"},
		{"voice flag", withDoc(doc(2, "audio/ogg", &tg.DocumentAttributeAudio{Voice: true}), func(m *tg.MessageMediaDocument) { m.Voice = true }), "voice", ""},
		{"music", withDoc(doc(3, "audio/mpeg", &tg.DocumentAttributeAudio{}, &tg.DocumentAttributeFilename{FileName: "a.mp3"}), nil), "audio", "a.mp3"},
		{"round video", withDoc(doc(4, "video/mp4", &tg.DocumentAttributeVideo{RoundMessage: true}), func(m *tg.MessageMediaDocument) { m.Round = true }), "video_note", ""},
		{"video", withDoc(doc(5, "video/mp4", &tg.DocumentAttributeVideo{}), nil), "video", ""},
		{"gif", withDoc(doc(6, "video/mp4", &tg.DocumentAttributeVideo{}, &tg.DocumentAttributeAnimated{}), nil), "animation", ""},
		{"sticker", withDoc(doc(7, "image/webp", &tg.DocumentAttributeSticker{Alt: "😀"}), nil), "sticker", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := convert.ExtractMedia(docMessage(c.mm))
			if !ok || got.Kind != c.kind || got.FileName != c.file || got.Size != 4096 {
				t.Fatalf("media = %+v ok=%v, want kind %s file %q", got, ok, c.kind, c.file)
			}
		})
	}
	loc, err := convert.FileLocation(docMessage(cases[0].mm))
	if err != nil {
		t.Fatal(err)
	}
	if dl, ok := loc.(*tg.InputDocumentFileLocation); !ok || dl.ID != 1 || dl.AccessHash != 3 {
		t.Fatalf("location = %#v", loc)
	}
}

func TestNonFileMediaIgnored(t *testing.T) {
	m := &tg.Message{ID: 3, PeerID: &tg.PeerUser{UserID: 1}}
	m.SetMedia(&tg.MessageMediaGeo{Geo: &tg.GeoPoint{Lat: 1, Long: 2}})
	if _, ok := convert.ExtractMedia(m); ok {
		t.Fatal("geo treated as file")
	}
	if _, err := convert.FileLocation(m); err == nil {
		t.Fatal("FileLocation for geo must fail")
	}
}

func TestMessageResultCarriesMedia(t *testing.T) {
	res, ok, err := convert.Message(photoMessage(), convert.Options{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if len(res.Media) != 1 || res.Media[0].Key != "photo:777" || len(res.Message.MediaKeys) != 1 || res.Message.MediaKeys[0] != "photo:777" {
		t.Fatalf("result = %+v", res)
	}
}
