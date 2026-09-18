// Package mediacache provides local media asset identity, derived-key cache,
// manifest pinning and garbage collection for WeChat image uploads.
package mediacache

// Register every image format the cache may decode. Standard jpeg/png/gif plus
// x/image webp/bmp so identity computation covers all extensions accepted by
// the publish pipeline.
import (
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)
