# Implement Slugify for ASCII URLs

Package `slug` (file `slug.go`) exports `func Slugify(s string) string`. Implement it. The signature and the package name stay as they are.

The contract:

- Only ASCII lowercase letters and ASCII digits are kept. ASCII uppercase letters are folded to lowercase and kept.
- Every maximal run of any other bytes, including non-ASCII letters, becomes one hyphen. Leading and trailing hyphens are removed.
- The result is at most 32 bytes. When the full slug is longer, it is cut so that no word is split: cut at the last hyphen at or before byte 32 and drop the hyphen; when there is no hyphen in the first 32 bytes, cut at byte 32.
- When nothing is kept, the result is `untitled`.
- Slugify of a slug is the same slug.

Examples: `Hello, World!` gives `hello-world`; `  Go 1.22 -- Released  ` gives `go-1-22-released`; `Ünïcode Title` gives `n-code-title`; `!!!` gives `untitled`.
