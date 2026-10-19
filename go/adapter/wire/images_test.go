package wire

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Images (images.test.ts): an image reaches a model whose capability record says vision as an
// image part, and any other model as the host's text rendering; never dropped in silence.

// pngImage is a 1×1 PNG, as the TypeScript suite's own is.
const pngImage = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// testRouting is the TypeScript suite's routing: a vision flag, the images a reference may name,
// and a host rendering that names what it was given, as the suite's host stub does.
func testRouting(vision bool, retained map[string]string) ImageRouting {
	return ImageRouting{
		Vision: vision,
		Resolve: func(reference string) *ImagePart {
			if held, ok := retained[reference]; ok {
				return &ImagePart{MediaType: "image/png", Bytes: held}
			}
			return nil
		},
		Render: func(context.Context, ImagePart) (string, error) {
			return "TEST RENDERING of a image/png image, 70 bytes", nil
		},
	}
}

func imageResult(image *Value) ToolResult {
	return ToolResult{Content: []*Value{Obj(F("type", Str("text")), F("text", Str("a screenshot of the form"))), image}}
}

// TestTheImagePartIsReadClosed is "is read closed, in the contract's shape and in MCP's".
func TestTheImagePartIsReadClosed(t *testing.T) {
	read := ReadImagePart(Obj(F("kind", Str("image")), F("mediaType", Str("image/png")), F("bytes", Str(pngImage))))
	if read == nil || read.Image == nil || *read.Image != (ImagePart{MediaType: "image/png", Bytes: pngImage}) {
		t.Errorf("the contract's shape read as %+v", read)
	}
	read = ReadImagePart(Obj(F("kind", Str("image")), F("mediaType", Str("image/webp")), F("reference", Str("attachment-1"))))
	if read == nil || read.Image == nil || *read.Image != (ImagePart{MediaType: "image/webp", Reference: "attachment-1"}) {
		t.Errorf("a reference read as %+v", read)
	}
	read = ReadImagePart(Obj(F("type", Str("image")), F("mimeType", Str("image/jpeg")), F("data", Str(pngImage))))
	if read == nil || read.Image == nil || *read.Image != (ImagePart{MediaType: "image/jpeg", Bytes: pngImage}) {
		t.Errorf("MCP's shape read as %+v", read)
	}
	if ReadImagePart(Obj(F("type", Str("text")), F("text", Str("not an image")))) != nil {
		t.Error("a text part was read as an image")
	}
}

// TestNamesWhatIsWrong is "names what is wrong with one that cannot be shown".
func TestNamesWhatIsWrong(t *testing.T) {
	for _, held := range []struct {
		what  string
		value *Value
		want  string
	}{
		{"an unsupported media type", Obj(F("kind", Str("image")), F("mediaType", Str("image/gif")), F("bytes", Str(pngImage))), "image/png, image/jpeg, image/webp"},
		{"both bytes and a reference", Obj(F("kind", Str("image")), F("mediaType", Str("image/png")), F("bytes", Str(pngImage)), F("reference", Str("x"))), "one of the two"},
		{"bytes that are not base64", Obj(F("kind", Str("image")), F("mediaType", Str("image/png")), F("bytes", Str("not base64!"))), "base64"},
		{"a member the part does not carry", Obj(F("kind", Str("image")), F("mediaType", Str("image/png")), F("bytes", Str(pngImage)), F("caption", Str("x"))), `"caption"`},
		{"an image over the bound", Obj(F("kind", Str("image")), F("mediaType", Str("image/png")), F("bytes", Str(strings.Repeat("AAAA", ImageBytes/3+1)))), fmt.Sprintf("%d bytes", ImageBytes)},
	} {
		read := ReadImagePart(held.value)
		if read == nil || read.Image != nil {
			t.Fatalf("%s was taken as an image", held.what)
		}
		contains(t, held.what, read.Problem, held.want)
	}
}

