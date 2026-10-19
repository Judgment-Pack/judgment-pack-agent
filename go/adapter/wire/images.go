package wire

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// Images in tool results and attachments (images.ts).
//
// The contract's image part is {kind: 'image', mediaType, bytes} (base64) or {kind: 'image',
// mediaType, reference} (an image the execution retains, by the id of the attachment or ledger
// item that holds it). The MCP form of an image content item, {type: 'image', data, mimeType}, is
// read as the same part.
//
// What the model is shown depends on one flag of its capability record, vision, and an image is
// never dropped in silence: a vision model is shown the image as an image part in the message
// after the result, any other model is shown the host's text rendering in its place, and an image
// that cannot be shown is a line in the result saying so. The image's bytes are never part of the
// result text.

// ImageMediaTypes is IMAGE_MEDIA_TYPES: the media types an image part may carry.
var ImageMediaTypes = []string{"image/png", "image/jpeg", "image/webp"}

// ImageBytes is IMAGE_BYTES: the most one image may hold, decoded — the Desk's bound on an
// attached document's original, 16 MiB.
const ImageBytes = 16 << 20

// ImageReferenceLength is IMAGE_REFERENCE_LENGTH: the longest reference an image part may carry.
const ImageReferenceLength = 512

// ImagePart is one image, as the contract states it (ImagePart in images.ts). Bytes and Reference
// are one of the two: a held image is one whose Bytes is set (the TypeScript's HeldImage).
type ImagePart struct {
	MediaType string
	// Bytes is the image, base64, or "" where the part names a reference instead.
	Bytes string
	// Reference is a retained image by its attachment or ledger id, or "".
	Reference string
}

// Base64 characters, then at most two of padding: one character class, so the check is linear on
// any length.
var base64Text = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)

// DecodedBytes is decodedBytes: how many bytes a base64 text decodes to. The TypeScript divides
// without rounding, and a length that is not a multiple of four answers a fraction, which the
// lines below print as JavaScript prints it.
func DecodedBytes(base64 string) float64 {
	size := float64(utf16Length(base64)) / 4 * 3
	switch {
	case strings.HasSuffix(base64, "=="):
		return size - 2
	case strings.HasSuffix(base64, "="):
		return size - 1
	}
	return size
}

// ImageRead is one value read as an image part (readImagePart's answer): the part, or a sentence
// saying why a value that is meant as an image cannot be one.
type ImageRead struct {
	// Image is the part, where the value is one.
	Image *ImagePart
	// Problem is why a value meant as an image cannot be one; "" where Image is set.
	Problem string
	// MediaType is the type a problem names, where it names one.
	MediaType string
}

// ReadImagePart is readImagePart: one value read as an image part, or nil where it is not meant as
// one. The contract's part is read closed; the MCP form is admitted beside it.
func ReadImagePart(value *Value) *ImageRead {
	if !value.IsObject() {
		return nil
	}
	var part *Value
	switch {
	case value.Get("kind").Text() == "image":
		for _, name := range value.Keys() {
			if !slices.Contains([]string{"kind", "mediaType", "bytes", "reference"}, name) {
				return &ImageRead{Problem: fmt.Sprintf("an image part does not carry %s", jsQuote(name))}
			}
		}
		part = value
	case value.Get("type").Text() == "image":
		part = Obj(F("kind", Str("image")), F("mediaType", value.Get("mimeType")), F("bytes", value.Get("data")))
	default:
		return nil
	}
	mediaType := part.Get("mediaType")
	if !mediaType.IsString() || !slices.Contains(ImageMediaTypes, mediaType.Text()) {
		read := &ImageRead{Problem: fmt.Sprintf("an image must be %s", strings.Join(ImageMediaTypes, ", "))}
		if mediaType.IsString() {
			read.MediaType = utf16Slice(mediaType.Text(), 64)
		}
		return read
	}
	bytes, reference := part.Get("bytes"), part.Get("reference")
	if (bytes != nil) == (reference != nil) {
		return &ImageRead{Problem: "an image carries its bytes or a reference, one of the two", MediaType: mediaType.Text()}
	}
	if reference != nil {
		if !reference.IsString() || reference.Text() == "" || utf16Length(reference.Text()) > ImageReferenceLength {
			return &ImageRead{
				Problem:   fmt.Sprintf("an image reference is a non-empty string of at most %d characters", ImageReferenceLength),
				MediaType: mediaType.Text(),
			}
		}
		return &ImageRead{Image: &ImagePart{MediaType: mediaType.Text(), Reference: reference.Text()}}
	}
	text := bytes.Text()
	if !bytes.IsString() || text == "" || utf16Length(text)%4 != 0 || !base64Text.MatchString(text) {
		return &ImageRead{Problem: "an image’s bytes are base64", MediaType: mediaType.Text()}
	}
	if DecodedBytes(text) > ImageBytes {
		return &ImageRead{Problem: fmt.Sprintf("an image holds at most %d bytes", ImageBytes), MediaType: mediaType.Text()}
	}
	return &ImageRead{Image: &ImagePart{MediaType: mediaType.Text(), Bytes: text}}
}

