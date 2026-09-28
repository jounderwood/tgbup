package convert

import (
	"errors"
	"fmt"

	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbp/internal/store"
)

func ExtractMedia(m *tg.Message) (store.Media, bool) {
	mc, ok := m.GetMedia()
	if !ok {
		return store.Media{}, false
	}
	switch v := mc.(type) {
	case *tg.MessageMediaPhoto:
		p, ok := photoOf(v)
		if !ok {
			return store.Media{}, false
		}
		_, size, ok := largestPhotoSize(p)
		if !ok {
			return store.Media{}, false
		}
		return store.Media{Key: fmt.Sprintf("photo:%d", p.ID), Kind: "photo", Mime: "image/jpeg", Size: size}, true
	case *tg.MessageMediaDocument:
		d, ok := documentOf(v)
		if !ok {
			return store.Media{}, false
		}
		return store.Media{
			Key:      fmt.Sprintf("document:%d", d.ID),
			Kind:     documentKind(v, d),
			FileName: documentFileName(d),
			Mime:     d.MimeType,
			Size:     d.Size,
		}, true
	}
	return store.Media{}, false
}

func FileLocation(mc tg.MessageClass) (tg.InputFileLocationClass, error) {
	m, ok := mc.(*tg.Message)
	if !ok {
		return nil, errors.New("not a regular message")
	}
	media, ok := m.GetMedia()
	if !ok {
		return nil, errors.New("message has no media")
	}
	switch v := media.(type) {
	case *tg.MessageMediaPhoto:
		p, ok := photoOf(v)
		if !ok {
			return nil, errors.New("photo is empty")
		}
		thumb, _, ok := largestPhotoSize(p)
		if !ok {
			return nil, errors.New("photo has no downloadable size")
		}
		return &tg.InputPhotoFileLocation{ID: p.ID, AccessHash: p.AccessHash, FileReference: p.FileReference, ThumbSize: thumb}, nil
	case *tg.MessageMediaDocument:
		d, ok := documentOf(v)
		if !ok {
			return nil, errors.New("document is empty")
		}
		return &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference}, nil
	}
	return nil, fmt.Errorf("media %T has no file", media)
}

func photoOf(v *tg.MessageMediaPhoto) (*tg.Photo, bool) {
	pc, ok := v.GetPhoto()
	if !ok {
		return nil, false
	}
	p, ok := pc.(*tg.Photo)
	return p, ok
}

func documentOf(v *tg.MessageMediaDocument) (*tg.Document, bool) {
	dc, ok := v.GetDocument()
	if !ok {
		return nil, false
	}
	d, ok := dc.(*tg.Document)
	return d, ok
}

func largestPhotoSize(p *tg.Photo) (thumbType string, size int64, ok bool) {
	best := -1
	for _, sc := range p.Sizes {
		var (
			typ     string
			area, n int
		)
		switch s := sc.(type) {
		case *tg.PhotoSize:
			typ, area, n = s.Type, s.W*s.H, s.Size
		case *tg.PhotoSizeProgressive:
			if len(s.Sizes) == 0 {
				continue
			}
			typ, area, n = s.Type, s.W*s.H, s.Sizes[len(s.Sizes)-1]
		default:
			continue
		}
		if area > best {
			best, thumbType, size, ok = area, typ, int64(n), true
		}
	}
	return thumbType, size, ok
}

func documentKind(mm *tg.MessageMediaDocument, d *tg.Document) string {
	var sticker, animated, video, audio bool
	round, voice := mm.Round, mm.Voice
	for _, a := range d.Attributes {
		switch v := a.(type) {
		case *tg.DocumentAttributeSticker:
			sticker = true
		case *tg.DocumentAttributeAnimated:
			animated = true
		case *tg.DocumentAttributeVideo:
			video = true
			round = round || v.RoundMessage
		case *tg.DocumentAttributeAudio:
			audio = true
			voice = voice || v.Voice
		}
	}
	switch {
	case sticker:
		return "sticker"
	case animated:
		return "animation"
	case round:
		return "video_note"
	case voice:
		return "voice"
	case video:
		return "video"
	case audio:
		return "audio"
	}
	return "document"
}

func documentFileName(d *tg.Document) string {
	for _, a := range d.Attributes {
		if f, ok := a.(*tg.DocumentAttributeFilename); ok {
			return f.FileName
		}
	}
	return ""
}
