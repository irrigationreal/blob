package server

import "testing"

func TestIsBlobBuildImageRefRecognizesCurrentAndLegacyTags(t *testing.T) {
	current := newBuildTag("demo")
	for _, image := range []string{"demo:" + current, "demo:1234567890"} {
		if !isBlobBuildImageRef(image, "demo") {
			t.Fatalf("expected %q to be recognized as a blob build image", image)
		}
	}
}

func TestIsBlobBuildImageRefLeavesPublicImagesAlone(t *testing.T) {
	for _, image := range []string{
		"nginx:alpine",
		"demo:alpine",
		"other:1234567890",
		"registry.example/demo:" + newBuildTag("demo"),
	} {
		if isBlobBuildImageRef(image, "demo") {
			t.Fatalf("expected %q to stay public/explicit", image)
		}
	}
}
