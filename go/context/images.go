package runcontext

// Images in tool results and attachments (agent#5), in the part packages/harness/images.ts the
// ledger, the reader and the search read: one value read as an image part, the line an image that
// cannot be shown leaves in its place, and a part resolved to the bytes in hand.
//
// What a run shows a model (separateImages, ImageRouting, the host's text rendering,
// humanWithImages) is the wire's, not this package's.

import (
	"strconv"
	"strings"
)

// ImageMediaTypes are the media types an image part may carry (IMAGE_MEDIA_TYPES).
var ImageMediaTypes = []string{"image/png", "image/jpeg", "image/webp"}

// ImageBytes is the most one image may hold, decoded: the Desk's bound on an attached document's
// original, 16 MiB. An image the ledger retains also counts against retainedBytes, as every item
// does.
const ImageBytes = 16 << 20

// ImageReferenceLength is the longest reference an image part may carry.
const ImageReferenceLength = 512

// ImagePart is one image, as the contract states it: its media type, and either its bytes
// (base64) or a reference to a retained image.
type ImagePart struct {
	MediaType string
	Bytes     string
	Reference string
}

// Value is the part as the contract writes it: kind, mediaType, then bytes or reference.
func (i ImagePart) Value() *Value {
	out := ObjectOf("kind", String("image"), "mediaType", String(i.MediaType))
	if i.Reference != "" {
		out.Set("reference", String(i.Reference))
		return out
	}
	out.Set("bytes", String(i.Bytes))
	return out
}

// DecodedBytes is how many bytes a base64 text decodes to (decodedBytes).
func DecodedBytes(base64 string) int {
	padding := 0
	if strings.HasSuffix(base64, "==") {
		padding = 2
	} else if strings.HasSuffix(base64, "=") {
		padding = 1
	}
	return len(base64)/4*3 - padding
}

// ReadImagePart is readImagePart: one value read as an image part, or the sentence saying why a
// value that is meant as an image cannot be one (problem, with the media type where it carries a
// string one), or neither where it is not meant as one. The contract's part is read closed; the
// MCP form ({type:'image',data,mimeType}) is admitted beside it.
func ReadImagePart(value *Value) (part *ImagePart, problem string, mediaType string, meant bool) {
	if !value.IsObject() {
		return nil, "", "", false
	}
	var held *Value
	if kind := value.Get("kind"); kind.IsString() && kind.Str() == "image" {
		for _, name := range value.Keys() {
			if name != "kind" && name != "mediaType" && name != "bytes" && name != "reference" {
				return nil, "an image part does not carry " + QuoteJSON(name), "", true
			}
		}
		held = value
	} else if kind := value.Get("type"); kind.IsString() && kind.Str() == "image" {
		held = ObjectOf("kind", String("image"))
		if media := value.Get("mimeType"); media != nil {
			held.Set("mediaType", media)
		}
		if data := value.Get("data"); data != nil {
			held.Set("bytes", data)
		}
	} else {
		return nil, "", "", false
	}
	media := held.Get("mediaType")
	known := false
	for _, name := range ImageMediaTypes {
		if media.IsString() && media.Str() == name {
			known = true
		}
	}
	if !known {
		if media.IsString() {
			return nil, "an image must be " + strings.Join(ImageMediaTypes, ", "), cutUTF16(media.Str(), 64), true
		}
		return nil, "an image must be " + strings.Join(ImageMediaTypes, ", "), "", true
	}
	bytes, reference := held.Get("bytes"), held.Get("reference")
	hasBytes, hasReference := bytes != nil, reference != nil
	if hasBytes == hasReference {
		return nil, "an image carries its bytes or a reference, one of the two", media.Str(), true
	}
	if hasReference {
		if !reference.IsString() || reference.Str() == "" || U16Length(reference.Str()) > ImageReferenceLength {
			return nil, "an image reference is a non-empty string of at most 512 characters", media.Str(), true
		}
		return &ImagePart{MediaType: media.Str(), Reference: reference.Str()}, "", media.Str(), true
	}
	if !bytes.IsString() || bytes.Str() == "" || U16Length(bytes.Str())%4 != 0 || !isBase64(bytes.Str()) {
		return nil, "an image’s bytes are base64", media.Str(), true
	}
	if DecodedBytes(bytes.Str()) > ImageBytes {
		return nil, "an image holds at most 16777216 bytes", media.Str(), true
	}
	return &ImagePart{MediaType: media.Str(), Bytes: bytes.Str()}, "", media.Str(), true
}

// isBase64 is /^[A-Za-z0-9+/]*={0,2}$/.
func isBase64(s string) bool {
	body := strings.TrimRight(s, "=")
	if len(s)-len(body) > 2 {
		return false
	}
	for _, c := range body {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/') {
			return false
		}
	}
	return true
}

// cutUTF16 is String.prototype.slice(0, n) over UTF-16 code units.
func cutUTF16(s string, n int) string { return U16Slice(s, 0, n) }

// NotShownLine is the line an image that cannot be shown leaves in its place, wherever it is read
// (notShownLine).
func NotShownLine(number int, reason, mediaType string) string {
	media := ""
	if mediaType != "" {
		media = " (" + mediaType + ")"
	}
	return "[image " + strconv.Itoa(number) + media + ": not shown, because " + reason + "]"
}

// ResolveImage is resolveImage: a value meant as an image, resolved — its bytes in hand (a
// reference followed to the image the execution retains), or the line saying why it cannot be
// shown; neither where the value is not meant as an image.
func ResolveImage(value *Value, resolve func(reference string) *ImagePart, number int) (image *ImagePart, line string, meant bool) {
	part, problem, mediaType, meant := ReadImagePart(value)
	if !meant {
		return nil, "", false
	}
	if problem != "" {
		return nil, NotShownLine(number, problem, mediaType), true
	}
	if part.Bytes != "" {
		return part, "", true
	}
	if held := resolve(part.Reference); held != nil {
		return held, "", true
	}
	return nil, NotShownLine(number, QuoteJSON(part.Reference)+" names no image this execution retains", part.MediaType), true
}