// NotShownLine is notShownLine: the line an image that cannot be shown leaves in its place,
// wherever it is read.
func NotShownLine(number int, reason, mediaType string) string {
	if mediaType != "" {
		return fmt.Sprintf("[image %d (%s): not shown, because %s]", number, mediaType, reason)
	}
	return fmt.Sprintf("[image %d: not shown, because %s]", number, reason)
}

// ResolvedImage is resolveImage's answer: the image with its bytes in hand, or the line saying why
// it cannot be shown.
type ResolvedImage struct {
	Image *ImagePart
	Line  string
}

// ResolveImage is resolveImage: a value meant as an image, resolved — its bytes in hand (a
// reference followed to the image the execution retains, through the function the caller passes),
// or the line saying why it cannot be shown; nil where the value is not meant as an image.
func ResolveImage(value *Value, resolve func(reference string) *ImagePart, number int) *ResolvedImage {
	read := ReadImagePart(value)
	if read == nil {
		return nil
	}
	if read.Image == nil {
		return &ResolvedImage{Line: NotShownLine(number, read.Problem, read.MediaType)}
	}
	if read.Image.Bytes != "" {
		return &ResolvedImage{Image: read.Image}
	}
	if held := resolve(read.Image.Reference); held != nil {
		return &ResolvedImage{Image: held}
	}
	return &ResolvedImage{Line: NotShownLine(number,
		fmt.Sprintf("%s names no image this execution retains", jsQuote(read.Image.Reference)), read.Image.MediaType)}
}

// ImageRouting is how a run shows images (ImageRouting in images.ts): the model's vision flag, the
// retained images a reference may name, and the host's rendering of one as text. The TypeScript
// reads the host's renderImageToText off the module; here it is a member, as the brief states, and
// the run's signal is the context.
type ImageRouting struct {
	Vision bool
	// Resolve is a retained image by its reference, or nil where the execution holds none by it.
	Resolve func(reference string) *ImagePart
	// Render is the host's render adapter: an image as text, for a model that is not shown images.
	Render func(ctx context.Context, image ImagePart) (string, error)
}

// ToolResult is one tool answer in the shape a runtime answer takes (McpToolResult in engine.ts).
// Its content items are kept as the values they arrived as, so an item this package does not read
// travels exactly as it came.
type ToolResult struct {
	Content           []*Value
	IsError           *bool
	StructuredContent *Value
	// Name is the tool that answered, which the images message names.
	Name string
}

// SeparateImages is separateImages: one tool result with its images taken out of its content and a
// line in each one's place, and the images a vision model is to be shown after it. A result that
// carries no image is returned as it is.
func SeparateImages(ctx context.Context, result ToolResult, routing ImageRouting) (ToolResult, []ImagePart, error) {
	read := make([]*ImageRead, len(result.Content))
	found := false
	for at, item := range result.Content {
		read[at] = ReadImagePart(item)
		if read[at] != nil {
			found = true
		}
	}
	if !found {
		return result, nil, nil
	}
	var images []ImagePart
	parts := make([]*Value, 0, len(result.Content))
	number := 0
	for at, item := range result.Content {
		if read[at] == nil {
			parts = append(parts, item)
			continue
		}
		number++
		line, held, err := imageLine(ctx, number, item, routing)
		if err != nil {
			return result, nil, err
		}
		if held != nil {
			images = append(images, *held)
		}
		parts = append(parts, Obj(F("type", Str("text")), F("text", Str(line))))
	}
	separated := result
	separated.Content = parts
	return separated, images, nil
}

// imageLine is the line one image leaves in a result, and the image a vision model is shown.
func imageLine(ctx context.Context, number int, value *Value, routing ImageRouting) (string, *ImagePart, error) {
	held := ResolveImage(value, routing.Resolve, number)
	if held == nil {
		return NotShownLine(number, "it is not an image part", ""), nil, nil
	}
	if held.Image == nil {
		return held.Line, nil, nil
	}
	image := *held.Image
	size := fmt.Sprintf("%s, %s bytes", image.MediaType, jsNumber(DecodedBytes(image.Bytes)))
	if routing.Vision {
		return fmt.Sprintf("[image %d (%s): shown to you as an image with this result]", number, size), &image, nil
	}
	if err := ctx.Err(); err != nil {
		return "", nil, failure.Cancelled
	}
	text, err := routing.Render(ctx, image)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, err
		}
		return NotShownLine(number,
			fmt.Sprintf("this model is not shown images and the host could not render it as text (%s)",
				utf16Slice(err.Error(), 200)), size), nil, nil
	}
	return fmt.Sprintf("[image %d (%s): this model is not shown images; the host's text rendering of it follows]\n%s", number, size, text), nil, nil
}

// utf16Slice is String.prototype.slice(0, n): the string cut to its first n UTF-16 code units.
func utf16Slice(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}