// TestAnImageInAResult is "an image in a result": taken out of the text and kept for a vision
// model, with a line in its place.
func TestAnImageInAResult(t *testing.T) {
	ctx := context.Background()
	image := Obj(F("kind", Str("image")), F("mediaType", Str("image/png")), F("bytes", Str(pngImage)))
	shown, images, err := SeparateImages(ctx, imageResult(image), testRouting(true, nil))
	if err != nil {
		t.Fatalf("a vision model: %v", err)
	}
	if len(images) != 1 || images[0] != (ImagePart{MediaType: "image/png", Bytes: pngImage}) {
		t.Fatalf("the images kept are %+v", images)
	}
	equalJSON(t, "the result a vision model is shown", Arr(shown.Content...), Arr(
		Obj(F("type", Str("text")), F("text", Str("a screenshot of the form"))),
		Obj(F("type", Str("text")), F("text", Str("[image 1 (image/png, 70 bytes): shown to you as an image with this result]"))),
	))

	// The host's rendering for any other model, and never the image.
	rendered, images, err := SeparateImages(ctx, imageResult(image), testRouting(false, nil))
	if err != nil {
		t.Fatalf("a model without vision: %v", err)
	}
	if len(images) != 0 {
		t.Errorf("a model without vision was handed %d images", len(images))
	}
	equalText(t, "the line in its place", rendered.Content[1].Get("text").Text(),
		"[image 1 (image/png, 70 bytes): this model is not shown images; the host's text rendering of it follows]\nTEST RENDERING of a image/png image, 70 bytes")
	if strings.Contains(Arr(rendered.Content...).String(), pngImage) {
		t.Error("the image's bytes reached the result text")
	}
}

// TestAnImageThatCannotBeShown is "says so where it cannot be shown: a failed rendering, a
// reference to nothing, a malformed part".
func TestAnImageThatCannotBeShown(t *testing.T) {
	ctx := context.Background()
	image := Obj(F("kind", Str("image")), F("mediaType", Str("image/png")), F("bytes", Str(pngImage)))
	failing := testRouting(false, nil)
	failing.Render = func(context.Context, ImagePart) (string, error) {
		return "", errors.New("the render adapter is not configured")
	}
	held, _, err := SeparateImages(ctx, imageResult(image), failing)
	if err != nil {
		t.Fatalf("a failed rendering: %v", err)
	}
	contains(t, "a failed rendering", held.Content[1].Get("text").Text(), "the host could not render it as text (the render adapter is not configured)")

	missing := Obj(F("kind", Str("image")), F("mediaType", Str("image/png")), F("reference", Str("gone")))
	held, _, err = SeparateImages(ctx, imageResult(missing), testRouting(true, nil))
	if err != nil {
		t.Fatalf("a reference to nothing: %v", err)
	}
	contains(t, "a reference to nothing", held.Content[1].Get("text").Text(), `"gone" names no image this execution retains`)

	kept := Obj(F("kind", Str("image")), F("mediaType", Str("image/png")), F("reference", Str("kept")))
	_, images, err := SeparateImages(ctx, imageResult(kept), testRouting(true, map[string]string{"kept": pngImage}))
	if err != nil {
		t.Fatalf("a reference the execution retains: %v", err)
	}
	if len(images) != 1 || images[0] != (ImagePart{MediaType: "image/png", Bytes: pngImage}) {
		t.Errorf("a retained reference resolved to %+v", images)
	}

	malformed := Obj(F("kind", Str("image")), F("mediaType", Str("image/png")))
	held, _, err = SeparateImages(ctx, imageResult(malformed), testRouting(true, nil))
	if err != nil {
		t.Fatalf("a malformed part: %v", err)
	}
	contains(t, "a malformed part", held.Content[1].Get("text").Text(), "not shown, because an image carries its bytes or a reference")
}

// TestAResultWithNoImage is "leaves a result with no image as it was, the same object".
func TestAResultWithNoImage(t *testing.T) {
	plain := ToolResult{Content: []*Value{Obj(F("type", Str("text")), F("text", Str("no image")))}}
	held, images, err := SeparateImages(context.Background(), plain, testRouting(true, nil))
	if err != nil {
		t.Fatalf("a result with no image: %v", err)
	}
	if len(images) != 0 || len(held.Content) != 1 || held.Content[0] != plain.Content[0] {
		t.Error("a result with no image was rewritten")
	}
}
